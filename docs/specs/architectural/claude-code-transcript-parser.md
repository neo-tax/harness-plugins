---
id: claude-code-transcript-parser
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-14
version: 7
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
- `message-id-is-provider-id`(tested): A turn's `message_id` is the provider's own id for the response as the transcript records it (for Anthropic, `msg_…`), unchanged, because Neo.Tax joins another source's record of the same API call to the turn on it.
- `subagent-transcripts-scanned`(tested): On every hook that reads turns, the adapter also scans each subagent transcript under the session's subagents directory from its own cursor and emits those messages as turns of the parent session with the subagent's id.
- `subagent-usage-counted`(tested): Subagent turns carry their own usage; they are never folded into a main-thread turn.
- `pr-link-lines`(tested): A transcript line recording a pull request opened from the session produces one `session.pr_opened` event with the repository parsed from the recorded repository name and the recorded number.
- `closed-on-session-end`(tested): `SessionEnd` produces one `session.closed` event with the harness's end reason.
- `session-totals-from-cost-state`(tested): The session's totals are read from the main transcript's `cost-state` lines: per model, the latest line of each process's tally (lines sharing a `startTime`), summed across tallies.
- `side-usage-is-totals-minus-turns`(tested): `side_usage` is, per model and per count, the session's totals less the usage of every turn of the session (main thread and subagents, each message counted once), clamped at zero, with models whose counts are all zero left out.
- `side-usage-model-join`(tested): Totals and turns are matched by model name with any trailing bracketed suffix removed (Claude Code may write `claude-opus-5[1m]` in its totals where the turn records `claude-opus-5`), and `side_usage` names the model without it. When a turn model with usage still has no totals while some totals model has no turns, the two name a model differently in an unknown way, and `side_usage` is null rather than reporting those turns as side calls.
- `side-usage-null-without-totals`(tested): `side_usage` is null when, after a bounded wait at a `SessionEnd` whose reason is an exit (`prompt_input_exit` or `other`), or at once for any other reason, the main transcript's latest `cost-state` line is followed by a user or assistant line, since such totals are an older process's (a resumed session's earlier exit) and would leave out what happened since. Other line types after it, which Claude Code writes as bookkeeping after exiting and adds to between versions, do not make the totals stale.
- `side-usage-cache-split`(tested): Session totals split cache creation into five-minute and one-hour counts as the `cost-state` line reports them. Where a `cost-state` line does not split it (Claude Code through 2.1.310), the turns' cache writes of both tiers are subtracted from the unsplit amount, after the split amounts of the same tier, and what remains is reported as `cache_write_5m`, as an unsplit turn's writes are.
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

Claude Code makes API calls that never become an assistant message in the transcript: the session title, the recap after the engineer has been away, and compaction's summary. Their usage appears nowhere in the transcript except in the session totals Claude Code writes as a `cost-state` line on exit, before `SessionEnd` fires. A usage audit through a recording proxy found those totals equal to the API's own counts for every request in the session, side calls and subagents included. Across 254 real sessions, the totals carried across `--resume` and only ever grew, except when a second process opened the same session: that process keeps a separate tally under its own `startTime`, which is why tallies are summed rather than the last line taken. One older version (2.1.284) recorded less than its subagent turns alone; clamping each count at zero means that case under-reports instead of reporting negative usage, and it was under 2% of the sample's tokens. In 253 of 256 sessions nothing followed the final `cost-state` line, and in the other three only an `artifact-comment-monitor` line did; a superseded line was always followed by the resumed process's own lines, almost always starting with `last-prompt`. A new kind of line written after exit would make `side_usage` null, which the server's null rate per harness version would show.

`SessionEnd` waits up to 2 seconds for a current `cost-state` line, and only on the reasons where the process exits. `/clear`, logout, and `/resume` end a session while the process keeps running and write no `cost-state` for it, so they read the transcript once and move on.

## Risks

- **The harness changes its transcript format.** Turns stop being parsed and sessions report `usage.source` unknown. Mitigated by fixtures per harness version and by the usage-unknown rate being a v0.1 exit criterion; the agent logs a parse failure with the harness version and never crashes the hook.
- **A harness release starts canceling async `SessionEnd` hooks.** A hook canceled before its events are queued loses `session.closed` and the session's final tail. Detected by the server's rate of sessions without `session.closed` per harness version; the install smoke test can assert `session.closed` arrives after the harness exits.
- **A session with no `SessionEnd`.** The harness was killed and no `session.closed` arrives. Accepted; the server treats a session as closed after inactivity. Its side usage is lost as well, since Claude Code writes `cost-state` only on a clean exit.
- **Transcript saving is off.** Claude Code then writes no transcript, so the adapter emits the session's opens and closes but no turns; a session launched from inside another Claude Code session inherits a marker that turns saving off. Detectable as sessions with no turns; the OTLP path (`otlp-ingest`) still reports their calls.
- **`cost-state` changes or disappears.** `side_usage` turns null and side calls go uncounted again. Detectable: the server can watch the null rate per harness version, and `scripts/dev.sh audit` re-measures against the API.

## Open Questions

- Whether a turn should also carry the transcript's reported model speed setting, which affects price.
