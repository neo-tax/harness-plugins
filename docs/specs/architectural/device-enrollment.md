---
id: device-enrollment
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-13
version: 1
affects:
  - internal/queue/*.go
  - internal/enroll/*.go
  - internal/ingest/*.go
  - internal/adapter/claudecode/account.go
  - cmd/neo-tax-agent/*.go
  - plugins/claude-code/skills/login/SKILL.md
---

# Device enrollment

Governs how a device obtains its device key: the enrollment token an admin mints, the interactive exchange the engineer runs, where the key is stored, and how the agent behaves when the key is rejected. Identity is captured here and nowhere else.

## Requirements

- `enrollment-is-the-only-way-to-get-a-key`(tested): A device key is issued only by exchanging an enrollment token; no other path mints one.
- `token-scope`(approved): An enrollment token can do nothing except enroll devices into the one organization it was minted for.
- `token-use-count-chosen-at-mint`(approved): An admin chooses at mint time whether a token is single-use or multi-use.
- `token-default-expiry`(approved): An enrollment token expires seven days after mint unless the admin sets a different expiry.
- `token-revocable`(approved): An admin can revoke an enrollment token before it expires, and revoking it does not affect devices already enrolled with it.
- `device-key-scope`(approved): A device key authorizes session ingest only; it cannot read or write any other Neo.Tax resource.
- `device-key-revocable-individually`(approved): Revoking one device key does not affect any other device key, including others minted from the same enrollment token.
- `one-key-per-device`(tested): A device holds exactly one device key; enrolling again replaces it.
- `interactive-enrollment`(tested): The engineer enrolls by invoking the harness's `neo-tax:login` entry point, which runs the agent's login command; the agent prompts for the enrollment token and the email.
- `login-skill-delegates-to-agent`(tested): In Claude Code, `neo-tax:login` is a skill whose only job is to instruct the model to run the agent's login command; it holds no logic of its own.
- `email-candidates`(tested): At login the agent offers the engineer the global git `user.email` and, when present, the email from the harness's own account file, and also accepts a typed email.
- `email-confirmed-by-engineer`(tested): An interactive enrollment sends `email_confirmed_by` as `engineer`.
- `key-storage-location`(tested): The device key and device id are stored in the harness-provided plugin data directory, readable only by the owning user.
- `key-never-in-events`(tested): The device key never appears in a session event, a queue segment, a dead-letter file, or a log line.
- `rejected-key-stops-flushing`(tested): When ingest returns 401 or 403, the agent marks the device as needing re-enrollment, keeps queuing events, and does not attempt another flush until enrollment succeeds.
- `backfill-after-interactive-login`(approved): A successful interactive enrollment starts a backfill of existing transcripts immediately.
- `managed-enrollment-deferred`(built): Managed delivery of the enrollment token (a file placed by device management or an environment variable, enrolling silently at the first session start with `email_confirmed_by` as `auto` and a random delay before backfill) is not part of v0.1.
  - status-reason: intentionally absent; the agent has no code path that reads an enrollment token from a file or the environment

## Design Notes

Harness hook payloads carry no user identity, and the only on-disk sources (git `user.email`, the harness's own account file) are either per-repository or belong to another product. Capturing identity once at enrollment, where the engineer confirms it, and storing it on the device key means every session event afterwards carries only the device id: the hook path never reads identity, no per-session field is personal, and the server resolves device to email to contributor through its existing contributor matcher. A shared or re-imaged machine therefore needs re-enrollment, since a device key is one engineer on one machine. The git `user.email` in effect for the session's repository is still recorded per session as a secondary, non-identifying join signal for pull-request authorship, because the server's GitHub email mapping already keys on it.

The shape is the Tailscale auth-key model: the circulated secret is short-lived and can only enroll, and the per-laptop secret is minted, not copied, so a leaked enrollment token exposes nothing already enrolled and a lost laptop is revoked alone.

The harness account email is offered as a candidate because it is the identity the engineer most likely recognizes, but it is only ever read during login, never on the hook path, so the privacy statement's identity line stays one sentence.

A harness model runs the login command without a terminal, so the agent cannot prompt there. When the token or email is missing and standard input is not a terminal, the agent writes exactly what it needs (and the candidate emails) and exits with status 3; the model relays the question and runs the command again with `--token` and `--email`. On a terminal the agent prompts directly. Either way the prompting is the agent's; the skill only says to run the command and follow its output.

Every enrollment mints a new device id, so the agent stamps `device_id` on each event when it is sent rather than trusting the id written when it was queued; events queued while a rejected device waited for re-enrollment are then delivered as the newly enrolled device instead of being rejected.

The enrollment request keeps the machine's hostname, unhashed, as a human-readable label for the device id so engineers can tell their devices apart; it is read once at enrollment and goes stale if the machine is renamed, which is acceptable.

Under the later WorkOS path the same enrollment response is produced from a verified user claim instead of a pasted token; the agent's storage and rejection behavior do not change.

## Alternatives Considered

| Option | Pros | Cons |
| --- | --- | --- |
| **Enrollment token → per-device key (chosen)** | No engineer accounts needed; one leak revokes one token, not the fleet; fits the existing API-key infrastructure | Email is self-reported, not verified |
| Browser login through the customer's identity provider | Identity verified; nothing circulated | Every engineer must exist as a Neo.Tax user; depends on the company-wide auth migration |
| One shared organization key on every laptop | Simplest rollout | One leak revokes every device; no per-device identity |

## Risks

- **An engineer confirms the wrong email.** Sessions resolve to no contributor or the wrong one. Mitigated by the server surfacing devices whose email matches no known contributor to an operator.
- **The login skill's Bash call has no plugin data directory.** Claude Code substitutes `${CLAUDE_PLUGIN_DATA}` into hook commands but not into a skill's Bash command, so the plugin's launcher falls back to `~/.claude/plugins/data/neo-tax-neo-tax` (under `CLAUDE_CONFIG_DIR` when set), the directory Claude Code uses for `neo-tax@neo-tax`. If Claude Code changes that layout, login stores the key where hooks do not look. Mitigated by the install smoke test.
- **The plugin data directory is synced or backed up off the machine.** The device key leaves the laptop. Accepted for v0.1; revisit OS keychain storage for the v1.0 rollout kit.

- **The enrollment token passes through the harness conversation.** In Claude Code the engineer pastes the token into the conversation, so it reaches the model vendor and the local transcript. Accepted: the token is short-lived and can only enroll, which is the reason it is separate from the device key; an engineer can also run the login command in a terminal to keep it out of the conversation.

## Open Questions

- Whether the login command should verify the device key with a no-op ingest call before reporting success.
