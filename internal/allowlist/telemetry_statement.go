package allowlist

import (
	"fmt"
	"strings"
)

// TelemetryHeading opens the statement's section on Claude Code's own
// telemetry, the one part of the statement not generated from this
// repository's allowlist or contract.
const TelemetryHeading = "## Claude Code's own telemetry"

// TelemetryAttribute is one attribute, or one metric, that Neo.Tax's OTLP
// endpoint keeps from Claude Code's telemetry export.
type TelemetryAttribute struct {
	Name        string
	Description string
}

// TelemetryLogAttributes are the attributes the endpoint keeps from a Claude
// Code `claude_code.api_request` log record. The endpoint, and the mapping and
// allowlist that decide this list, live in platform-new
// (docs/specs/architectural/platform/otlp-ingest.spec.md: `server-allowlist`,
// `claude-code-turn-mapping`, `claude-code-repo-mapping`), so this list is
// kept by hand and must change with them.
var TelemetryLogAttributes = []TelemetryAttribute{
	{"session.id", "Claude Code's id for the session, the same id the plugin sends as `session_id`."},
	{"user.email", "Email of the Claude account the engineer is signed in to Claude Code with."},
	{"model", "Model name the call ran on."},
	{"input_tokens", "Uncached input tokens."},
	{"output_tokens", "Output tokens."},
	{"cache_read_tokens", "Input tokens read from the prompt cache."},
	{"cache_creation_5m_tokens", "Input tokens written to the five-minute prompt cache."},
	{"cache_creation_1h_tokens", "Input tokens written to the one-hour prompt cache."},
	{"cache_creation_tokens", "Input tokens written to the prompt cache, used only when Claude Code does not split them by cache duration."},
	{"speed", "Speed the call ran at, standard or fast, which sets its price per token."},
	{"cost_usd", "Claude Code's own estimate of the call's cost in US dollars."},
	{"request_id", "The model vendor's API request id for the call."},
	{"event.timestamp", "When Claude Code recorded the call; the record's own timestamps are used when it is missing."},
	{"vcs.repository.url.full", "URL of the repository's git remote; only its host is kept."},
	{"vcs.owner.name", "Organization or user that owns the repository."},
	{"vcs.repository.name", "Repository name."},
}

// TelemetryMetrics are the Claude Code metrics the endpoint keeps
// (otlp-ingest `claude-code-session-activity`,
// `claude-code-usage-metrics-for-coverage`, and `metric-usage-fallback`).
var TelemetryMetrics = []TelemetryAttribute{
	{"claude_code.lines_of_code.count", "Lines of code added and removed."},
	{"claude_code.commit.count", "Commits created."},
	{"claude_code.pull_request.count", "Pull requests created, without their numbers."},
	{"claude_code.active_time.total", "Time the engineer was active in the session, split into time at the keyboard and time waiting on tools and the model."},
	{"claude_code.token.usage", "Tokens used, by kind of token. Kept to check how much of a session's usage reached Neo.Tax as log records, and used as the session's usage, with its cache writes not split by cache duration, when no turn of the session reached Neo.Tax."},
	{"claude_code.cost.usage", "Claude Code's estimated cost, kept for the same check and the same fallback."},
}

// TelemetryMetricAttributes are the attributes the endpoint keeps on a kept
// metric's data points (otlp-ingest `metric-allowlist`).
var TelemetryMetricAttributes = []TelemetryAttribute{
	{"session.id", "Claude Code's id for the session."},
	{"user.email", "Email of the Claude account the engineer is signed in to Claude Code with."},
	{"model", "Model name, on token and cost metrics."},
	{"type", "Which kind of count: added or removed lines, keyboard or waiting time, or the kind of token."},
	{"query_source", "Which part of Claude Code made the calls, such as the main conversation or a subagent."},
	{"speed", "Speed the calls ran at."},
	{"vcs.repository.url.full", "URL of the repository's git remote; only its host is kept."},
	{"vcs.owner.name", "Organization or user that owns the repository."},
	{"vcs.repository.name", "Repository name."},
}

// telemetrySection is the statement's section on Claude Code's own telemetry.
//
// @spec: claude-code-telemetry-export#privacy-statement-covers-telemetry
func telemetrySection() string {
	var b strings.Builder
	b.WriteString("\n" + TelemetryHeading + `

This section applies only when an organization turns on Claude Code's telemetry export with the managed settings Neo.Tax provides. Claude Code then sends its own usage telemetry, as OpenTelemetry logs and metrics, to Neo.Tax directly. It does not pass through the neo-tax plugin, so the plugin's allowlist above does not apply to it, and this section is maintained by hand from the rules Neo.Tax's server applies rather than generated.

Those managed settings leave Claude Code's content logging off, so Claude Code redacts prompts, responses, and tool details before they leave the machine. Claude Code sends more attributes than Neo.Tax keeps, such as account and organization ids and the terminal type. Neo.Tax drops every attribute, resource attribute, and record body not listed below before anything is stored or logged. Prompt text, response text, tool inputs and outputs, commands, file paths, the names of tools, skills, and MCP servers, and API request and response bodies are dropped even when an administrator configures Claude Code to send them.

Unlike session events, telemetry identifies the engineer directly: it carries ` + "`user.email`" + `, the email of the Claude account the engineer signs in to Claude Code with.

### Log records

Neo.Tax keeps only ` + "`claude_code.api_request`" + ` records, one per model API call, and drops every other Claude Code event, including prompts, tool results and decisions, and API errors. Of each record it keeps:

| Attribute | Contents |
| --- | --- |
`)
	writeTelemetryRows(&b, TelemetryLogAttributes)
	b.WriteString(`
### Metrics

Neo.Tax keeps these metrics, each data point's value and its start and end times, and drops every other Claude Code metric:

| Metric | Contents |
| --- | --- |
`)
	writeTelemetryRows(&b, TelemetryMetrics)
	b.WriteString(`
Of each kept data point's attributes it keeps only these; agent, skill, plugin, and MCP server names, and exemplars, are dropped:

| Attribute | Contents |
| --- | --- |
`)
	writeTelemetryRows(&b, TelemetryMetricAttributes)
	return b.String()
}

func writeTelemetryRows(b *strings.Builder, rows []TelemetryAttribute) {
	for _, r := range rows {
		fmt.Fprintf(b, "| `%s` | %s |\n", r.Name, r.Description)
	}
}
