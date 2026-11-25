---
id: office-agents-telemetry
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-11-24
updated: 2026-11-24
version: 1
affects:
  - deploy/claude-code/README.md
  - internal/fakeingest/otlp*.go
  - internal/fakeingest/server.go
  - internal/allowlist/telemetry_statement.go
  - PRIVACY.md
---

# Office Agents telemetry

Governs what this repository ships for Claude's Office Agents (Claude in Word, Excel, and PowerPoint) as a telemetry source: the admin guidance that turns its export on, the fake ingest server's support for what it sends, and the privacy statement's coverage of it. The endpoint that receives it is platform-new's `otlp-ingest`; the requirements it needs there are listed under Endpoint dependencies and are not yet in that spec.

Office Agents is configured only in the Claude admin console, with no machine-side setting, and it has no switch that keeps content out of its export. So unlike Claude Code's export, where this repository's managed settings keep content on the machine (`claude-code-telemetry-export#managed-settings-no-content`), here the endpoint's mapping is the only thing between a prompt and storage.

## Requirements

### Admin guidance

- `admin-form-empty-until-supported`(built): Until the endpoint meets every requirement under Endpoint dependencies, the deploy guide tells admins to leave the Office Agents monitoring form empty.
  - status-reason: `deploy/claude-code/README.md`, "Cowork" section; documentation, which no test checks
- `admin-form-settings`(approved): Once the endpoint meets them, the deploy guide gives the Office Agents form's values (OTLP endpoint `https://api.neo.tax/otlp`, protocol `http/json`, headers `Authorization=Bearer <ingest key>`) and says that Office Agents sends prompts, document paths, and document content, which Neo.Tax discards on arrival.

### Local testing

- `fake-ingest-accepts-traces`(approved): The fake ingest server accepts OTLP/JSON trace export requests at `/otlp/v1/traces` under the same authentication, content-type, gzip, size, and malformed-request rules as `claude-code-telemetry-export#fake-ingest-accepts-otlp`, and records every span unfiltered: its name, trace, span, and parent span ids, start and end times, attributes, events with their attributes, and its resource's attributes.
- `fake-ingest-answers-cors`(approved): The fake ingest server answers a CORS preflight on its OTLP routes from any origin, allowing `POST` with the `Authorization` and `Content-Type` headers, and sets `Access-Control-Allow-Origin` on the response to the POST itself, so an exporter running in a browser can reach it.

### Privacy statement

- `privacy-statement-covers-office-agents`(approved): The published privacy statement lists the span attributes the endpoint keeps from Office Agents, and says that Office Agents' export carries the prompt, the document's path, and tool input and output (document content), which the endpoint discards before storing anything.

## Endpoint dependencies

These belong in platform-new's `otlp-ingest` spec. They are stated here so this repository's requirements can cite them until they are added there.

- **Traces route.** `POST /otlp/v1/traces` accepts OTLP/JSON trace export requests under an ingest key, with the same transport rules as the logs and metrics routes.
- **CORS.** The OTLP routes answer a preflight from `https://pivot.claude.ai` allowing `POST` with `Authorization` and `Content-Type`, and set `Access-Control-Allow-Origin` on the POST response. Without both, the browser drops every export, and the add-in sees only a network error.
- **Office Agents mapping.** Only `agent.stream` spans become turns. One span is one API call, and the mapping keeps `model`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `stop_reason`, `request_id` (the response's `msg_…` id, so the call id), `session.id`, `user.email`, `user.account_uuid`, `organization.id`, `agent.surface`, and the span's start and end times. Every other span and attribute is dropped before storage, including `user.message`, `document.url`, `tool.input`, and `tool.output`, and none of them reaches a log line.

## Design Notes

The export's shape was observed on 2026-11-24 from Claude in Word on macOS (Office 16.115, add-in build `ecebbf48`), through a capture server reached over Tailscale Funnel; the captures were deleted afterward because they held a real prompt and document text.

- **It runs in the browser.** The add-in exports from the Office webview (`Origin: https://pivot.claude.ai`, a cross-site request), not from Anthropic's servers. The `Authorization` header makes every export preflighted.
- **Traces only.** Every request was `POST <base>/v1/traces`, with the base URL taken from the admin form as entered. No logs or metrics were sent. Bodies were OTLP/JSON, uncompressed.
- **One trace per question.** An `agent.query` root span carries the request's context; each API call is an `agent.stream` span with token counts and the response id; each tool call is an `agent.tool_execution` span. Every span carries `user.email`, `user.account_uuid`, `organization.id`, and `session.id`. `agent.stream` spans also carry `office_agent.token.usage` events that repeat the span's token counts by type.
- **Content is always on.** `agent.query` carries `user.message` (the prompt) and `document.url` (the document's local file path). `agent.tool_execution` carries `tool.input` (generated Office.js code) and `tool.output` (what it read from the document). The admin form has no setting that removes them.

The fake ingest server records spans unfiltered, as it does logs and metrics, because it exists to show what a source sends. Its CORS answer allows any origin, since the add-in's origin is the only one that matters to it and a stricter fake would only fail in a way the real endpoint's tests already cover.

## Risks

- **The ingest key is readable in the browser.** The add-in sends the key from the user's Office webview, so anyone using Office Agents can read it in the developer tools. Accepted on the same terms as the key cached on disk for Claude Code: it can only write telemetry to its organization, and it can be revoked.
- **The shape changes without notice.** The span names and attributes are observed, not documented by Anthropic. A rename would drop turns silently. The mapping should count spans it could not map, so a rename shows up as a rate rather than a gap.

## Open Questions

- Whether Office on Windows and Office on the web send the same origin and shape as Office on macOS.
- Whether the endpoint should refuse a span whose `organization.id` doesn't match the organization of the ingest key that sent it.
- What `agent.vendor` (observed as `m`) and `user.bucket` mean, and whether either belongs in the mapping.
