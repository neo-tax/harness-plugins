---
id: otlp-ingest
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-11-02
updated: 2026-11-11
version: 4
affects:
---

# OTLP ingest

Governs Neo.Tax's second entry point into the session record: an OpenTelemetry (OTLP) endpoint that takes a harness's own telemetry, or a gateway's, as logs and metrics, under an organization-level ingest key, keeps only an allowlisted set of attributes, and joins what it receives with the plugin's session events. Claude Code's built-in telemetry is its first source. The plugin's path is governed by `session-event-wire-contract`, which this endpoint neither uses nor changes.

## Requirements (v0.1)

v0.1 takes Claude Code's own export, sent directly by managed settings: logs and metrics, OTLP/JSON, delta temporality, joined to the plugin's turns on the request id.

### Transport and authentication

- `second-entry-point`(approved): The OTLP endpoint writes to the same session record as the plugin's ingest endpoint, and a session's turns can arrive through either path or both.
- `otlp-http-logs`(approved): The endpoint accepts OTLP/HTTP log export requests at `/otlp/v1/logs` on the Neo.Tax API host, the path a standard exporter derives from the base endpoint `https://api.neo.tax/otlp`.
- `otlp-http-metrics`(approved): The endpoint accepts OTLP/HTTP metric export requests at `/otlp/v1/metrics`, the path a standard exporter derives from the same base endpoint, under the same ingest key, encodings, and limits as logs.
- `json-accepted`(approved): An authenticated request is decoded as OTLP/JSON when its content type is `application/json`; any other content type is answered 415 without its body being read.
- `gzip-accepted`(approved): A body sent with `Content-Encoding: gzip` is decompressed before it is decoded, and a body whose decompressed size exceeds the endpoint's limit is answered 413 and nothing in it is stored.
- `authenticate-first`(approved): The ingest key is checked before the content type or the body, so a request with a bad key is answered 401 whatever else is wrong with it.
- `malformed-request-refused`(approved): A body that is not an OTLP export request for its path's signal is answered 400 and nothing in it is stored.
- `partial-success`(approved): A request in which some records are rejected is answered 200 with OTLP's partial success message in the request's encoding (in JSON, `partialSuccess` carrying `rejectedLogRecords`, or `rejectedDataPoints` for metrics, and an `errorMessage` naming why the first was rejected); the request's other records are stored.
- `ingest-key-bearer`(approved): A request authenticates with an ingest key sent as a bearer token in the `Authorization` header, so an exporter can carry it as a static header.
- `ingest-key-refusal`(approved): A missing, unknown, or revoked ingest key is answered 401 with the same body for each, and nothing in the request is stored.
- `ingest-key-organization`(approved): An ingest key belongs to exactly one organization, and every record accepted under it is stored in that organization.
- `ingest-key-scope`(approved): An ingest key authorizes OTLP ingest only; it cannot enroll a device, post session events, or read any Neo.Tax resource.
- `other-credentials-refused`(approved): A device key or enrollment token presented to the OTLP endpoint is refused as an unknown ingest key.
- `ingest-key-per-destination`(approved): An organization can hold several ingest keys at once, each labeled at mint with the source that will send under it.
- `ingest-key-revocable-individually`(approved): Revoking one ingest key does not affect the organization's other ingest keys, its enrollment tokens, or its device keys.
- `ingest-key-stored-hashed`(approved): Neo.Tax stores an ingest key only as a hash and a display prefix, and shows the plaintext once, at mint.

### Claude Code log mapping

- `mapping-per-source`(approved): Each source has its own mapping from its attribute names to the session record, chosen by the record's event name, and a record no mapping claims is dropped.
- `claude-code-api-request-is-turn`(approved): Each Claude Code `claude_code.api_request` log record becomes one turn of the session named by its `session.id` attribute.
- `claude-code-other-events-dropped`(approved): Every other Claude Code event, including prompts, assistant responses, tool results and decisions, API errors, and API request and response bodies, is dropped and is not counted as rejected.
- `claude-code-fixture`(approved): The Claude Code mapping is specified by a fixture: a captured Claude Code OTLP/JSON log export paired with the turns it must produce.
- `claude-code-turn-mapping`(approved): A Claude Code turn takes its model from `model`; its input, output, and cache read counts from `input_tokens`, `output_tokens`, and `cache_read_tokens`; its cache write counts from `cache_creation_5m_tokens` and `cache_creation_1h_tokens`; its timestamp from `event.timestamp`; its request id from `request_id`; and its estimated cost from `cost_usd`.
- `claude-code-unsplit-cache-write`(draft): A Claude Code turn without the split cache write attributes counts `cache_creation_tokens` as five-minute cache writes.
- `claude-code-repo-mapping`(approved): A Claude Code turn's repo is the host of `vcs.repository.url.full` with `vcs.owner.name` and `vcs.repository.name`, each lowercase, in the same shape as the wire contract's `repo`, and is null when those attributes are absent.
- `numeric-attribute-encodings`(approved): A token count is read from an `intValue` or from a `stringValue` holding a base-10 integer, and a cost from a `doubleValue`, an `intValue`, or a `stringValue` holding a decimal number.
- `claude-code-record-rejected`(approved): A `claude_code.api_request` record without a `session.id`, missing `input_tokens`, `output_tokens`, or `cache_read_tokens`, or with a token count that is not a non-negative integer, is rejected.
- `claude-code-optional-attributes`(approved): Of the mapped attributes, only `session.id` and the input, output, and cache read counts are required: a missing `model` or `user.email` is stored as null, a missing or unparseable `event.timestamp` falls back to the log record's `timeUnixNano` when it is non-zero, then to its `observedTimeUnixNano` when it is non-zero, and a record with none of the three is rejected, and a missing `request_id` leaves the turn to `turn-without-call-id` (and, from v1.0, `join-on-fingerprint`).
- `claude-code-missing-cache-write`(draft): A record with neither the split cache write attributes nor `cache_creation_tokens` has zero cache writes.
- `claude-code-cost-optional`(approved): A `claude_code.api_request` record whose `cost_usd` is missing or unreadable is stored with a null cost rather than rejected, because its usage is still exact.

### Allowlist and identity

- `server-allowlist`(approved): Of each log record the endpoint keeps only the attributes its source's mapping names: model, token counts, estimated cost, event timestamp, session id, call ids, user email, and repo; no other attribute, resource attribute, or record body is stored.
- `metric-allowlist`(approved): Of each metric data point the endpoint keeps only the metric name, its value, its start and end times, and the attributes its source's mapping names: for Claude Code, `session.id`, `user.email`, `model`, `type`, `query_source`, and the repo attributes; no other attribute (including agent, skill, plugin, and MCP server names), resource attribute, or exemplar is stored.
- `content-never-stored`(approved): Prompt text, response text, tool inputs and outputs, commands, file paths, and API bodies are dropped even when the sending harness is configured to export them.
- `allowlist-before-write`(approved): The allowlist is applied before any part of a record is written to storage or to a log line.
- `engineer-from-user-email`(approved): A telemetry-only turn's engineer, and a metric data point's, is its `user.email`, resolved to a contributor the same way a device's confirmed email is; a turn without `user.email` is stored with no engineer.
- `estimated-cost-labeled`(approved): A telemetry turn's cost is stored as the harness's own estimate, distinct from list-price, billed, or invoiced cost.

### Joining with the plugin's turns

- `call-ids`(approved): A turn's call ids are the model provider's own ids for the API call it records: its response id (the plugin's `message_id`; OTLP's `gen_ai.response.id`) and its request id (the plugin's `request_id`; Claude Code telemetry's `request_id`). Either may be null, and a source's mapping names which attributes carry them.
- `join-on-request-id`(approved): Two turns in one organization that were not joined by response id and share a non-null request id are one turn, counted once, unless both carry response ids and those differ.
- `telemetry-turn-idempotent`(approved): A re-exported telemetry record is joined to its earlier copy by `join-on-response-id` or `join-on-request-id`, so it adds nothing.
- `turn-without-call-id`(draft): A telemetry turn with neither call id, and from v1.0 no fingerprint match, is stored on its own, keyed by its session id, timestamp, and model.
- `one-turn-per-call-id`(draft): Plugin turns in one organization that share a call id are one turn: the first stored keeps its session, and later ones are counted as duplicates.
- `join-key-recorded`(approved): A joined turn records the key that joined it (response id, request id, or fingerprint), so the join rate can be measured per source and per key.
- `join-order-independent`(approved): A joined turn is the same whichever of its two paths arrives first.
- `telemetry-wins-usage`(approved): A joined turn takes its token counts and cost from telemetry, and its model from telemetry when telemetry carries one, else from the plugin.
- `plugin-wins-context`(approved): A joined turn takes its session, engineer, timestamp, repo, branch, head SHA, and subagent id from the plugin, its engineer being the one its device resolves to, and telemetry never sets them.
- `plugin-wins-otherwise`(approved): Any field of a joined turn that both paths carry and no other requirement assigns comes from the plugin.
- `pr-opened-plugin-only`(approved): A session's pull requests opened come only from the plugin's `session.pr_opened` events.
- `single-path-turn-kept`(approved): A turn that arrives through only one path is stored as it arrived: a telemetry-only turn has no branch, and a plugin-only turn keeps its transcript usage.
- `side-usage-yields-to-telemetry`(draft): A session with at least one plugin turn, every one of which joined a telemetry turn, does not add its plugin `side_usage` to its usage, because telemetry reports those side calls as turns of their own; any other session adds it.

### Claude Code metrics

- `metrics-never-turns`(approved): A metric data point never creates, changes, or joins a turn, and no metric value adds to a session's usage, because Claude Code reports the same API calls as `claude_code.api_request` log records and adding both would count them twice.
- `claude-code-session-activity`(approved): From Claude Code's metrics the endpoint stores, per session and engineer, the totals of `claude_code.lines_of_code.count` by `type`, `claude_code.commit.count`, `claude_code.pull_request.count`, and `claude_code.active_time.total` by `type`.
- `claude-code-usage-metrics-for-coverage`(approved): The endpoint stores, per session, the totals of `claude_code.token.usage` by `type`, `model`, and `query_source`, and of `claude_code.cost.usage` by `model` and `query_source`, only to measure how much of each session's usage reached Neo.Tax as turns.
- `claude-code-other-metrics-dropped`(approved): Every other Claude Code metric, including `claude_code.session.count` and `claude_code.code_edit_tool.decision`, is dropped and is not counted as rejected.
- `delta-accepted`(approved): A claimed metric is accepted as a monotonic sum in delta temporality. A claimed metric of any other type or temporality is rejected.
- `metric-point-idempotent`(approved): A re-exported delta data point adds nothing: it is keyed by its series, start time, and end time.
- `claude-code-metrics-fixture`(approved): The Claude Code metric mapping is specified by a fixture: a captured Claude Code OTLP/JSON metric export in delta temporality, paired with the session totals it must produce, and from v1.0 the same export in cumulative temporality.

## Requirements (v1.0)

v1.0 takes what a gateway or a customer's own OpenTelemetry Collector sends: protobuf, cumulative metrics, the response id, and records with no call id or no session id.

- `protobuf-accepted`(approved): An authenticated request whose content type is `application/x-protobuf` is decoded as OTLP protobuf, to the same records OTLP/JSON decodes to, before any mapping runs.
- `join-on-response-id`(approved): Two turns in one organization with the same non-null response id are one turn, counted once, whichever paths or sources they arrived through.
- `join-on-fingerprint`(draft): A telemetry turn with neither call id joins another turn in its organization only when that turn is the only one matching it on model and on every token count, within five seconds of its timestamp, with the same engineer when both have one, and it is the only telemetry turn matching that turn; a tie joins nothing.
- `cumulative-accepted`(approved): A claimed metric is also accepted as a monotonic sum in cumulative temporality; a cumulative series (one metric, session, set of kept attributes, and start time) is stored as the differences between its points, and a point whose start time changes or whose value falls starts the series over.
- `cumulative-point-idempotent`(approved): A re-exported cumulative data point adds nothing: it is keyed by its series and end time.
- `metric-without-session`(draft): A data point without `session.id`, sent when the exporter turns session ids off, is stored under its engineer and its time interval with no session.
- `metric-usage-fallback`(draft): A session with metric usage and no turn from either path takes its usage from its metrics, labeled as metric-derived and never joined to a turn.

## Design Notes

OTLP is the entry point because it is what keeps a capture path swappable. Nearly every gateway and observability stack exports it, and Claude Code exports it natively as a supported interface, unlike the transcript the plugin parses. It also reaches places the plugin does not load: managed settings turn it on in Claude Code's cloud sessions, which have no laptop and no hooks. OTLP fixes the transport, not the attribute names, and the GenAI semantic conventions are still experimental, so each source needs its own mapping onto the session record. Adding a source means adding a mapping, not building another endpoint.

The plugin's allowlist runs on the engineer's machine, so it says nothing about what arrives here: the customer's admin configures the exporter, and an exporter can be told to send prompts, tool inputs, and whole API bodies. The server therefore enforces its own allowlist, named by the mapping, and drops the rest before anything is written. A customer who turns on prompt logging for their own collector sends us nothing more than one who does not.

The credential is per organization, not per device, because an exporter is configured once for a fleet through managed settings and cannot run enrollment. A static header is the only credential every exporter supports. Managed settings cache it on every machine, which is acceptable only because the key can do nothing but write telemetry into its own organization and can be revoked without touching anything else. One key per destination (dogfood telemetry, a gateway, a customer's own collector) lets one leak be revoked alone.

A telemetry record identifies the engineer directly, through the harness's sign-in email, where a session event identifies only its device. That email is the harness vendor's account, not one the engineer confirmed to Neo.Tax, so it is resolved through the same contributor matcher and is no more trusted than a device's email.

Turns join on call ids because they are the model provider's own ids for one API call, so every path that saw the call carries the same value: the plugin reads them from the transcript, Claude Code's telemetry from the response, and a gateway from the response it relayed. The response id comes first because it is in the response body, which a gateway must pass through intact; the request id is a response header, which a gateway may replace with its own (OpenRouter reports the provider's only in its Cost metadata), and some gateways, Kong's AI Gateway among them, export the response id and not the request id. The request id is the second key because Claude Code's telemetry carries it and not the response id. A source that carries neither, such as a gateway that drops both, can still join on a fingerprint of model, token counts, and time, but only when the match is unique, because a wrong join would move one engineer's usage into another's session. Recording which key joined each turn turns the strength of the join into a measured rate rather than an assumption. A session id would not do. Claude Code replaces `session.id` on `/clear`, and a session id names a whole session, not a turn. A request id names one API call across every session, so the join and the telemetry idempotency key are the organization and a call id alone, and a joined turn belongs to the plugin's session, which its `session.opened` and `session.pr_opened` events also name. Plugin turns that repeat a call id collapse to the first, so the key stays one-to-one. Joining on call ids lets each path supply what it is good at. Telemetry has the supported usage and Claude Code's cost estimate. The plugin has the branch and head SHA per turn and the pull requests opened. Running both is what lets v0.1 measure how often sessions match a pull request with the plugin's branch and without it.

Telemetry reports every API call, including the side calls (session title, recap, compaction summary) that the plugin can see only as session totals on `session.closed`. Adding both would count side calls twice, so when telemetry covered every turn the plugin saw, the plugin's `side_usage` stops contributing to that session's usage.

Metrics are ingested from v0.1, beside the logs, for what the logs do not carry. `claude_code.active_time.total` is the engineer's own time in a session, split into time at the keyboard and time spent waiting on tools and the model, which is the engineering-time half of what Neo.Tax measures. Lines changed, commits, and pull requests created are counts the plugin sees only on a laptop, and metrics report them from cloud sessions too, though without the pull request's number, so `pr-opened-plugin-only` still governs which pull requests a session opened. Metrics also carry token and cost totals, but they are aggregates over an export interval with no call ids, so they cannot be joined to a turn, and adding them to the turns from `claude_code.api_request` would count every call twice. They are kept only as a check: a session whose metric totals exceed its turns has lost log records, which the rate of that gap per source makes visible. Claude Code exports metrics as deltas by default, every 60 seconds, so v0.1 accepts deltas only. A customer's collector may convert them to cumulative, so v1.0 accepts that too and stores differences either way.

The plugin keeps its own endpoint and contract rather than sending OTLP to this one. OTLP is the right format for sources Neo.Tax does not control, because adopting theirs is the only option. For a source it writes, a typed contract is stronger: `session.opened`, `session.pr_opened`, `side_usage`, and `dropped_events` have no OTLP equivalent and would become attributes held in place by convention; the JSON Schema is what lets a security team see exactly what leaves a laptop, where an OTLP attribute bag promises only whatever the server's allowlist drops; and the agent's retry and drop logic depends on the plugin ingest response naming each rejected event, which OTLP's partial success cannot. The two paths converge after decoding instead, on the same turn record and the same join.

v0.1 takes OTLP/JSON only, because it is what Claude Code sends when managed settings ask for it, and v0.1's only source is Claude Code. v1.0 takes both of OTLP/HTTP's encodings because a gateway's choice is not ours to make: protobuf is OTLP's default: the OpenTelemetry Collector's exporter, most SDKs, and Kong's AI Gateway send nothing else, and a customer's existing collector is the usual way a gateway's telemetry would reach us. Both encodings are the same schema, so they decode to the same records and the mapping, allowlist, and join never see the difference.

The endpoint answers rejected records through OTLP's own partial success message rather than a Neo.Tax shape, so an unmodified exporter handles the response correctly. OTLP's response carries a count and one message, not a per-record index the way the plugin's ingest response does.

## Alternatives Considered

| Option | Pros | Cons |
| --- | --- | --- |
| **OTLP endpoint beside the plugin's (chosen)** | Any OTLP exporter or gateway connects with configuration only; reaches cloud sessions; uses a supported Claude Code interface | A second credential kind and a second allowlist to keep honest; a mapping per source |
| Plugin only | One credential, one allowlist, one contract | Transcript usage depends on an unsupported format; no cloud sessions; every gateway needs custom work |
| Route telemetry through the agent | The agent's allowlist would apply | The agent does not run in cloud sessions or gateways, which are the reason for this path |
| Device key on the exporter | Reuses the existing credential | An exporter cannot enroll, and one shared device key breaks the one-engineer-per-key rule |
| JSON only (v0.1's choice, not v1.0's) | One decoder; requests are readable in logs | Refuses OTLP's default encoding, so the Collector, most SDKs, and Kong's AI Gateway cannot connect without reconfiguration they may not offer |
| Plugin sends OTLP instead of its own contract | One endpoint for every source; a customer could route plugin data through their own OpenTelemetry Collector | Session opens, pull requests, side usage, and dropped events become convention-only attributes; loses the JSON Schema as the published privacy statement; OTLP partial success names no rejected event, which the agent's retry needs; rewrites a built, tested path for no visible gain. Revisit, as an added transport beside the contract, if a customer requires all egress through their own Collector |
| Usage from metrics instead of logs | One signal; metrics are aggregated, so far fewer records | No call ids, so no join with the plugin's turns; per-interval totals lose per-call cache and tier detail; adding them to the logs' turns double-counts |
| Join on request id only | One key, exact | Misses every source that carries the response id and not the request id, which includes gateways that replace the request id |

## Risks

- **A source renames an attribute.** Its turns lose usage or are rejected without the exporter noticing. Mitigated by `partialSuccess` counts and a rejected-record rate watched per source.
- **The ingest key leaks from a managed machine.** Anyone holding it can write false telemetry into that organization. Accepted for a write-only key; revoke and remint for that destination.
- **The plugin and telemetry disagree on a joined turn's usage.** Telemetry's counts are taken silently. Accepted; the difference between the two is itself a measure of the transcript parser's accuracy.
- **Telemetry covers only part of a session.** An exporter turned on mid-session, or records lost in transit, leave some turns plugin-only, so that session adds the plugin's whole `side_usage` on top of the side calls telemetry did report. Accepted as a small overcount on partly covered sessions, which the rate of plugin-only turns per session makes visible.
- **A fingerprint joins two different calls.** One engineer's usage lands in another call's turn. Mitigated by requiring a unique match on every token count within five seconds, and made visible by `join-key-recorded`; if the fingerprint's share of joins is high for a source, that source needs a call id instead.
- **A compressed body expands without bound.** A small gzip request could exhaust memory. Mitigated by the decompressed size limit in `gzip-accepted`.
- **Metrics are mistaken for usage.** A report that sums metric token or cost totals with turns double-counts every call. Mitigated by `metrics-never-turns` and by storing metric totals apart from usage; `metric-usage-fallback` stays draft until the cases it covers are measured.
- **A collector resets a cumulative series without changing its start time.** The next difference is negative or understated. Mitigated by treating a falling value as a reset in `cumulative-accepted`.

## Open Questions

- Whether Claude Code's `session.id` always equals the `session_id` its hooks receive, so a telemetry-only turn and the plugin's `session.opened` name the same session.
- Whether a forked Claude Code session re-sends its parent's turns under the new session id with the same request ids, which `one-turn-per-call-id` assumes it may.
- What a turn without a call id should be keyed by, and whether such turns occur on the Anthropic API at all.
- How to group a source's telemetry-only turns into sessions when it sends no session id (Kong's spans carry none): by trace id, or by ingest key, engineer, and an idle gap.
- Whether Claude Code's telemetry can carry the response id, which would let its turns join on the stronger key.
- Whether a session's metrics name the same session id as its logs on a cloud session, where Claude Code also sends `ccr.session.id`.
- Whether `claude_code.code_edit_tool.decision` (edits accepted and rejected) is worth keeping as a measure of how much of an agent's output an engineer used.
- Whether a metric session with no turns from either path happens often enough to approve `metric-usage-fallback`.
- Whether the privacy statement should describe this path, since what telemetry sends is set by the customer's admin, not by the plugin.
