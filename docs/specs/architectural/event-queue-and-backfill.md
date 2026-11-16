---
id: event-queue-and-backfill
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-14
version: 3
affects:
  - internal/queue/*.go
  - internal/ingest/*.go
  - internal/agent/*.go
  - internal/cursor/*.go
  - internal/adapter/claudecode/backfill.go
  - cmd/neo-tax-agent/*.go
  - plugins/claude-code/skills/backfill/SKILL.md
  - internal/adapter/claudecode/hooks.go
---

# Event queue and backfill

Governs how the agent stores session events on disk between hooks, flushes them to the ingest endpoint, backs off on failure, bounds its own footprint, and backfills transcripts that predate enrollment.

## Requirements

- `queue-is-jsonl-segments`(tested): Queued events are appended one per line to segment files under a queue directory in the harness-provided plugin data directory.
- `segment-rotation`(tested): A segment is closed and a new one opened when it exceeds 1 MB.
- `no-database`(tested): The queue uses no embedded database; a corrupt segment loses at most that segment.
- `cursor-per-transcript`(tested): A cursor file records, for each transcript path including subagent transcripts, the last emitted message id and byte offset.
- `flush-on-every-hook`(tested): Every hook invocation attempts a flush after queuing its own events.
- `flush-batch-size`(tested): A flush sends oldest events first, at most 500 per request.
- `segment-advances-on-success`(tested): A segment is deleted only after every event in it has been accepted or recorded as a duplicate by the server.
- `backoff-persisted`(tested): After a failed flush the agent records a next-attempt time, starting at 30 seconds and doubling to a ceiling of 1 hour, and no hook flushes before that time.
- `backoff-resets-on-success`(tested): A successful flush resets the backoff.
- `schema-rejection-dead-letters`(tested): Events the server rejects for schema reasons are moved to a dead-letter directory with the server's reason, and the remaining events continue.
- `dead-letter-retention`(tested): Dead-letter files older than 14 days are deleted at the next hook.
- `auth-rejection-stops-flushing`(tested): A 401 or 403 marks the device as needing re-enrollment; events keep queuing, and no flush is attempted until enrollment succeeds.
- `server-and-network-errors-back-off`(tested): A 5xx response or a network error triggers backoff and leaves the batch in place.
- `queue-cap`(tested): The queue is capped at 50 MB; when exceeded, the oldest segment is deleted.
- `dropped-events-reported`(tested): The number of events deleted by the cap is counted and carried as `dropped_events` on the next `session.opened` or `session.closed` event, then reset.
- `flush-lock`(tested): A single lock file serializes flushes; a hook that cannot acquire it exits without flushing and leaves its events for the next hook.
- `backfill-scope`(tested): A backfill enumerates every transcript on disk for the harness and emits events for each from the beginning, newest transcript first.
- `backfill-session-id-from-harness`(tested): A backfill reads only transcripts whose file name has the shape of the harness's session id (for Claude Code, a lowercase UUID), and uses that name as the session id. Any other file under the harness's transcript directory is skipped, so a file name the engineer chose never reaches an event.
- `backfill-after-login`(tested): An interactive enrollment starts a backfill immediately after the device key is stored.
- `backfill-lock`(tested): A backfill holds a lock so that two backfills cannot run at once; a second request exits.
- `backfill-command`(tested): The agent exposes a backfill command, reachable through the harness's `neo-tax:backfill` entry point. When the last backfill paused or was killed after recording a resume point, the command resumes it; otherwise, or when run with `--restart`, it resets every cursor and re-runs the backfill from the newest transcript.
- `backfill-waits-out-backoff`(tested): When the queue holds more than the backfill's queue limit (half the queue cap) after a flush, the backfill stops reading transcripts, sleeps until the recorded next-attempt time, and flushes again, until the queue is back under the limit. It stops early only when the device needs re-enrollment, when it is canceled, or when its total wait passes a budget of 6 hours; each of those records the backfill as paused. After the last transcript it waits the same way for the queue to empty, and if the budget runs out there it finishes unpaused, since every event is queued and hooks send the rest.
- `backfill-resumes-after-pause`(tested): A backfill records in `backfill.json`, after each flushed batch and when it pauses, a resume point that names no transcript: the modification time of the last transcript it queued and how many transcripts at that time, in path order, it queued; a backfill that finishes clears it. The next backfill without `--restart`, after one that paused or was killed, skips every transcript modified after that time and those counted at it, reads the rest from the beginning, and leaves every cursor as it is.
- `backfill-idempotent-server-side`(tested): A re-run backfill resends events the server has already accepted, and relies on the server's idempotency key to discard them.
- `no-daemon`(built): The agent runs only when a hook or a command invokes it; nothing runs between invocations.
  - status-reason: structural; the agent is a short-lived process with no background goroutine or service, which no test can observe

## Design Notes

Flushing on every hook rather than on a timer is what makes the no-daemon model work: a laptop that is offline for a week sends nothing, and the first hook after it reconnects drains the queue in batches of 500. The persisted backoff is what keeps that from turning into a retry on every turn while offline.

Backfill runs newest first because the most recent work is what an engineer and an operator want to see land; retention bounds how far back it reaches, so no cutoff is needed.

One failed ingest request puts the queue into backoff, during which a flush is a no-op, while a backfill reads transcripts at disk speed. If the backfill stopped as soon as the queue passed its limit, one 5xx would end it within a second of the failure, and after login nobody would see the message. So it waits out the backoff instead: the backoff is at most an hour per attempt, the backfill runs detached under its own command, and the 24-hour budget bounds a backfill against a server that stays down.

A pause records a resume point because a fresh run resets every cursor and starts again from the newest transcript, so a backfill that kept pausing would never reach the oldest sessions. The resume point is a modification time and a count rather than a list of transcripts, so `backfill.json` holds no paths. Transcripts modified after the boundary are skipped on resume whether the paused run queued them or not: the paused run reset every cursor, so a hook that wrote to an older transcript since read it from the start. Cursors are not reset on resume for the same reason: the queued transcripts keep theirs, and the rest have none unless a hook has read them since. `--restart` exists for an engineer who wants everything resent regardless; the skill passes it only when asked to start over.

A turn carries no repository, so a backfill emits a `session.opened` for each past session before its turns: `started_at`, `entrypoint`, and `branch` come from the transcript; `repo` and `git_user_email` come from the session's working directory as it is today (read locally from the transcript, never sent); `source` and `head_sha` are null because the harness's reason and the commit at the time are unknown. If hooks already sent the session's `session.opened`, the server's idempotency key discards the backfilled one. Interactive login starts the backfill as a detached `neo-tax-agent backfill` process, so login returns at once.

The random 0–30 minute delay before backfill belongs to managed enrollment, where a fleet enrolls at once; interactive enrollment has a human waiting and starts immediately.

## Risks

- **A long backfill under the hook's time budget.** The harness may kill a hook that runs too long. Mitigated by backfill running under its own command after login, not under a hook, and by a paused backfill resuming from its recorded point.
- **A resume point counted at a shared modification time.** If a transcript the paused run queued at the boundary time is modified before the resume, the count at the boundary now covers one transcript it did not queue, which the resume skips. This needs two transcripts with the same modification time to the nanosecond and a write to one of them in between; accepted, and `--restart` recovers it.
- **A backfill process lives up to six hours.** Waiting out backoff keeps the detached process alive for as long as the wait budget. It is still one command's run, so `no-daemon` holds, but an engineer or a security reviewer may notice a process that outlives the login by hours; the budget is kept to six so an outage longer than that pauses the backfill for a later run to resume, rather than keeping it alive; it sleeps rather than polls and holds only the backfill lock, so a second backfill exits instead of piling up.
- **Two harnesses on one machine share the plugin data layout.** Each plugin has its own data directory, so queues do not collide; accepted.
- **The 50 MB cap drops events silently from the engineer's point of view.** The server sees `dropped_events` and the operator view shows the gap; accepted.
