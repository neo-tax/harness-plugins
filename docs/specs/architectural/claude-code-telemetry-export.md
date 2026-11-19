---
id: claude-code-telemetry-export
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-11-02
updated: 2026-11-18
version: 16
affects:
  - deploy/claude-code/*
  - internal/fakeingest/otlp*.go
  - cmd/fake-ingest/*.go
  - scripts/dev.sh
  - internal/allowlist/telemetry_statement.go
  - internal/allowlist/statement.go
  - PRIVACY.md
---

# Claude Code telemetry export

Governs what this repository ships for Neo.Tax's second entry point into the session record, the OTLP endpoint that takes Claude Code's own telemetry: the managed settings that turn the export on, the local stack that exercises it against the fake ingest server, the privacy statement's telemetry section, and from v1.0 the agent command that authenticates the export as its device. The endpoint itself (what it accepts, keeps, and joins with the agent's session events) is specified in platform-new's `docs/specs/architectural/platform/otlp-ingest.spec.md`, which this spec cites as `otlp-ingest`. The agent's own path is governed by `session-event-wire-contract`, which the export neither uses nor changes.

## Requirements (v0.1)

v0.1 sends Claude Code's own export directly, through managed settings, under the organization's ingest key.

### Exporter configuration

- `managed-settings-exporter`(tested): The managed settings Neo.Tax ships to turn on Claude Code's export, kept in this repository as `deploy/claude-code/managed-settings.json`, set `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_LOGS_EXPORTER=otlp`, `OTEL_METRICS_EXPORTER=otlp`, `OTEL_EXPORTER_OTLP_ENDPOINT` to the base endpoint `https://api.neo.tax/otlp`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/json`, `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta`, `OTEL_METRICS_INCLUDE_REPOSITORY=true`, and `OTEL_EXPORTER_OTLP_HEADERS` carrying the ingest key as a bearer token, and set no per-signal exporter variable that would override them.
- `exporter-minimum-version`(built): The telemetry path supports Claude Code 2.1.269 and later, the first version that sends repository attributes; an older Claude Code's telemetry-only turns have a null repo.
  - status-reason: the managed settings use nothing a 2.1.269 exporter lacks, and the endpoint stores a null repo when the repository attributes are absent (platform-new's `otlp-ingest#claude-code-repo-mapping`, tested there); the managed settings deliberately set no minimum version, as the design notes explain
- `managed-settings-no-content`(tested): The managed settings Neo.Tax ships to turn on Claude Code's export never enable `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT`, or `OTEL_LOG_RAW_API_BODIES`, so Claude Code redacts content before it leaves the machine.

### Privacy statement

- `privacy-statement-covers-telemetry`(built): The published privacy statement covers this path as well as the agent's: it lists, per source, the log and metric attributes the endpoint keeps, and says that content is dropped as `otlp-ingest#content-never-stored` describes.
  - status-reason: `PRIVACY.md`'s "Claude Code's own telemetry" section, rendered from the lists in internal/allowlist/telemetry_statement.go; TestStatementCoversTelemetry checks every listed attribute and the content statement appear, but the lists are copied by hand from platform-new's `otlp-ingest#server-allowlist`, `#metric-allowlist`, and the Claude Code mapping requirements, and nothing checks them against the server

### Local testing

- `fake-ingest-accepts-otlp`(tested): The fake ingest server accepts OTLP/JSON log and metric export requests at `/otlp/v1/logs` and `/otlp/v1/metrics`, under any ingest key it was started with (any key when none was given) and never under a device key, with `otlp-ingest#authenticate-first`, `#json-accepted`, `#gzip-accepted`, and `#malformed-request-refused`, and records every log record and metric data point unfiltered, so the plugin and a harness's own telemetry can run end to end against one local server.
- `dev-stack-exports-telemetry`(built): The local dev stack runs Claude Code with its telemetry exporting logs and metrics to the fake ingest server, beside the plugin, with the exporter settings `managed-settings-exporter` names, repository attributes included, apart from the endpoint and key.
  - status-reason: `scripts/dev.sh claude` sets the exporter environment; exercised by hand against a real Claude Code, which no automated test runs
- `dev-stack-clears-inherited-telemetry`(built): Before setting its own telemetry environment, the dev stack clears every `OTEL_*` and `CLAUDE_CODE_OTEL_*` variable and `CLAUDE_CODE_ENABLE_TELEMETRY` inherited from the shell, so a dev session never exports to a collector the engineer's shell names and never turns on content logging the fake would store; it warns when a managed settings file sets telemetry, which it cannot override.
  - status-reason: `scripts/dev.sh claude` unsets them; the clearing step was run by hand with `OTEL_LOG_USER_PROMPTS`, a per-signal logs endpoint, `CLAUDE_CODE_OTEL_HEADERS_HELPER`, and `CLAUDE_CODE_ENABLE_TELEMETRY` exported and left none set, which no automated test runs
- `dev-stack-capture`(built): `scripts/dev.sh capture` runs a new session, a resume, a fork, and an interactive `/clear` through the dev stack, and fails unless the plugin and telemetry agree on every session id, a fork re-sends every one of its parent's turns and the fake ingest server drops each as a duplicate rather than storing it under the fork, and every plugin turn has a telemetry call with its request id.
  - status-reason: it drives a real, logged-in Claude Code, which CI does not have; run it by hand against each new Claude Code version
- `dev-stack-private`(built): Every file the local dev stack writes under `.dev`, including the fake ingest server's recordings and the slices `capture` copies from them, is readable by the engineer alone, because recorded telemetry carries their email and account ids.
  - status-reason: `scripts/dev.sh` sets `umask 077` and makes `.dev` mode 700, and the fake ingest server opens its recordings 0600; no automated test checks the modes

## Requirements (v1.0)

From v1.0 Claude Code's export authenticates as the device that sent it, through the agent, instead of with the organization's static ingest key. The credential's lifetime and scope on the server are `otlp-ingest#telemetry-credential-lifetime` and `#telemetry-credential-scope`.

- `device-bound-telemetry`(approved): The managed settings Neo.Tax ships set `otelHeadersHelper` to the agent's `otel-headers` command and carry no ingest key, so each machine's export authenticates with a telemetry credential bound to its device.
- `otel-headers-command`(approved): The agent's `otel-headers` command prints a JSON object whose only member is an `Authorization` header holding a bearer telemetry credential, obtained by presenting the device key to Neo.Tax and reused from the plugin data directory while it has more than 30 minutes left; it prints nothing and exits non-zero when the device is not enrolled, needs re-enrollment, or cannot reach Neo.Tax and holds no unexpired credential.

## Design Notes

The exporter settings are explicit because Claude Code's defaults do not match the endpoint. From 2.1.323 the default protocol is `http/protobuf`, which v0.1 answers with 415, a status the exporter does not retry, so every record would be lost without notice. Repository attributes are off unless `OTEL_METRICS_INCLUDE_REPOSITORY` is set, which would leave every telemetry-only turn with no repo. Delta temporality is already the default, and is set anyway so a later default change cannot break it.

The managed settings set no minimum Claude Code version. Claude Code's managed `requiredMinimumVersion` makes an older Claude Code refuse to start, which would stop an engineer's work over a telemetry gap, and `minimumVersion` only stops auto-updates from going lower, so it would not move an older install forward. An older exporter costs only the repo on its telemetry-only turns; a turn the plugin also reported takes its repo from the plugin.

The privacy statement's telemetry section is written by hand, unlike the rest of the statement, because what the endpoint keeps is decided by mappings in platform-new. It lists the attributes Claude Code's mapping keeps and is changed in the same release as the mapping.

The managed settings leave Claude Code's content switches off, so on that configuration content does not leave the machine at all. The server's own allowlist (`otlp-ingest#server-allowlist`) covers an admin who turns them on, so this repository's guarantee is about what Neo.Tax ships, not about what arrives.

Claude Code also supports `otelHeadersHelper`: a command, run at startup and every 29 minutes, whose printed JSON becomes the export's HTTP headers. It works only over HTTP, which v0.1 already requires. v1.0 points it at the agent, which trades the device key for a short-lived telemetry credential, so Claude Code's export is attributed to an enrolled device the way the agent's session events are, and the static key is left to sources with no agent. A device that is not enrolled then sends no telemetry until it is, since Claude Code exports nothing while the helper fails.

The fake ingest server's OTLP endpoints mirror the real endpoint's transport rules so the plugin and Claude Code's export can run end to end against one local server, but they record everything unfiltered: they are a tool for seeing what Claude Code sends, not a model of what Neo.Tax keeps. `scripts/dev.sh capture` is how a new Claude Code version's session ids, forks, `/clear`, and request ids are checked against the plugin before the endpoint's joins rely on them.

## Risks

- **The headers helper fails.** From v1.0, a machine whose agent cannot obtain a telemetry credential exports nothing until it can, and Claude Code shows a warning once per session. Accepted; the plugin's own path still reports the machine's turns.

## Decision History

- 2026-11-18: The OTLP endpoint's requirements moved to platform-new, which builds it, leaving here only what this repository ships for it; this spec was `otlp-ingest` until then.

## Open Questions

- Whether the privacy statement's telemetry section should be generated from the endpoint's mappings, as the agent's section is generated from the field allowlist, once a second source makes copying them by hand error-prone. The mappings live in platform-new, so this needs them published somewhere this repo can read, such as a file vendored back here the way the contract is vendored there.
