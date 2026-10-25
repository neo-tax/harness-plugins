---
id: device-enrollment
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-24
version: 1
affects: []
---

# Device enrollment

Governs how a device obtains its device key: the enrollment token an admin mints, the interactive exchange the engineer runs, where the key is stored, and how the agent behaves when the key is rejected. Identity is captured here and nowhere else (ADR 0002).

## Requirements

- `enrollment-is-the-only-way-to-get-a-key`(approved): A device key is issued only by exchanging an enrollment token; no other path mints one.
- `token-scope`(approved): An enrollment token can do nothing except enroll devices into the one organization it was minted for.
- `token-use-count-chosen-at-mint`(approved): An admin chooses at mint time whether a token is single-use or multi-use.
- `token-default-expiry`(approved): An enrollment token expires seven days after mint unless the admin sets a different expiry.
- `token-revocable`(approved): An admin can revoke an enrollment token before it expires, and revoking it does not affect devices already enrolled with it.
- `device-key-scope`(approved): A device key authorizes session ingest only; it cannot read or write any other Neo.Tax resource.
- `device-key-revocable-individually`(approved): Revoking one device key does not affect any other device key, including others minted from the same enrollment token.
- `one-key-per-device`(approved): A device holds exactly one device key; enrolling again replaces it.
- `interactive-enrollment`(approved): The engineer enrolls by invoking the harness's `neo-tax:login` entry point, which runs the agent's login command; the agent prompts for the enrollment token and the email.
- `login-skill-delegates-to-agent`(approved): In Claude Code, `neo-tax:login` is a skill whose only job is to instruct the model to run the agent's login command; it holds no logic of its own.
- `email-candidates`(approved): At login the agent offers the engineer the global git `user.email` and, when present, the email from the harness's own account file, and also accepts a typed email.
- `email-confirmed-by-engineer`(approved): An interactive enrollment sends `email_confirmed_by` as `engineer`.
- `key-storage-location`(approved): The device key and device id are stored in the harness-provided plugin data directory, readable only by the owning user.
- `key-never-in-events`(approved): The device key never appears in a session event, a queue segment, a dead-letter file, or a log line.
- `rejected-key-stops-flushing`(approved): When ingest returns 401 or 403, the agent marks the device as needing re-enrollment, keeps queuing events, and does not attempt another flush until enrollment succeeds.
- `backfill-after-interactive-login`(approved): A successful interactive enrollment starts a backfill of existing transcripts immediately.
- `managed-enrollment-deferred`(approved): Managed delivery of the enrollment token (a file placed by device management or an environment variable, enrolling silently at the first session start with `email_confirmed_by` as `auto` and a random delay before backfill) is not part of v0.1.

## Design Notes

The shape is the Tailscale auth-key model: the circulated secret is short-lived and can only enroll, and the per-laptop secret is minted, not copied, so a leaked enrollment token exposes nothing already enrolled and a lost laptop is revoked alone.

The harness account email is offered as a candidate because it is the identity the engineer most likely recognizes, but it is only ever read during login, never on the hook path, so the privacy statement's identity line stays one sentence.

Under the later WorkOS path the same enrollment response is produced from a verified user claim instead of a pasted token; the agent's storage and rejection behavior do not change.

## Alternatives Considered

| Option | Pros | Cons |
| --- | --- | --- |
| **Enrollment token → per-device key (chosen)** | No engineer accounts needed; one leak revokes one token, not the fleet; fits the existing API-key infrastructure | Email is self-reported, not verified |
| Browser login through the customer's identity provider | Identity verified; nothing circulated | Every engineer must exist as a Neo.Tax user; depends on the company-wide auth migration |
| One shared organization key on every laptop | Simplest rollout | One leak revokes every device; no per-device identity |

## Risks

- **An engineer confirms the wrong email.** Sessions resolve to no contributor or the wrong one. Mitigated by the server surfacing devices whose email matches no known contributor to an operator.
- **The plugin data directory is synced or backed up off the machine.** The device key leaves the laptop. Accepted for v0.1; revisit OS keychain storage for the v1.0 rollout kit.

## Open Questions

- Whether the login command should verify the device key with a no-op ingest call before reporting success.
