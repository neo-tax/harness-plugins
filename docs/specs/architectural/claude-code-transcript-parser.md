---
id: claude-code-transcript-parser
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-15
version: 11
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
- `fork-skips-copied-lines`(tested): At a `SessionStart` whose `source` is fork, the adapter records the hook's time against the session's transcript, and its reads of that transcript and its subagent transcripts skip assistant and pull-request lines timestamped before it, which Claude Code copied from the parent session. The record is kept until the transcript is written, for up to a day. A backfill skips a transcript that has the record, as it skips any transcript with a cursor; a backfill with `--restart` drops it and finds a fork's copies by their line uuids instead (`event-queue-and-backfill#backfill-skips-fork-copies`).
- `dedupe-by-message-id`(tested): An assistant message that spans several transcript lines produces exactly one turn, keyed by the message id, because every line of that message repeats the same usage block.
- `usage-mapping`(tested): A turn's `usage` maps the transcript's input, output, cache-read, and cache-creation counts, with cache creation split into the five-minute and one-hour entries the transcript reports, and `service_tier` and `speed` as reported, each null when the transcript has none.
- `usage-source-transcript`(tested): A turn whose counts came from the transcript carries `usage.source` as `transcript`.
- `usage-unknown-when-absent`(tested): An assistant message with no usage block produces a turn with `usage.source` as `unknown` and null counts.
- `request-id-from-transcript`(tested): A turn's `request_id` is the API request id the transcript records for that message, or null.
- `message-id-is-provider-id`(tested): A turn's `message_id` is the message id the transcript records, unchanged. For an API response that is the provider's own id (for Anthropic, `msg_…`), and Neo.Tax joins another source's record of the same API call to the turn on it.
- `subagent-transcripts-scanned`(tested): On every hook that reads turns, the adapter also scans each subagent transcript under the session's subagents directory from its own cursor and emits those messages as turns of the parent session with the subagent's id.
- `subagent-usage-counted`(tested): Subagent turns carry their own usage; they are never folded into a main-thread turn.
- `pr-link-lines`(tested): A transcript line recording a pull request link produces a `session.pr_linked` event carrying the recorded number, the line's time as `first_seen_at`, and the repository parsed from the recorded `owner/name` with the host of the recorded URL (`github.com` when it has none), or null when the name does not parse.
- `pr-link-once-per-session`(tested): The adapter sends one `session.pr_linked` per pull request per session, across hook runs and across the session's main and subagent transcripts, with the earliest time a copy of the link line was seen in that read. It records the links it has sent on the main transcript's cursor as short hashes, so the cursor file does not name repositories, and keeps them when a rewritten transcript is read again.
- `closed-on-session-end`(tested): `SessionEnd` produces one `session.closed` event with the harness's end reason.
- `session-totals-from-cost-state`(tested): The session's totals are read from the main transcript's `cost-state` lines: per model, the latest line of each process's tally (lines sharing a `startTime`), summed across tallies.
- `side-usage-is-totals-minus-turns`(tested): `side_usage` is, per model and per count, the session's totals less the usage of every turn of the session (main thread and subagents, each message counted once), clamped at zero, with models whose counts are all zero left out.
- `side-usage-model-join`(tested): Totals and turns are matched by model name with any trailing bracketed suffix removed (Claude Code may write `claude-opus-5[1m]` in its totals where the turn records `claude-opus-5`), and `side_usage` names the model without it. When a turn model with usage still has no totals while some totals model has no turns, the two name a model differently in an unknown way, and `side_usage` is null rather than reporting those turns as side calls.
- `side-usage-null-without-totals`(tested): `side_usage` is null when, after a bounded wait at a `SessionEnd` whose reason is an exit (`prompt_input_exit` or `other`), or at once for any other reason, the main transcript's latest `cost-state` line is followed by a user or assistant line, since such totals are an older process's (a resumed session's earlier exit) and would leave out what happened since. Other line types after it, which Claude Code writes as bookkeeping after exiting and adds to between versions, do not make the totals stale.
- `side-usage-cache-split`(tested): Session totals split cache creation into five-minute and one-hour counts as the `cost-state` line reports them. Where a `cost-state` line does not split it (Claude Code through 2.1.310), the turns' cache writes of both tiers are subtracted from the unsplit amount, after the split amounts of the same tier, and what remains is reported as `cache_write_5m`, as an unsplit turn's writes are.
- `branch-per-turn`(tested): A turn's `branch` is the transcript's recorded branch for that message when present, else the branch read from the working directory at hook time.
- `transcript-version-recorded`(tested): The harness version recorded in the transcript is what the envelope's `harness_version` carries.
- `content-never-read-into-events`(tested): Message content, tool inputs, tool results, titles, and the working-directory path are never copied into any event, under any condition.
- `fixture-driven`(tested): The parser's output for a transcript is specified by checked-in fixtures, each pairing a transcript (anonymized from a real session, or synthetic) with the exact event stream the parser must produce from it, and the parser's output for every fixture is checked against that stream.
- `no-version-branches`(built): The parser branches on what a transcript line records, never on the harness version it names, so a new harness version is supported by adding a fixture, not a code branch.
  - status-reason: internal/adapter/claudecode has no conditional on the harness version; review keeps it that way, since no test can show a branch is absent

## Design Notes

The `Stop` hook fires on the main thread after each assistant turn, which is frequent enough that subagent transcripts are picked up within one turn of being written without registering a subagent hook of its own.

`PreCompact` is registered because compaction rewrites the transcript; it reads the tail before compaction so the turns since the last `Stop` are read from the original lines. The hook is async, so compaction may win the race; then the next read sees the rewrite, reads the transcript again from the start, and the server's idempotency key discards what it already has.

A transcript that reports cache creation without the five-minute and one-hour split (older versions) maps the whole amount to `cache_write_5m`, the only TTL those versions wrote. A turn takes the usage, model, request id, and timestamp of its message's first line.

Claude Code cancels a synchronous `SessionEnd` hook 1.5 seconds after it starts, and a plugin hook's own `timeout` does not extend that. An async hook was not canceled in our probes: a probe hook that ran for 14 seconds finished on Claude Code 2.1.322 (macOS) after `claude -p`, after `/exit`, and after the terminal was closed, while the same hook registered synchronously got SIGTERM at 1.5 seconds each time. Claude Code's hooks documentation says `-p` may kill async hooks still running at teardown and does not enforce `timeout` on async hooks, so this is observed behavior, not a guarantee; `event-queue-and-backfill` names the risk. `SessionEnd` relies on it: it waits up to 30 seconds for the cursor lock, because a tail it skips is recovered only by a backfill run with `--restart`, which an engineer runs rarely.

Resume and fork keep the harness's session id semantics: a resumed session reuses its id and produces a `session.opened` with `source` resume; a fork is a new session id with `source` fork. The server's idempotency key makes re-reading a resumed transcript harmless. A fork's transcript begins with copies of the parent's lines, unmarked and with the parent's timestamps, which the parent's own hooks have already sent; skipping lines older than the fork's `SessionStart` keeps the fork from re-sending them under its own id.

Claude Code makes API calls that never become an assistant message in the transcript: the session title, the recap after the engineer has been away, and compaction's summary. Their usage appears nowhere in the transcript except in the session totals Claude Code writes as a `cost-state` line on exit, before `SessionEnd` fires. A usage audit through a recording proxy found those totals equal to the API's own counts for every request in the session, side calls and subagents included. Across 254 real sessions, the totals carried across `--resume` and only ever grew, except when a second process opened the same session: that process keeps a separate tally under its own `startTime`, which is why tallies are summed rather than the last line taken. One older version (2.1.284) recorded less than its subagent turns alone; clamping each count at zero means that case under-reports instead of reporting negative usage, and it was under 2% of the sample's tokens. In 253 of 256 sessions nothing followed the final `cost-state` line, and in the other three only an `artifact-comment-monitor` line did; a superseded line was always followed by the resumed process's own lines, almost always starting with `last-prompt`. A new kind of line written after exit would make `side_usage` null, which the server's null rate per harness version would show.

`SessionEnd` waits up to 2 seconds for a current `cost-state` line, and only on the reasons where the process exits. `/clear`, logout, and `/resume` end a session while the process keeps running and write no `cost-state` for it, so they read the transcript once and move on.

## Risks

- **Not every assistant line is an API response.** Claude Code writes assistant lines of its own, such as API-error and interruption notices, with an empty or placeholder model, zero or no usage, and a UUID where a provider's `msg_…` id would be. The adapter emits each as a turn, so turn counts include calls that were never made, and their `message_id` joins with no other source. They carry no tokens, so usage totals and `side_usage` are unaffected. Open: the adapter should leave them out, with a fixture that has them.
- **The harness changes its transcript format.** Turns stop being parsed and sessions report `usage.source` unknown. Mitigated by fixtures per harness version and by the usage-unknown rate being a v0.1 exit criterion; the agent logs a parse failure with the harness version and never crashes the hook.
- **A harness release starts canceling async `SessionEnd` hooks.** A hook canceled before its events are queued loses `session.closed` and the session's final tail. Detected by the server's rate of sessions without `session.closed` per harness version; the install smoke test can assert `session.closed` arrives after the harness exits.
- **A session with no `SessionEnd`.** The harness was killed and no `session.closed` arrives. Accepted; the server treats a session as closed after inactivity. Its side usage is lost as well, since Claude Code writes `cost-state` only on a clean exit.
- **Transcript saving is off.** Claude Code then writes no transcript, so the adapter emits the session's opens and closes but no turns; a session launched from inside another Claude Code session inherits a marker that turns saving off. Detectable as sessions with no turns; the OTLP path (`otlp-ingest`) still reports their calls.
- **Side usage has no speed.** `cost-state` totals are per model, not per speed, so side calls made in fast mode (compaction in a fast session) are reported without it and priced at standard speed. Claude Code's telemetry splits its token and cost metrics by `speed`, which bounds the error where it is exported.
- **`cost-state` changes or disappears.** `side_usage` turns null and side calls go uncounted again. Detectable: the server can watch the null rate per harness version, and `scripts/dev.sh audit` re-measures against the API.
