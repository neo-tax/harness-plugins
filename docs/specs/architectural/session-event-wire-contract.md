---
id: session-event-wire-contract
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-25
version: 2
affects:
  - contract/*.schema.json
  - contract/*.go
  - internal/fakeingest/*.go
  - cmd/fake-ingest/*.go
---

# Session event wire contract

Governs every request and response exchanged between the agent and Neo.Tax: the session event envelope, the four event kinds, the batched ingest request and its response, and the enrollment exchange. The contract is owned by this repository as JSON Schema.

## Requirements

- `schema-is-source-of-truth`(tested): Every request and response body is defined by a JSON Schema document in this repository's contract directory, and no Go type or TypeScript type is authoritative over it.
- `four-schema-documents`(tested): The contract consists of four schema documents: session event, ingest request, ingest response, and enrollment.
- `contract-version-on-every-request`(tested): Every request carries a `contract_version` string, and the server rejects a version it does not accept with a response that names the versions it does.
- `breaking-change-is-new-version`(built): A change that would make a previously valid request invalid, or change the meaning of an existing field, is published under a new `contract_version`; additive optional fields are not.
  - status-reason: contract.Version in contract/contract.go names the published version; whether a schema change is breaking is decided in review, not by a test
- `previous-version-accepted`(approved): The server accepts the previous `contract_version` until every published plugin release has moved to the current one.
- `event-kinds`(tested): A session event has exactly one of the kinds `session.opened`, `session.turn`, `session.pr_opened`, or `session.closed`.
- `envelope-fields`(tested): Every session event carries `contract_version`, `device_id`, `harness`, `harness_version`, `agent_version`, `session_id`, `event_kind`, and `sent_at`, and no other envelope field.
- `opened-fields`(tested): A `session.opened` event carries `started_at`, `entrypoint`, `source`, `repo`, `branch`, `head_sha`, `git_user_email`, and `dropped_events`.
- `turn-fields`(tested): A `session.turn` event carries `message_id`, `request_id`, `subagent_id`, `timestamp`, `model`, `branch`, `head_sha`, and `usage`.
- `usage-shape`(tested): `usage` carries `input`, `output`, `cache_write_5m`, `cache_write_1h`, `cache_read`, `service_tier`, and `source`, where `source` is one of `payload`, `transcript`, or `unknown`, and every count is null when `source` is `unknown`.
- `pr-opened-fields`(tested): A `session.pr_opened` event carries `repo`, `number`, and `timestamp`.
- `closed-fields`(tested): A `session.closed` event carries `ended_at`, `reason`, and `dropped_events`.
- `repo-shape`(tested): `repo` is either null or an object of `host`, `owner`, and `name`, each lowercase.
- `subagent-turns-belong-to-parent`(approved): A turn produced by a subagent carries the parent session's `session_id` and a non-null `subagent_id`; a main-thread turn carries a null `subagent_id`.
- `idempotency-key`(tested): The server treats two events with the same `device_id`, `session_id`, `event_kind`, and `message_id` as the same event; for kinds without a `message_id`, the key uses the event kind alone within the session.
- `batched-ingest`(tested): An ingest request carries an ordered array of between 1 and 500 session events.
- `ingest-response-per-event`(tested): An ingest response reports, for the batch as a whole, how many events were accepted, how many were duplicates, and the index and reason of each event rejected for schema reasons.
- `schema-rejection-is-partial`(tested): A schema-invalid event in a batch does not cause the server to reject the valid events in the same batch.
- `enrollment-request`(tested): An enrollment request carries the enrollment token, the confirmed email, `email_confirmed_by`, a device description (hostname, OS, architecture, harness, harness version, agent version), and `contract_version`.
- `enrollment-response`(tested): A successful enrollment response carries `device_id`, the device key, and the ingest base URL the device is to use.
- `email-confirmed-by-values`(tested): `email_confirmed_by` is one of `engineer` or `auto`.
- `auth-errors-are-distinct`(tested): The server distinguishes an invalid or revoked device key (401), a device key that is valid but not permitted to ingest (403), and a schema failure (422) by status code, because the agent reacts to each differently.
- `platform-vendors-pinned-copy`(approved): Neo.Tax's server validates incoming requests against a vendored copy of these schemas, and a CI check in that repository fails when the copy differs from the tagged release it pins.
- `fake-ingest-validates-same-schemas`(tested): The fake ingest server validates requests against the schema documents in this repository, not against a separate copy.

## Design Notes

The ingest endpoint lives in Neo.Tax's platform repository, so if the server defined the contract the plugin would be blocked on it. Owning the contract here as JSON Schema, with the server vendoring a pinned copy, lets the agent be built and dogfooded against the fake ingest server before the real endpoint exists, and keeps the schema as the single source of truth for both sides rather than letting it leak out of either side's types. The fake server is a shipped artifact, not a test helper: it is how a customer's security team can see exactly what the plugin sends without a Neo.Tax account.

The contract carries one event per assistant message rather than a cumulative per-session snapshot re-sent on every hook. The transcript already dedupes by message id, a session that spans two branches attributes each turn to the right one, invoice reconciliation needs the per-message cache and tier detail, and the calibration study needs per-turn granularity that a snapshot would average away. Volume is a few hundred thousand rows per engineer-year; compact storage is the server's concern (the expected shape is raw events in cold storage with per-session and per-day aggregates in Postgres), and the contract does not change if the server tiers its storage.

`session.pr_opened` is included from the first version because Claude Code records pull requests opened from a session; it is the strongest session-to-PR signal available and costs nothing to carry. A harness that cannot give per-message usage still emits `session.turn` events with `usage.source` as `unknown`, so the shape is uniform across adapters.

`dropped_events` is on both `session.opened` and `session.closed` so that a count left by the queue cap rides on whichever of the two the agent emits next; a session killed before `SessionEnd` still reports it through the next session's `session.opened`.

`email_confirmed_by` is in the enrollment request from the first version even though v0.1 only ever sends `engineer`, so managed enrollment in v1.0 adds no server-side change.

Harness-specific identifiers (`session_id`, `message_id`, `request_id`, `subagent_id`) are passed through as the harness produced them; the contract does not normalize them beyond requiring strings.

## Risks

- **A field is added to the allowlist without a schema change.** The agent would emit events the server rejects as schema-invalid. Mitigated by the contract test that validates every emitted event against the schema in this repository's CI.
- **Neo.Tax's vendored copy drifts.** Requests the agent considers valid are rejected in production. Mitigated by the pinned-copy CI diff; revisit if the two repositories release at very different cadences.

## Open Questions

- Whether `session.pr_opened` should join `number` into its idempotency key: as written, a session that opens two pull requests has its second `session.pr_opened` discarded as a duplicate.
- Whether the ingest response should also return the server's current `contract_version` so an old agent can log that it is behind.
