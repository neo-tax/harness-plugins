---
id: otlp-ingest
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-11-02
updated: 2026-11-02
version: 1
affects:
---

# OTLP ingest

Governs Neo.Tax's second entry point into the session record: an OpenTelemetry (OTLP) endpoint that takes a harness's own telemetry, or a gateway's, under an organization-level ingest key, keeps only an allowlisted set of attributes, and joins what it receives with the plugin's session events. Claude Code's built-in telemetry is its first source. The plugin's path is governed by `session-event-wire-contract`, which this endpoint neither uses nor changes.

## Requirements

- `second-entry-point`(approved): The OTLP endpoint writes to the same session record as the plugin's ingest endpoint, and a session's turns can arrive through either path or both.
- `otlp-http-json-logs`(approved): The endpoint accepts OTLP/HTTP log export requests with JSON bodies at `/otlp/v1/logs` on the Neo.Tax API host, the path a standard exporter derives from the base endpoint `https://api.neo.tax/otlp`.
- `json-only`(approved): A request whose content type is not `application/json` is answered 415 without its body being read.
- `malformed-request-refused`(approved): A body that is not an OTLP log export request is answered 400 and nothing in it is stored.
- `partial-success`(approved): A request in which some records are rejected is answered 200 with OTLP's `partial_success`, carrying the number of rejected records and a message naming why the first was rejected; the request's other records are stored.
- `ingest-key-bearer`(approved): A request authenticates with an ingest key sent as a bearer token in the `Authorization` header, so an exporter can carry it as a static header.
- `ingest-key-refusal`(approved): A missing, unknown, or revoked ingest key is answered 401 with the same body for each, and nothing in the request is stored.
- `ingest-key-organization`(approved): An ingest key belongs to exactly one organization, and every record accepted under it is stored in that organization.
- `ingest-key-scope`(approved): An ingest key authorizes OTLP ingest only; it cannot enroll a device, post session events, or read any Neo.Tax resource.
- `other-credentials-refused`(approved): A device key or enrollment token presented to the OTLP endpoint is refused as an unknown ingest key.
- `ingest-key-per-destination`(approved): An organization can hold several ingest keys at once, each labeled at mint with the source that will send under it.
- `ingest-key-revocable-individually`(approved): Revoking one ingest key does not affect the organization's other ingest keys, its enrollment tokens, or its device keys.
- `ingest-key-stored-hashed`(approved): Neo.Tax stores an ingest key only as a hash and a display prefix, and shows the plaintext once, at mint.
- `mapping-per-source`(approved): Each source has its own mapping from its attribute names to the session record, chosen by the record's event name, and a record no mapping claims is dropped.
- `claude-code-api-request-is-turn`(approved): Each Claude Code `claude_code.api_request` log record becomes one turn of the session named by its `session.id` attribute.
- `claude-code-other-events-dropped`(approved): Every other Claude Code event, including prompts, assistant responses, tool results and decisions, API errors, and API request and response bodies, is dropped and is not counted as rejected.
- `claude-code-turn-mapping`(approved): A Claude Code turn takes its model from `model`; its input, output, and cache read counts from `input_tokens`, `output_tokens`, and `cache_read_tokens`; its cache write counts from `cache_creation_5m_tokens` and `cache_creation_1h_tokens`; its timestamp from `event.timestamp`; its request id from `request_id`; and its estimated cost from `cost_usd`.
- `claude-code-unsplit-cache-write`(draft): A Claude Code turn without the split cache write attributes counts `cache_creation_tokens` as five-minute cache writes.
- `claude-code-repo-mapping`(approved): A Claude Code turn's repo is the host of `vcs.repository.url.full` with `vcs.owner.name` and `vcs.repository.name`, each lowercase, in the same shape as the wire contract's `repo`, and is null when those attributes are absent.
- `claude-code-record-rejected`(approved): A `claude_code.api_request` record without a `session.id`, or with a token count that is not a non-negative integer, is rejected.
- `server-allowlist`(approved): Of each record the endpoint keeps only the attributes its source's mapping names: model, token counts, estimated cost, event timestamp, session id, request id, user email, and repo; no other attribute, resource attribute, or record body is stored.
- `content-never-stored`(approved): Prompt text, response text, tool inputs and outputs, commands, file paths, and API bodies are dropped even when the sending harness is configured to export them.
- `allowlist-before-write`(approved): The allowlist is applied before any part of a record is written to storage or to a log line.
- `engineer-from-user-email`(approved): A telemetry turn's engineer is its `user.email`, resolved to a contributor the same way a device's confirmed email is; a turn without `user.email` is stored with no engineer.
- `estimated-cost-labeled`(approved): A telemetry turn's cost is stored as the harness's own estimate, distinct from list-price, billed, or invoiced cost.
- `telemetry-turn-idempotent`(approved): Two telemetry turns in one organization with the same request id are one turn, so a re-exported record adds nothing.
- `telemetry-turn-without-request-id`(draft): A telemetry turn without a `request_id` is keyed by its session id, timestamp, and model, and never joins a plugin turn.
- `join-on-request-id`(approved): A plugin `session.turn` and a telemetry turn in the same organization with the same non-null request id are one turn, counted once.
- `one-turn-per-request-id`(draft): Plugin turns in one organization that share a request id are one turn: the first stored keeps its session, and later ones are counted as duplicates.
- `join-order-independent`(approved): A joined turn is the same whichever of its two paths arrives first.
- `telemetry-wins-usage`(approved): A joined turn takes its model, token counts, and cost from telemetry.
- `plugin-wins-context`(approved): A joined turn takes its session, branch, head SHA, and subagent id from the plugin, and telemetry never sets them.
- `pr-opened-plugin-only`(approved): A session's pull requests opened come only from the plugin's `session.pr_opened` events.
- `single-path-turn-kept`(approved): A turn that arrives through only one path is stored as it arrived: a telemetry-only turn has no branch, and a plugin-only turn keeps its transcript usage.
- `side-usage-yields-to-telemetry`(draft): A session with at least one plugin turn, every one of which joined a telemetry turn, does not add its plugin `side_usage` to its usage, because telemetry reports those side calls as turns of their own; any other session adds it.

## Design Notes

OTLP is the entry point because it is what keeps a capture path swappable. Nearly every gateway and observability stack exports it, and Claude Code exports it natively as a supported interface, unlike the transcript the plugin parses. It also reaches places the plugin does not load: managed settings turn it on in Claude Code's cloud sessions, which have no laptop and no hooks. OTLP fixes the transport, not the attribute names, and the GenAI semantic conventions are still experimental, so each source needs its own mapping onto the session record. Adding a source means adding a mapping, not building another endpoint.

The plugin's allowlist runs on the engineer's machine, so it says nothing about what arrives here: the customer's admin configures the exporter, and an exporter can be told to send prompts, tool inputs, and whole API bodies. The server therefore enforces its own allowlist, named by the mapping, and drops the rest before anything is written. A customer who turns on prompt logging for their own collector sends us nothing more than one who does not.

The credential is per organization, not per device, because an exporter is configured once for a fleet through managed settings and cannot run enrollment. A static header is the only credential every exporter supports. Managed settings cache it on every machine, which is acceptable only because the key can do nothing but write telemetry into its own organization and can be revoked without touching anything else. One key per destination (dogfood telemetry, a gateway, a customer's own collector) lets one leak be revoked alone.

A telemetry record identifies the engineer directly, through the harness's sign-in email, where a session event identifies only its device. That email is the harness vendor's account, not one the engineer confirmed to Neo.Tax, so it is resolved through the same contributor matcher and is no more trusted than a device's email.

The request id is the join key because both paths carry the vendor's own id for the same API call, unchanged: the plugin reads it from the transcript and telemetry from the response header. A session id would not do. Claude Code replaces `session.id` on `/clear`, and a session id names a whole session, not a turn. A request id names one API call across every session, so the join and the telemetry idempotency key are the organization and request id alone, and a joined turn belongs to the plugin's session, which its `session.opened` and `session.pr_opened` events also name. Joining on the request id lets each path supply what it is good at. Telemetry has the supported usage and Claude Code's cost estimate. The plugin has the branch and head SHA per turn and the pull requests opened. Running both is what lets v0.1 measure how often sessions match a pull request with the plugin's branch and without it.

Telemetry reports every API call, including the side calls (session title, recap, compaction summary) that the plugin can see only as session totals on `session.closed`. Adding both would count side calls twice, so when telemetry covered every turn the plugin saw, the plugin's `side_usage` stops contributing to that session's usage.

The endpoint answers rejected records through OTLP's own `partial_success` rather than a Neo.Tax shape, so an unmodified exporter handles the response correctly. OTLP's response carries a count and one message, not a per-record index the way the plugin's ingest response does.

## Alternatives Considered

| Option | Pros | Cons |
| --- | --- | --- |
| **OTLP endpoint beside the plugin's (chosen)** | Any OTLP exporter or gateway connects with configuration only; reaches cloud sessions; uses a supported Claude Code interface | A second credential kind and a second allowlist to keep honest; a mapping per source |
| Plugin only | One credential, one allowlist, one contract | Transcript usage depends on an unsupported format; no cloud sessions; every gateway needs custom work |
| Route telemetry through the agent | The agent's allowlist would apply | The agent does not run in cloud sessions or gateways, which are the reason for this path |
| Device key on the exporter | Reuses the existing credential | An exporter cannot enroll, and one shared device key breaks the one-engineer-per-key rule |

## Risks

- **A source renames an attribute.** Its turns lose usage or are rejected without the exporter noticing. Mitigated by `partial_success` counts and a rejected-record rate watched per source.
- **The ingest key leaks from a managed machine.** Anyone holding it can write false telemetry into that organization. Accepted for a write-only key; revoke and remint for that destination.
- **The plugin and telemetry disagree on a joined turn's usage.** Telemetry's counts are taken silently. Accepted; the difference between the two is itself a measure of the transcript parser's accuracy.
- **Telemetry covers only part of a session.** An exporter turned on mid-session, or records lost in transit, leave some turns plugin-only, so that session adds the plugin's whole `side_usage` on top of the side calls telemetry did report. Accepted as a small overcount on partly covered sessions, which the rate of plugin-only turns per session makes visible.
- **An exporter is left on OTLP's protobuf default.** Every request is refused with 415 and nothing arrives. Mitigated by the managed settings and the setup instructions for each source setting the protocol to `http/json`.
- **An exporter also sends metrics.** The dogfood managed settings set a metrics exporter, so the same base endpoint receives `/otlp/v1/metrics` requests it does not accept, and the exporter logs and retries them. Mitigated by leaving the metrics exporter out of managed settings.

## Open Questions

- Whether Claude Code's `session.id` always equals the `session_id` its hooks receive, so a telemetry-only turn and the plugin's `session.opened` name the same session.
- Whether a forked Claude Code session re-sends its parent's turns under the new session id with the same request ids, which `one-turn-per-request-id` assumes it may.
- What a turn without a `request_id` should be keyed by, and whether such turns occur on the Anthropic API at all.
- Whether `/otlp/v1/metrics` should be answered with success and nothing stored, so a customer who keeps the metrics exporter on sees no errors.
- Whether the privacy statement should describe this path, since what telemetry sends is set by the customer's admin, not by the plugin.
