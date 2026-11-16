---
id: event-queue-and-backfill
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-15
version: 7
affects:
  - internal/queue/*.go
  - internal/ingest/*.go
  - internal/agent/*.go
  - internal/cursor/*.go
  - internal/adapter/claudecode/backfill.go
  - internal/adapter/claudecode/sideusage.go
  - cmd/neo-tax-agent/*.go
  - plugins/claude-code/skills/backfill/SKILL.md
  - internal/adapter/claudecode/hooks.go
---

# Event queue and backfill

Governs how the agent stores session events on disk between hooks, flushes them to the ingest endpoint, backs off on failure, bounds its own footprint, and backfills transcripts that predate enrollment.

## Requirements

- `queue-is-jsonl-segments`(tested): Queued events are appended one per line to an active segment file under a queue directory in the harness-provided plugin data directory; a flush first seals the active segment into a numbered one and sends sealed segments only.
- `segment-rotation`(tested): A segment is closed and a new one opened when it exceeds 1 MB.
- `no-database`(tested): The queue uses no embedded database; a corrupt segment loses at most that segment.
- `cursor-per-transcript`(tested): A cursor file records, for each transcript path including subagent transcripts, the last emitted message id and byte offset.
- `flush-on-every-hook`(tested): Every hook invocation attempts a flush after queuing its own events.
- `flush-batch-size`(tested): A flush sends oldest events first, at most 500 per request.
- `segment-advances-on-success`(tested): A flush deletes a segment only once every line in it has been accepted or recorded as a duplicate by the server, or dead-lettered (a schema rejection or a corrupt line); until then it records how many of the segment's lines it has handled, and a failed send records none. The only other way a segment is deleted is `queue-cap`.
- `backoff-persisted`(tested): After a failed flush the agent records a next-attempt time, starting at 30 seconds and doubling to a ceiling of 1 hour, and no hook flushes before that time.
- `backoff-resets-on-success`(tested): A successful flush resets the backoff.
- `schema-rejection-dead-letters`(tested): Events the server rejects for schema reasons are moved to a dead-letter directory with the server's reason, and the remaining events continue.
- `dead-letter-retention`(tested): Dead-letter files older than 14 days are deleted at the next hook.
- `auth-rejection-stops-flushing`(tested): A 401 or 403 marks the device as needing re-enrollment; events keep queuing, and no flush is attempted until enrollment succeeds.
- `server-and-network-errors-back-off`(tested): A 5xx response or a network error triggers backoff and leaves the batch in place.
- `queue-cap`(tested): The queue is capped at 50 MB; when exceeded, the oldest segment is deleted.
- `dropped-events-reported`(tested): The number of events deleted by the cap is counted and carried as `dropped_events` on the next `session.opened` or `session.closed` event, then reset.
- `flush-lock`(tested): A flush lock file, separate from the state lock that serializes appends and queue state, keeps two flushes from running at once; a hook that finds it held exits without flushing and leaves its events for the next hook.
- `backfill-scope`(tested): A backfill enumerates the harness's main transcripts on disk that `backfill-session-id-from-harness` accepts, and emits events from the beginning for each one `backfill-skips-read-transcripts` leaves, newest transcript first, reading each session's subagent transcripts with its main one. For Claude Code that is `projects/*/<session id>.jsonl`; the top-level `agent-*.jsonl` subagent transcripts older Claude Code builds wrote are not read, and a transcript with no timestamped line is skipped.
- `backfill-skips-read-transcripts`(tested): Without `--restart`, a backfill leaves every cursor as it is and skips each transcript that has one, since a hook or an earlier backfill read it and queued its events; a re-enrollment therefore sends only sessions the agent has not seen. With `--restart`, it resets every cursor and reads every transcript again.
- `backfill-session-id-from-harness`(tested): A backfill reads only transcripts whose file name has the shape of the harness's session id (for Claude Code, a lowercase UUID), and uses that name as the session id. Any other file under the harness's transcript directory is skipped, so a file name the engineer chose never reaches an event.
- `backfill-skips-fork-copies`(tested): A backfill sends a fork's copies of its parent's turns and pull request links once, under the parent, and opens the fork with `source` fork. Transcripts that share their first line's uuid are a fork family; within one, a transcript all of whose line uuids another has comes first, then the one whose first timestamped line is earlier, and each later transcript's lines whose uuids an earlier one has are copies. Side usage still subtracts the copied turns, as at `SessionEnd`.
- `backfill-after-login`(tested): An interactive enrollment starts a backfill immediately after the device key is stored.
- `backfill-lock`(tested): A backfill holds a lock so that two backfills cannot run at once; a second request exits.
- `backfill-command`(tested): The agent exposes a backfill command, reachable through the harness's `neo-tax:backfill` entry point. When the last backfill paused or was killed after recording a resume point, the command resumes it; otherwise it backfills the transcripts the agent has not read, newest first. With `--restart` it resets every cursor and re-runs the backfill over every transcript from the newest.
- `backfill-waits-out-backoff`(tested): When the queue holds more than the backfill's queue limit (half the queue cap) after a flush, the backfill stops reading transcripts, sleeps until the recorded next-attempt time, and flushes again, until the queue is back under the limit. It stops early only when the device needs re-enrollment, when it is canceled, or when its total wait passes a budget of 6 hours; each of those records the backfill as paused. After the last transcript it waits the same way for the queue to empty, and if the budget runs out there it finishes unpaused, since every event is queued and hooks send the rest.
- `backfill-resumes-after-pause`(tested): A backfill records in `backfill.json`, after each flushed batch and when it pauses, a resume point that names no transcript: the modification time of the last transcript it queued and how many transcripts at that time, in path order, it queued; a backfill that finishes clears it. The next backfill without `--restart`, after one that paused or was killed, skips every transcript modified after that time and those counted at it, reads the rest from the beginning, and leaves every cursor as it is.
- `backfill-closes-ended-sessions`(tested): For a past session whose main transcript ends with current session totals, a backfill emits a `session.closed` after its turns, with `ended_at` the latest timestamp the transcript records, `reason` null, `dropped_events` 0, and `side_usage` computed as at `SessionEnd` without waiting; a session without current totals, or whose side usage cannot be computed, gets no `session.closed`.
- `backfill-idempotent-server-side`(tested): A backfill that reads a transcript whose events the server already accepted (any `--restart` run, or a transcript whose cursor was lost) resends them, and relies on the server's idempotency key to discard them.
- `no-daemon`(built): The agent runs only when a hook or a command invokes it; nothing runs between invocations.
  - status-reason: structural; the agent is a short-lived process with no background goroutine or service, which no test can observe

## Design Notes

Flushing on every hook rather than on a timer is what makes the no-daemon model work: a laptop that is offline for a week sends nothing, and the first hook after it reconnects drains the queue in batches of 500. The persisted backoff is what keeps that from turning into a retry on every turn while offline.

Backfill runs newest first because the most recent work is what an engineer and an operator want to see land; retention bounds how far back it reaches, so no cutoff is needed.

One failed ingest request puts the queue into backoff, during which a flush is a no-op, while a backfill reads transcripts at disk speed. If the backfill stopped as soon as the queue passed its limit, one 5xx would end it within a second of the failure, and after login nobody would see the message. So it waits out the backoff instead: the backoff is at most an hour per attempt, the backfill runs detached under its own command, and the 6-hour budget bounds a backfill against a server that stays down.

A pause records a resume point because a `--restart` run resets every cursor and starts again from the newest transcript, so a restarted backfill that kept pausing would never reach the oldest sessions; without `--restart`, the cursors the paused run recorded already skip what it queued. The resume point is a modification time and a count rather than a list of transcripts, so `backfill.json` holds no paths. Transcripts modified after the boundary are skipped on resume whether the paused run queued them or not: if the paused run reset every cursor, a hook that wrote to an older transcript since read it from the start. Cursors are not reset on resume for the same reason: the queued transcripts keep theirs, and the rest have none unless a hook has read them since. `--restart` exists for an engineer who wants everything resent regardless; the skill passes it only when asked to start over.

A turn carries no repository, so a backfill emits a `session.opened` for each past session before its turns: `started_at`, `entrypoint`, and `branch` come from the transcript; `repo` comes from the session's working directory as it is today (read locally from the transcript, never sent); `source` is fork for a fork `backfill-skips-fork-copies` finds and otherwise null, and `head_sha` is null, because the harness's reason and the commit at the time are unknown. If hooks already sent the session's `session.opened`, the server's idempotency key discards the backfilled one. A backfill closes a past session only when its transcript ends with current session totals, which Claude Code writes on a clean exit; that is what makes it safe to close a session from its transcript, since a session still running, or one that crashed, has none, and is left for its hooks or the server's inactivity rule. The close exists to carry the session's side usage, which no turn reports. Its `ended_at` comes from the transcript, so a re-run backfill sends the same close, and a close the `SessionEnd` hook sent, timestamped when the hook ran, is later and supersedes it.

Interactive login starts the backfill as a detached `neo-tax-agent backfill` process, so login returns at once.

The random 0–30 minute delay before backfill belongs to managed enrollment, where a fleet enrolls at once; interactive enrollment has a human waiting and starts immediately.

## Risks

- **A skipped transcript whose events were lost.** A transcript with a cursor is skipped on the assumption its events were queued and sent; if the queue's cap deleted them before they were sent, a plain backfill does not resend them. Accepted: the cap reports how many it dropped as `dropped_events`, and `--restart` resends everything.
- **A transcript whose final tail no hook read.** A plain backfill also skips a transcript whose cursor stops short of its end: a `SessionEnd` that could not take the cursor lock in time, or a headless `claude -p` run, where Claude Code kills async hooks still running at teardown and does not enforce their `timeout`, so `SessionEnd` can die before it reads the tail or queues `session.closed`. Those last turns and the close then go out only with `--restart`. A hook killed after queuing but before flushing loses nothing: its events wait for the next hook on the machine.
- **Sessions ended by `/clear`, logout, or `/resume` never report side usage.** Claude Code writes no session totals for them, so neither `SessionEnd` nor `backfill-closes-ended-sessions` can compute it, and a backfill sends no `session.closed` for them. Accepted for v0.1; telemetry reports those side calls as turns where it is configured.
- **Backfill drops parse failures silently.** A transcript line that does not parse is skipped, as at a hook, but a backfill neither counts nor logs it, so a harness format change shows up in hooks' `parse_failures` log lines and not in a backfill.
- **Forks are inferred.** Claude Code marks neither a fork nor its copied lines, so a backfill infers both from shared line uuids. On one engineer's 264 transcripts this found 3 forks, matched each to the parent created first, and left no turn sent under two sessions (36 before). A fork whose parent's transcript is gone, or whose first line was compacted away, is not found; its copies then go out under the fork, and the server's turn key still keeps the usage from counting twice. When a parent went on after the fork and the fork lacks its own start lines, the order falls back to the file path.
- **A long backfill under the hook's time budget.** The harness may kill a hook that runs too long. Mitigated by backfill running under its own command after login, not under a hook, and by a paused backfill resuming from its recorded point.
- **A resume point counted at a shared modification time.** If a transcript the paused run queued at the boundary time is modified before the resume, the count at the boundary now covers one transcript it did not queue, which the resume skips. This needs two transcripts with the same modification time to the nanosecond and a write to one of them in between; accepted, and `--restart` recovers it.
- **A backfill process lives up to six hours.** Waiting out backoff keeps the detached process alive for as long as the wait budget. It is still one command's run, so `no-daemon` holds, but an engineer or a security reviewer may notice a process that outlives the login by hours; the budget is kept to six so an outage longer than that pauses the backfill for a later run to resume, rather than keeping it alive; it sleeps rather than polls and holds only the backfill lock, so a second backfill exits instead of piling up.
- **Two harnesses on one machine share the plugin data layout.** Each plugin has its own data directory, so queues do not collide; accepted.
- **The 50 MB cap drops events silently from the engineer's point of view.** The server sees `dropped_events` and the operator view shows the gap; accepted.
