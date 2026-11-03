---
id: field-allowlist
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-13
version: 6
affects:
  - internal/allowlist/*.go
  - contract/types.go
  - contract/enrollment_fields.go
  - contract/enrollment.schema.json
  - cmd/privacy-statement/*.go
  - PRIVACY.md
  - internal/agent/*.go
  - internal/queue/*.go
  - contract/contract.go
  - internal/adapter/claudecode/hooks.go
---

# Field allowlist

Governs the single list of fields the agent is permitted to emit, how it is enforced, and how the published privacy statement is derived from it.

## Requirements

- `one-allowlist`(tested): The agent holds exactly one allowlist, keyed by event kind, listing every field that event kind may carry.
- `allowlist-matches-contract`(tested): The allowlist names exactly the fields the wire contract schema permits for each event kind, and a test fails when they differ.
- `enforced-below-adapters`(tested): The allowlist is applied by the shared agent after an adapter produces an event and before the event is queued, so no adapter can add a field.
- `unknown-field-dropped-and-counted`(tested): A field not on the allowlist is removed before queuing and the removal is counted in the agent's local diagnostics.
- `values-validated-before-queue`(tested): After the allowlist is applied and before queuing, the agent validates each event's values against the wire contract schema; an event that fails is not queued, is counted by event kind in local diagnostics, and is logged by event kind and failing keyword and field name only, never by value. An empty `device_id` on an event emitted before enrollment does not by itself fail validation.
- `cwd-excluded`(tested): The working-directory path is not on the allowlist for any event kind.
- `title-excluded`(tested): A harness-generated session title or slug is not on the allowlist for any event kind.
- `content-excluded`(tested): Prompt text, completion text, tool inputs, tool outputs, and file paths are not on the allowlist for any event kind.
- `request-id-included`(tested): The vendor's API request id is on the allowlist for `session.turn`.
- `side-usage-included`(tested): Per-model token counts of the API calls that produced no turn are on the allowlist for `session.closed` as `side_usage`, carrying the model name and counts only.
- `git-user-email-included`(tested): The git `user.email` in effect for the session's repository (its own config, else the engineer's global git config) is on the allowlist for `session.opened` only.
- `content-bearing-payloads-reduced`(tested): A hook payload that carries model output is reduced to its usage counts inside the agent before any of it is written to disk.
- `privacy-statement-generated`(tested): The published privacy statement's field table is generated from the allowlist in code, and CI fails when the committed statement differs from the generated one.
- `statement-per-event-kind`(tested): The generated statement lists, per event kind, every field and a one-line description of what it contains.
- `statement-names-identity-line`(tested): The statement names the two values meant to identify the engineer: the email the engineer confirms at enrollment and the git email in effect for the repository as a join signal.
- `statement-names-incidental-identity`(tested): The statement says plainly that `branch` is free text the engineer chooses and `repo.owner` can be a personal account name, and that the device hostname sent at enrollment often contains the engineer's name, so each can identify a person although none is meant to.
- `statement-covers-enrollment`(tested): The generated statement has an "At enrollment" section, generated from the enrollment request in the wire contract, that lists every field the request carries with a one-line description and says it is sent once per enrollment or re-enrollment.
- `change-updates-this-spec`(built): Adding or removing a field from the allowlist updates the requirements of this spec in the same change.
  - status-reason: a process rule stated at the top of internal/allowlist/allowlist.go and enforced in review; no test can see the spec diff

## Design Notes

Customers' security teams ask exactly what leaves an engineer's machine, and the answer has to be the same for every harness. One allowlist held by the shared agent, applied below every adapter, is what makes that answer true by construction rather than by review. The working-directory path is excluded because it leaks usernames, directory names, and client names, and the normalized repository identity already carries everything the join needs. A model-written session title is excluded because it is conversation content, not metadata. The vendor's request id is included because it is not personal and is the only key that could reconcile a turn to a vendor's usage export, or to the harness's own telemetry, exactly.

The allowlist lives in the shared agent rather than in the schema validator because the schema says what is valid, not what is permitted: a schema with `additionalProperties` false would reject an over-full event at the server, after it had already left the laptop. Enforcement has to happen before the queue. The allowlist checks names, not values, so an adapter bug could still put a path or prompt text into a permitted string field; validating each allowlisted event against the schema before queuing closes that for every field the schema constrains by type, enum, format, or pattern. Free-text fields the schema leaves unconstrained (branch, model, and the like) are covered only by the allowlist and adapter tests.

This allowlist governs only what leaves the agent. A harness's own telemetry reaches Neo.Tax without passing through the agent, so the OTLP endpoint holds a separate allowlist on the server (`otlp-ingest`).

Generating the statement from code is what lets the answer to a security questionnaire be "this table, and the test that proves it" rather than a document someone remembers to update.

## Risks

- **A field description in the generated statement is too terse to satisfy a security reviewer.** Mitigated by the descriptions living beside the allowlist entries and being reviewed as part of the same change.
- **Refused events are invisible to Neo.Tax.** An event refused under `values-validated-before-queue` is dropped whole, and the count of refusals stays in the agent's local queue state. It is never sent, because reporting it needs the engineer's consent, so a bug that rejects every event of one kind shows up on the server only as missing usage. Accepted; the local count is there for debugging on the machine.
