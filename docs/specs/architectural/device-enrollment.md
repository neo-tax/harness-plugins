---
id: device-enrollment
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-14
version: 4
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
- `token-scope`(built): An enrollment token can do nothing except enroll devices into the one organization it was minted for.
  - status-reason: tested in platform-new projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts: a token enrolls devices into its own organization only
- `token-use-count-chosen-at-mint`(built): An admin chooses at mint time whether a token is single-use or multi-use.
  - status-reason: tested in platform-new projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts; single_use is set at mint and enforced by a conditional update
- `token-default-expiry`(built): An enrollment token expires seven days after mint unless the admin sets a different expiry.
  - status-reason: implemented in platform-new projects/server/src/harness-ingest/harness-enrollment.service.ts (DEFAULT_ENROLLMENT_TOKEN_TTL_MS); no test asserts the seven days
- `token-revocable`(built): An admin can revoke an enrollment token before it expires, and revoking it does not affect devices already enrolled with it.
  - status-reason: tested in platform-new projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts
- `device-key-scope`(built): A device key authorizes session ingest only; it cannot read or write any other Neo.Tax resource.
  - status-reason: tested in platform-new projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts; only the harness ingest route looks a credential up in harness_devices
- `device-key-revocable-individually`(built): Revoking one device key does not affect any other device key, including others minted from the same enrollment token.
  - status-reason: tested in platform-new projects/server/test/test-new/harness-ingest/harness-ingest.controller.spec.ts
- `one-key-per-device`(tested): A device holds exactly one device key; enrolling again replaces it.
- `interactive-enrollment`(tested): The engineer enrolls by invoking the harness's `neo-tax:login` entry point, which runs the agent's login command; the agent collects the enrollment token and the email itself, through prompts when it has a terminal and through a local browser form when it does not.
- `token-never-in-conversation`(tested): The agent never asks the harness's model to obtain the enrollment token and never writes the token to its output; when it cannot collect the token itself, its output names the login command to run in a terminal instead.
- `token-never-on-argv`(tested): The login command never takes the enrollment token as an argument, where the process list and shell history keep it; a script pipes it in with `--token-stdin`, which refuses a terminal.
- `form-address-printed-only-as-fallback`(tested): The agent's output, which the harness's model reads, carries the form's address only when no browser could be opened to it; otherwise it says the form is opening and names the terminal alternative.
- `browser-form-loopback`(tested): The browser form is served only on the loopback interface, at a path holding a one-time random secret, refuses any request whose `Host` is not that loopback address and port, and stops serving once enrollment succeeds or the wait ends.
- `browser-form-retries`(tested): A rejected token or an invalid email is reported in the form, which stays open for another try without the login command being run again.
- `browser-exchange-outlives-request`(tested): Once the browser form is submitted, the enrollment exchange runs to completion even if the browser drops the request, so a token the server has spent always leaves its device key stored.
- `browser-wait-bounded`(tested): The agent waits at most five minutes for the browser form, then exits reporting that login was not completed.
- `browser-terminal-alternative`(tested): Whether or not a browser opened, the agent's output names the login command to run in a terminal, for a machine whose browser cannot reach the form (for example over SSH).
- `login-skill-delegates-to-agent`(tested): In Claude Code, `neo-tax:login` is a skill whose only job is to instruct the model to run the agent's login command; it holds no logic of its own.
- `email-candidates`(tested): At login the agent offers the engineer the global git `user.email` and, when present, the email from the harness's own account file, at the terminal prompt or in the browser form, and also accepts a typed email.
- `email-confirmed-by-engineer`(tested): An interactive enrollment sends `email_confirmed_by` as `engineer`.
- `key-storage-location`(tested): The device key and device id are stored in the harness-provided plugin data directory, readable only by the owning user.
- `key-never-in-events`(tested): The device key never appears in a session event, a queue segment, a dead-letter file, or a log line.
- `rejected-key-stops-flushing`(tested): When ingest returns 401 or 403, the agent marks the device as needing re-enrollment, keeps queuing events, and does not attempt another flush until enrollment succeeds.
- `backfill-after-interactive-login`(tested): A successful interactive enrollment starts a backfill of existing transcripts immediately.
- `managed-enrollment-deferred`(built): Managed delivery of the enrollment token (a file placed by device management or an environment variable, enrolling silently at the first session start with `email_confirmed_by` as `auto` and a random delay before backfill) is not part of v0.1.
  - status-reason: intentionally absent; the agent has no code path that reads an enrollment token from a file or the environment

## Design Notes

Harness hook payloads carry no user identity, and the only on-disk sources (git `user.email`, the harness's own account file) are either per-repository or belong to another product. Capturing identity once at enrollment, where the engineer confirms it, and storing it on the device key means every session event afterwards carries only the device id: the hook path never reads identity, no per-session field is personal, and the server resolves device to email to contributor through its existing contributor matcher. A shared or re-imaged machine therefore needs re-enrollment, since a device key is one engineer on one machine. The git `user.email` in effect for the session's repository is still recorded per session as a secondary, non-identifying join signal for pull-request authorship, because the server's GitHub email mapping already keys on it.

The shape is the Tailscale auth-key model: the circulated secret is short-lived and can only enroll, and the per-laptop secret is minted, not copied, so a leaked enrollment token exposes nothing already enrolled and a lost laptop is revoked alone.

The harness account email is offered as a candidate because it is the identity the engineer most likely recognizes, but it is only ever read during login, never on the hook path, so the privacy statement's identity line stays one sentence.

A harness model runs the login command without a terminal, so the agent cannot prompt there. Relaying the question through the model would put the token in the conversation, where it reaches the model vendor and the local transcript, so instead the agent serves a one-page form on the loopback interface, opens the engineer's browser to it, and performs the enrollment exchange itself when the form is submitted; the conversation only sees that the form is opening, the terminal alternative, and the result. The form's address carries its secret, so it is printed only when no browser could be opened. The address carries a one-time secret and the server checks `Host`, so another local page cannot submit to it and a DNS-rebinding page cannot reach it. Doing the exchange inside the form's handler, rather than handing the values back to the command, is what lets a rejected token be corrected in place. On a terminal the agent prompts directly. For scripts, `--email` takes the email and `--token-stdin` reads the token from a pipe; there is no flag that takes the token itself, since an argument stays in the process list and shell history. `NEO_TAX_NO_BROWSER` set keeps the agent from opening a browser while still serving the form and printing its address, for tests and headless machines. Either way the collecting is the agent's; the skill only says to run the command and follow its output.

Every enrollment mints a new device id, so the agent stamps `device_id` on each event when it is sent rather than trusting the id written when it was queued; events queued while a rejected device waited for re-enrollment are then delivered as the newly enrolled device instead of being rejected.

The enrollment request keeps the machine's hostname, unhashed, as a human-readable label for the device id so engineers can tell their devices apart; it is read once at enrollment and goes stale if the machine is renamed, which is acceptable.

Under the later WorkOS path the same enrollment response is produced from a verified user claim instead of a pasted token; the agent's storage and rejection behavior do not change.
Under the later WorkOS path the same enrollment response is produced from a verified user claim instead of a pasted token; the agent's storage and rejection behavior do not change. The browser form is the step toward it: the engineer already finishes login in a browser, and only what happens behind the form changes, from a pasted token to an OAuth sign-in.

## Alternatives Considered

| Option | Pros | Cons |
| --- | --- | --- |
| **Enrollment token → per-device key (chosen)** | No engineer accounts needed; one leak revokes one token, not the fleet; fits the existing API-key infrastructure | Email is self-reported, not verified |
| Browser login through the customer's identity provider | Identity verified; nothing circulated | Every engineer must exist as a Neo.Tax user; depends on the company-wide auth migration |
| Token pasted into the harness conversation (v0.1 before this change) | No local server | The token reaches the model vendor and the transcript; the model has to relay a secret |
| Agent reads the token from the clipboard | No form | Reading the clipboard is surprising behavior for a security product |
| One shared organization key on every laptop | Simplest rollout | One leak revokes every device; no per-device identity |

## Risks

- **An engineer confirms the wrong email.** Sessions resolve to no contributor or the wrong one. Mitigated by the server surfacing devices whose email matches no known contributor to an operator.
- **The login skill's Bash call has no plugin data directory.** Claude Code substitutes `${CLAUDE_PLUGIN_DATA}` into hook commands but not into a skill's Bash command, so the plugin's launcher falls back to `~/.claude/plugins/data/neo-tax-neo-tax` (under `CLAUDE_CONFIG_DIR` when set), the directory Claude Code uses for `neo-tax@neo-tax`. If Claude Code changes that layout, login stores the key where hooks do not look. Mitigated by the install smoke test.
- **The plugin data directory is synced or backed up off the machine.** The device key leaves the laptop. Accepted for v0.1; revisit OS keychain storage for the v1.0 rollout kit.

- **The browser cannot reach the form.** Over SSH, in a remote container, or with no browser, the form is unreachable. Mitigated by the output naming the terminal command alongside the form's address, and by the five-minute bound.
- **Another local process submits the form.** Anything that learns the address could enroll this device with a token it holds. Accepted: the address is printed only when no browser could be opened, and then it enters the harness conversation, its transcript, and the model vendor; that grants nothing the model could not already do by running the login command itself, the secret is single-use and dies with the form, and the worst case is the device enrolling with someone else's valid token, which the engineer sees in the result.

## Open Questions

- When browser sign-in through OAuth (likely WorkOS) replaces the enrollment token, which removes the circulated secret and verifies the email.

- Whether the login command should verify the device key with a no-op ingest call before reporting success.
