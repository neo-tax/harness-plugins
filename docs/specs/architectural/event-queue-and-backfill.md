---
id: event-queue-and-backfill
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-25
version: 1
affects:
  - internal/queue/*.go
  - internal/ingest/*.go
  - internal/agent/*.go
---

# Event queue and backfill

Governs how the agent stores session events on disk between hooks, flushes them to the ingest endpoint, backs off on failure, bounds its own footprint, and backfills transcripts that predate enrollment.

## Requirements

- `queue-is-jsonl-segments`(tested): Queued events are appended one per line to segment files under a queue directory in the harness-provided plugin data directory.
- `segment-rotation`(tested): A segment is closed and a new one opened when it exceeds 1 MB.
- `no-database`(tested): The queue uses no embedded database; a corrupt segment loses at most that segment.
- `cursor-per-transcript`(approved): A cursor file records, for each transcript path including subagent transcripts, the last emitted message id and byte offset.
- `flush-on-every-hook`(approved): Every hook invocation attempts a flush after queuing its own events.
- `flush-batch-size`(tested): A flush sends oldest events first, at most 500 per request.
- `segment-advances-on-success`(tested): A segment is deleted only after every event in it has been accepted or recorded as a duplicate by the server.
- `backoff-persisted`(tested): After a failed flush the agent records a next-attempt time, starting at 30 seconds and doubling to a ceiling of 1 hour, and no hook flushes before that time.
- `backoff-resets-on-success`(tested): A successful flush resets the backoff.
- `schema-rejection-dead-letters`(tested): Events the server rejects for schema reasons are moved to a dead-letter directory with the server's reason, and the remaining events continue.
- `dead-letter-retention`(built): Dead-letter files older than 14 days are deleted at the next hook.
  - status-reason: PruneDeadLetters deletes files older than 14 days (tested); calling it on every hook lands with the hook dispatch in PLAT-673
- `auth-rejection-stops-flushing`(tested): A 401 or 403 marks the device as needing re-enrollment; events keep queuing, and no flush is attempted until enrollment succeeds.
- `server-and-network-errors-back-off`(tested): A 5xx response or a network error triggers backoff and leaves the batch in place.
- `queue-cap`(tested): The queue is capped at 50 MB; when exceeded, the oldest segment is deleted.
- `dropped-events-reported`(built): The number of events deleted by the cap is counted and carried as `dropped_events` on the next `session.opened` or `session.closed` event, then reset.
  - status-reason: the queue counts and resets the dropped total (tested); carrying it on session.opened and session.closed lands with the adapter in PLAT-673
- `flush-lock`(tested): A single lock file serializes flushes; a hook that cannot acquire it exits without flushing and leaves its events for the next hook.
- `backfill-scope`(approved): A backfill enumerates every transcript on disk for the harness and emits events for each from the beginning, newest transcript first.
- `backfill-after-login`(approved): An interactive enrollment starts a backfill immediately after the device key is stored.
- `backfill-lock`(approved): A backfill holds a lock so that two backfills cannot run at once; a second request exits.
- `backfill-command`(approved): The agent exposes a backfill command, reachable through the harness's `neo-tax:backfill` entry point, that resets every cursor and re-runs the backfill.
- `backfill-idempotent-server-side`(approved): A re-run backfill resends events the server has already accepted, and relies on the server's idempotency key to discard them.
- `no-daemon`(built): The agent runs only when a hook or a command invokes it; nothing runs between invocations.
  - status-reason: structural; the agent is a short-lived process with no background goroutine or service, which no test can observe

## Design Notes

Flushing on every hook rather than on a timer is what makes the no-daemon model work: a laptop that is offline for a week sends nothing, and the first hook after it reconnects drains the queue in batches of 500. The persisted backoff is what keeps that from turning into a retry on every turn while offline.

Backfill runs newest first because the most recent work is what an engineer and an operator want to see land; retention bounds how far back it reaches, so no cutoff is needed.

The random 0–30 minute delay before backfill belongs to managed enrollment, where a fleet enrolls at once; interactive enrollment has a human waiting and starts immediately.

## Risks

- **A long backfill under the hook's time budget.** The harness may kill a hook that runs too long. Mitigated by backfill running under its own command after login, not under a hook, and by flushes being resumable from the cursor file.
- **Two harnesses on one machine share the plugin data layout.** Each plugin has its own data directory, so queues do not collide; accepted.
- **The 50 MB cap drops events silently from the engineer's point of view.** The server sees `dropped_events` and the operator view shows the gap; accepted.
