---
id: session-event-wire-contract
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-19
version: 14
affects:
  - contract/*.schema.json
  - contract/*.go
  - internal/fakeingest/*.go
  - cmd/fake-ingest/*.go
  - internal/adapter/claudecode/transcript.go
  - internal/adapter/claudecode/backfill.go
  - internal/adapter/claudecode/sideusage.go
---

# Session event wire contract

Governs every request and response exchanged between the agent and Neo.Tax: the session event envelope, the four event kinds, the batched ingest request and its response, and the enrollment exchange. The contract is owned by this repository as JSON Schema.

The plugin's ingest endpoint is one of Neo.Tax's two entry points into the session record. The other, governed by `otlp-ingest`, takes a harness's own OpenTelemetry export and does not use this contract; the two meet on the turn's call ids, response id first (`otlp-ingest#call-ids`).

## Requirements

- `schema-is-source-of-truth`(tested): Every request and response body is defined by a JSON Schema document in this repository's contract directory, and no Go type or TypeScript type is authoritative over it.
- `four-schema-documents`(tested): The contract consists of four schema documents: session event, ingest request, ingest response, and enrollment.
- `contract-version-on-every-request`(tested): Every request carries a `contract_version` string, and the server rejects a version it does not accept with a response that names the versions it does.
- `breaking-change-is-new-version`(built): A change that would make a previously valid request invalid, or change the meaning of an existing field, is published under a new `contract_version`; additive optional fields are not.
  - status-reason: contract.Version in contract/contract.go names the published version; whether a schema change is breaking is decided in review, not by a test
- `previous-version-accepted`(built): The server accepts the previous `contract_version` until every published plugin release has moved to the current one.
  - status-reason: unmerged: built in platform-new #5601, which is still open, in projects/server/src/harness-ingest/harness-contract.ts (ACCEPTED_CONTRACT_VERSIONS); only version 1 exists so far
- `event-kinds`(tested): A session event has exactly one of the kinds `session.opened`, `session.turn`, `session.pr_linked`, or `session.closed`.
- `envelope-fields`(tested): Every session event carries `contract_version`, `device_id`, `harness`, `harness_version`, `agent_version`, `session_id`, `event_kind`, and `sent_at`, and no other envelope field.
- `opened-fields`(built): A `session.opened` event carries `started_at`, `entrypoint`, `source`, `repo`, `branch`, `head_sha`, and `dropped_events`.
  - status-reason: the schema's opened definition has these properties; TestTypedFieldsMatchSchemaProperties proves only that the Go type and the schema agree, and no test compares the schema to this list
- `turn-fields`(built): A `session.turn` event carries `message_id`, `request_id`, `subagent_id`, `timestamp`, `model`, `branch`, `head_sha`, and `usage`.
  - status-reason: the schema's turn definition has these properties; TestTypedFieldsMatchSchemaProperties proves only that the Go type and the schema agree, and TestEventRejections that a turn field is refused on a close and `usage` is required; no test compares the schema to this list
- `usage-shape`(tested): `usage` carries `input`, `output`, `cache_write_5m`, `cache_write_1h`, `cache_read`, `service_tier`, `speed`, and `source`, where `speed` is the speed the harness reports the call ran at (Claude Code: `standard` or `fast`) or null when it reports none, and `source` is one of `payload`, `transcript`, or `unknown`, and every count is null when `source` is `unknown`.
- `pr-linked-fields`(built): A `session.pr_linked` event carries `repo`, `number`, and `first_seen_at`, the time the session's transcript first records the link.
  - status-reason: the schema's pr_linked definition has these properties; TestTypedFieldsMatchSchemaProperties proves only that the Go type and the schema agree, and no test compares the schema to this list
- `pr-link-per-session`(tested): A `session.pr_linked` event links its session to a pull request; it does not say the session opened it. The server keeps each session's links as a set: two `session.pr_linked` events with the same `device_id`, `session_id`, `repo`, and `number` are the same link and a repeat adds nothing, and the same pull request linked from two sessions is linked to both. The agent sends the earliest `first_seen_at` it saw, so the first one stored is the earliest unless a cursor reset re-reads a transcript whose early link lines are gone.
- `closed-fields`(built): A `session.closed` event carries `ended_at`, `reason`, `dropped_events`, and `side_usage`.
  - status-reason: the schema's closed definition has these properties; TestTypedFieldsMatchSchemaProperties proves only that the Go type and the schema agree, and TestEventRejections that `side_usage` is required; no test compares the schema to this list
- `side-usage-shape`(tested): `side_usage` is null when the harness recorded no session totals, or when they cannot be matched to the session's turns, else an array with at most one entry per model, each carrying `model` and the non-negative integer counts `input`, `output`, `cache_write_5m`, `cache_write_1h`, and `cache_read`.
- `side-usage-cumulative`(built): `side_usage` covers the whole session up to that close, so the server takes it from the session's `session.closed` with the latest `ended_at` whose `side_usage` is not null, and never adds two closes' `side_usage` together.
  - status-reason: unmerged: built in platform-new #5742, which is still open, in libs/ai-tokens/src/domain/src/session-record/session-record.ts (`sideUsageOf`, which keeps each device's latest close with `side_usage`), and tested there in projects/server/test/test-new/harness-ingest/otlp/read-harness-session-record.spec.ts
- `repo-shape`(tested): `repo` is either null or an object of `host`, `owner`, and `name`, each lowercase.
- `subagent-turns-belong-to-parent`(tested): A turn produced by a subagent carries the parent session's `session_id` and a non-null `subagent_id`; a main-thread turn carries a null `subagent_id`.
- `idempotency-key`(built): The server treats two `session.turn` events with the same `device_id` and `message_id` as the same event whatever their `session_id`, so a forked session that re-sends its parent's turns under its own id adds nothing, and which session keeps them is `shared-event-owner`; a `session.closed` is the same event as another with the same `device_id`, `session_id`, and `ended_at`, so a resumed session's later close is kept; a `session.opened` is the same as another with the same `device_id` and `session_id`.
  - status-reason: unmerged: built in platform-new #5601, which is still open, in libs/ai-tokens/src/domain/src/harness-session-event/session-event-idempotency.ts, and tested there in projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts (a fork's copies of its parent's turns count as duplicates)
- `opened-before-turns`(built): An agent sends a session's `session.opened` before any of that session's other events, or not at all.
  - status-reason: a backfill emits each session's `session.opened` ahead of its turns, tested by TestBackfillEmitsEveryTranscriptNewestFirst; on the hook path it holds because `SessionStart` queues the `session.opened` before the first `Stop` queues a turn and the queue is flushed oldest first by one flusher, both hooks are async and no test pins their order, but each queues under the cursor lock, and the first `Stop` comes a prompt and a model reply after `SessionStart`, so a turn is queued first only if `SessionStart` stalls for longer than that reply; even then the owner is wrong only if a fork's copy of that turn arrives between the turn and its `session.opened`
- `shared-event-owner`(built): When a `session.turn` arrives that another session already reported, the server keeps whichever of the stored copy and the new one belongs to the better-ranked session and drops the other: a session whose `session.opened` has a `source` other than fork, then the earliest `started_at`, then the lowest `session_id`. A session whose `session.opened` has not arrived ranks after every session whose has. Because of `opened-before-turns`, the owner does not depend on the order the copies arrive in.
  - status-reason: unmerged: built in platform-new #5601, which is still open, in libs/ai-tokens/src/domain/src/harness-session-event/session-event-owner.ts (`fileSharedTurns`), and tested there in libs/ai-tokens/src/domain/test/harness-session-event/file-shared-turns.spec.ts and projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts; the fake ingest server does the same in internal/fakeingest/owners.go, tested in internal/fakeingest/server_test.go
- `batched-ingest`(tested): An ingest request carries an ordered array of between 1 and 500 session events.
- `ingest-response-per-event`(tested): An ingest response reports, for the batch as a whole, how many events were accepted, how many were duplicates, and the index and reason of each event rejected for schema reasons.
- `schema-rejection-is-partial`(built): A schema-invalid event in a batch does not cause the server to reject the valid events in the same batch.
  - status-reason: unmerged: built in platform-new #5601, which is still open, and tested there in projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts
- `enrollment-request`(tested): An enrollment request carries the enrollment token, the confirmed email, `email_confirmed_by`, a device description (hostname, OS, architecture, harness, harness version, agent version), and `contract_version`.
- `enrollment-response`(built): A successful enrollment response carries `device_id`, the device key, and the ingest base URL the device is to use.
  - status-reason: the enrollment schema's response requires exactly these; TestEnrollmentAndIngestShapes proves only that a Go response with them validates, and no test compares the schema to this list
- `email-confirmed-by-values`(tested): `email_confirmed_by` is one of `engineer` or `auto`.
- `auth-errors-are-distinct`(built): The server distinguishes an invalid or revoked device key (401), a device key that is valid but not permitted to ingest (403), and a schema failure (422) by status code, because the agent reacts to each differently.
  - status-reason: unmerged: built in platform-new #5601, which is still open, in projects/server/src/harness-ingest/harness-ingest.controller.ts; 401 and 422 are tested, 403 is reserved because a device has no state between live and revoked
- `platform-vendors-pinned-copy`(approved): Neo.Tax's server validates incoming requests against a vendored copy of these schemas, and a CI check in that repository fails when the copy differs from the tagged release it pins.
  - status-reason: platform-new #5601, which is still open, vendors the schemas in projects/server/src/harness-ingest/contract/v1/ and validates every request against them in harness-contract.ts, recording the pinned commit in contract/README.md; no CI check there compares the copy with the pin yet, and no release of this repository has been tagged to pin
- `fake-ingest-validates-same-schemas`(tested): The fake ingest server validates requests against the schema documents in this repository, not against a separate copy.

## Design Notes

The ingest endpoint lives in Neo.Tax's platform repository, so if the server defined the contract the plugin would be blocked on it. Owning the contract here as JSON Schema, with the server vendoring a pinned copy, lets the agent be built and dogfooded against the fake ingest server before the real endpoint exists, and keeps the schema as the single source of truth for both sides rather than letting it leak out of either side's types. The fake server is a shipped artifact, not a test helper: it is how a customer's security team can see exactly what the plugin sends without a Neo.Tax account.

The contract carries one event per assistant message rather than a cumulative per-session snapshot re-sent on every hook. The transcript already dedupes by message id, a session that spans two branches attributes each turn to the right one, invoice reconciliation needs the per-message cache and tier detail, and the calibration study needs per-turn granularity that a snapshot would average away. Volume is a few hundred thousand rows per engineer-year; compact storage is the server's concern (the expected shape is raw events in cold storage with per-session and per-day aggregates in Postgres), and the contract does not change if the server tiers its storage.

`session.pr_linked` is included from the first version because Claude Code records the pull requests a session touches; it is the strongest session-to-PR signal available and costs nothing to carry. It was `session.pr_opened` until 2026-11-14, but Claude Code writes the same link line many times in a session (2,348 lines for 46 pull requests across one engineer's transcripts) and for pull requests the session only looked at or pushed to, so the event names a link, not an opening, and the agent sends each link once per session. A link is its own event rather than a list on `session.closed` because many sessions never close cleanly (killed, `/clear`, resumed), and their links would be lost. A harness that cannot give per-message usage still emits `session.turn` events with `usage.source` as `unknown`, so the shape is uniform across adapters.

`dropped_events` is on both `session.opened` and `session.closed` so that a count left by the queue cap rides on whichever of the two the agent emits next; a session killed before `SessionEnd` still reports it through the next session's `session.opened`.

`side_usage` is the usage of the API calls a harness makes that never become an assistant message in its transcript, so no `session.turn` carries them: in Claude Code, the session title, the recap shown after the engineer has been away, and the summarization behind compaction. A usage audit against the API's own counts found these were 14% of a sample of real sessions' tokens, so leaving them out would understate spend. They have no message id or request id to key a turn by, and the harness only records them as session totals, so they ride on `session.closed` as one entry per model rather than as turns. The array form, not an object keyed by model, keeps model names out of field names, where the allowlist could not describe them. Because the totals are cumulative and a resumed session closes more than once, each close carries the session's whole side usage so far; the latest close supersedes the earlier ones, and a lost close costs only the side usage since the one before it. `side_usage` was added before any release published the contract, so it is part of `contract_version` 1 rather than a new version.

`email_confirmed_by` is in the enrollment request from the first version even though v0.1 only ever sends `engineer`, so managed enrollment in v1.0 adds no server-side change.

Harness-specific identifiers (`session_id`, `message_id`, `request_id`, `subagent_id`) are passed through as the harness produced them; the contract does not normalize them beyond requiring strings. The message id and request id are held to that most strictly (`claude-code-transcript-parser#message-id-is-provider-id`, `claude-code-transcript-parser#request-id-from-transcript`), because they are the model provider's own ids for the API call, which telemetry and gateways also carry: the server joins a turn from this contract with another source's record of the same call on them (`otlp-ingest#call-ids`), so a normalized or synthesized value would split one turn into two.

Turns are keyed without the session because Claude Code copies a parent session's transcript into a fork: a capture on 2026-11-11, before the adapter skipped the copies, saw a fork re-send its parent's two turns with the same message and request ids under the fork's session id. Keyed by session, every copy the adapter misses would count its parent's usage again. A message id is the model provider's id for one response, so it names one turn on a device whichever session reports it. Pull request links keep the session, because a link is a fact about one session: a fork that links a pull request its parent also linked is linked to it too, and nothing is counted twice, since a link carries no usage.

The first session to report a shared turn used to keep it, which made ownership depend on arrival order. A backfill sends the newest transcript first, so a fork, being newer, usually reached the server before its parent and took the parent's turns. A test on Claude Code 2.1.325 found no field marking a fork's copied lines: they keep the parent's message id, request id, uuid, and timestamp, and only `sessionId` is rewritten. Converting a session to a background session copies the transcript the same way. The adapter now skips the copies: on the hook path by the fork's start time (`claude-code-transcript-parser#fork-skips-copied-lines`), and in a backfill by the line uuids a fork shares with its parent's transcript (`event-queue-and-backfill#backfill-skips-fork-copies`). Either can miss a fork: a fork's `SessionStart` that cannot take the cursor lock records no start, and a backfill cannot find the parent of a fork whose parent's transcript is gone. So the server still picks the owner by a rule that does not depend on order.

The server compares only the stored copy with the arriving one, and keeps no other copy. Choosing among every copy ever reported would need each one stored, and the owner could then move whenever a late `session.opened` arrived, rewriting a turn long after it was filed. Comparing two copies loses nothing that keeping them all would gain, given `opened-before-turns`: a copy the server dropped lost to a session whose rank it already knew, and a session that never sends its `session.opened` (the plugin was enabled mid-session, its `SessionStart` failed, or the queue cap deleted the event) ranks last either way. A `session.opened` that does arrive late, from a backfill with `--restart`, is followed by that session's turns again, and each is compared afresh. A stored turn can still move, so anything summed from turns, such as usage per pull request, is recomputed from the stored events rather than added to as they arrive.

## Risks

- **A field is added to the allowlist without a schema change.** The agent would emit events the server rejects as schema-invalid. Mitigated by the contract test that validates every emitted event against the schema in this repository's CI.
- **A fork the adapter does not recognize can own its parent's turns.** Such a fork re-sends its parent's turns, and a backfilled one is opened with `source` null, so it ranks with its parent on `source` and the owner rule falls to `started_at` and `session_id`, which may pick the fork. Totals are unaffected; only which of the two sessions a shared turn is filed under. Accepted.
- **Neo.Tax's vendored copy drifts.** Requests the agent considers valid are rejected in production. Mitigated by the pinned-copy CI diff; revisit if the two repositories release at very different cadences.

## Open Questions

- Whether the ingest response should also return the server's current `contract_version` so an old agent can log that it is behind.
