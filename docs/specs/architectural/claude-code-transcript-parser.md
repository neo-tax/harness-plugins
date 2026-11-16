---
id: claude-code-transcript-parser
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-12
version: 2
affects:
  - internal/adapter/claudecode/*.go
  - internal/cursor/*.go
  - plugins/claude-code/**
  - e2e/*.go
---

# Claude Code transcript parser

Governs how the Claude Code adapter turns hook payloads and on-disk transcripts into session events. The hook path gives the adapter the session id and transcript path; everything about a turn comes from the transcript.

## Requirements

- `hook-set`(tested): The adapter registers exactly the `SessionStart`, `Stop`, `PreCompact`, and `SessionEnd` hooks.
- `hooks-are-async`(tested): Every registered hook runs asynchronously so no hook adds latency to the engineer's turn.
- `hook-exec-shape`(tested): Each hook execs the agent with the hook name on the command line and the harness's JSON payload on standard input.
- `opened-on-session-start`(tested): `SessionStart` produces one `session.opened` event whose `source` is the harness's startup, resume, or fork reason and whose `entrypoint` is the harness's reported entry point.
- `turns-from-transcript-tail`(tested): On `Stop`, `PreCompact`, and `SessionEnd`, the adapter reads the session transcript from its stored cursor to the end and emits one `session.turn` per assistant message not yet emitted.
- `dedupe-by-message-id`(tested): An assistant message that spans several transcript lines produces exactly one turn, keyed by the message id, because every line of that message repeats the same usage block.
- `usage-mapping`(tested): A turn's `usage` maps the transcript's input, output, cache-read, and cache-creation counts, with cache creation split into the five-minute and one-hour entries the transcript reports, and `service_tier` as reported.
- `usage-source-transcript`(tested): A turn whose counts came from the transcript carries `usage.source` as `transcript`.
- `usage-unknown-when-absent`(tested): An assistant message with no usage block produces a turn with `usage.source` as `unknown` and null counts.
- `request-id-from-transcript`(tested): A turn's `request_id` is the API request id the transcript records for that message, or null.
- `subagent-transcripts-scanned`(tested): On every hook that reads turns, the adapter also scans each subagent transcript under the session's subagents directory from its own cursor and emits those messages as turns of the parent session with the subagent's id.
- `subagent-usage-counted`(tested): Subagent turns carry their own usage; they are never folded into a main-thread turn.
- `pr-link-lines`(tested): A transcript line recording a pull request opened from the session produces one `session.pr_opened` event with the repository parsed from the recorded repository name and the recorded number.
- `closed-on-session-end`(tested): `SessionEnd` produces one `session.closed` event with the harness's end reason.
- `branch-per-turn`(tested): A turn's `branch` is the transcript's recorded branch for that message when present, else the branch read from the working directory at hook time.
- `transcript-version-recorded`(tested): The harness version recorded in the transcript is what the envelope's `harness_version` carries.
- `content-never-read-into-events`(tested): Message content, tool inputs, tool results, titles, and the working-directory path are never copied into any event, under any condition.
- `fixture-driven`(tested): The parser's output for a transcript is specified by a checked-in fixture pairing an anonymized transcript with its expected event stream, and a new harness version is supported by adding a fixture, not a code branch.

## Design Notes

The `Stop` hook fires on the main thread after each assistant turn, which is frequent enough that subagent transcripts are picked up within one turn of being written without registering a subagent hook of its own.

`PreCompact` is registered because compaction rewrites the transcript; reading the tail before compaction guarantees no turn is lost between the last `Stop` and the rewrite.

A transcript that reports cache creation without the five-minute and one-hour split (older versions) maps the whole amount to `cache_write_5m`, the only TTL those versions wrote. A turn takes the usage, model, request id, and timestamp of its message's first line.

Claude Code cancels a synchronous `SessionEnd` hook 1.5 seconds after it starts, and a plugin hook's own `timeout` does not extend that. An async hook is not canceled: it keeps running after Claude Code exits. A probe hook that ran for 14 seconds finished on Claude Code 2.1.322 (macOS) after `claude -p`, after `/exit`, and after the terminal was closed, while the same hook registered synchronously got SIGTERM at 1.5 seconds each time. `SessionEnd` relies on this: it waits up to 30 seconds for the cursor lock, because a tail it skips is recovered only by a backfill, and an engineer runs a backfill rarely.

Resume and fork keep the harness's session id semantics: a resumed session reuses its id and produces a `session.opened` with `source` resume; a fork is a new session id with `source` fork. The server's idempotency key makes re-reading a resumed transcript harmless.

## Risks

- **The harness changes its transcript format.** Turns stop being parsed and sessions report `usage.source` unknown. Mitigated by fixtures per harness version and by the usage-unknown rate being a v0.1 exit criterion; the agent logs a parse failure with the harness version and never crashes the hook.
- **A harness release starts canceling async `SessionEnd` hooks.** A hook canceled before its events are queued loses `session.closed` and the session's final tail. Detected by the server's rate of sessions without `session.closed` per harness version; the install smoke test can assert `session.closed` arrives after the harness exits.
- **A session with no `SessionEnd`.** The harness was killed and no `session.closed` arrives. Accepted; the server treats a session as closed after inactivity.

## Open Questions

- Whether a turn should also carry the transcript's reported model speed setting, which affects price.
