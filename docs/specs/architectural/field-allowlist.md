---
id: field-allowlist
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-24
version: 1
affects: []
---

# Field allowlist

Governs the single list of fields the agent is permitted to emit, how it is enforced, and how the published privacy statement is derived from it.

## Requirements

- `one-allowlist`(approved): The agent holds exactly one allowlist, keyed by event kind, listing every field that event kind may carry.
- `allowlist-matches-contract`(approved): The allowlist names exactly the fields the wire contract schema permits for each event kind, and a test fails when they differ.
- `enforced-below-adapters`(approved): The allowlist is applied by the shared agent after an adapter produces an event and before the event is queued, so no adapter can add a field.
- `unknown-field-dropped-and-counted`(approved): A field not on the allowlist is removed before queuing and the removal is counted in the agent's local diagnostics.
- `cwd-excluded`(approved): The working-directory path is not on the allowlist for any event kind.
- `title-excluded`(approved): A harness-generated session title or slug is not on the allowlist for any event kind.
- `content-excluded`(approved): Prompt text, completion text, tool inputs, tool outputs, and file paths are not on the allowlist for any event kind.
- `request-id-included`(approved): The vendor's API request id is on the allowlist for `session.turn`.
- `git-user-email-included`(approved): The repository-level git `user.email` is on the allowlist for `session.opened` only.
- `content-bearing-payloads-reduced`(approved): A hook payload that carries model output is reduced to its usage counts inside the agent before any of it is written to disk.
- `privacy-statement-generated`(approved): The published privacy statement's field table is generated from the allowlist in code, and CI fails when the committed statement differs from the generated one.
- `statement-per-event-kind`(approved): The generated statement lists, per event kind, every field and a one-line description of what it contains.
- `statement-names-identity-line`(approved): The statement states that the only personal identity sent is the email the engineer confirms at enrollment, plus the repository-level git email as a join signal.
- `change-updates-this-spec`(approved): Adding or removing a field from the allowlist updates the requirements of this spec in the same change.

## Design Notes

Customers' security teams ask exactly what leaves an engineer's machine, and the answer has to be the same for every harness. One allowlist held by the shared agent, applied below every adapter, is what makes that answer true by construction rather than by review. The working-directory path is excluded because it leaks usernames, directory names, and client names, and the normalized repository identity already carries everything the join needs. A model-written session title is excluded because it is conversation content, not metadata. The vendor's request id is included because it is not personal and is the only key that could reconcile a turn to a vendor's usage export exactly.

The allowlist lives in the shared agent rather than in the schema validator because the schema says what is valid, not what is permitted: a schema with `additionalProperties` false would reject an over-full event at the server, after it had already left the laptop. Enforcement has to happen before the queue.

Generating the statement from code is what lets the answer to a security questionnaire be "this table, and the test that proves it" rather than a document someone remembers to update.

## Risks

- **A field description in the generated statement is too terse to satisfy a security reviewer.** Mitigated by the descriptions living beside the allowlist entries and being reviewed as part of the same change.
