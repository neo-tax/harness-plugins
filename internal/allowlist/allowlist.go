// Package allowlist holds the single list of fields the agent may emit, keyed
// by event kind, and enforces it before any event is queued. The published
// privacy statement is generated from this list.
//
// Adding or removing a field here updates docs/specs/architectural/field-allowlist.md
// in the same change.
//
// @spec: field-allowlist#one-allowlist
// @spec: field-allowlist#change-updates-this-spec
// @spec: field-allowlist#cwd-excluded
// @spec: repo-and-branch-identity#cwd-never-sent
package allowlist

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/neo-tax/harness-plugins/contract"
)

// Field is one permitted field and the one-line description the privacy
// statement publishes for it. Children, when set, is the allowlist for the
// fields of an object-valued field, or of each object in a list when List
// is set.
type Field struct {
	Name        string
	Description string
	Children    []Field
	List        bool
}

var envelope = []Field{
	{"contract_version", "Version of the wire contract the event conforms to.", nil, false},
	{"device_id", "Random id minted for this device at enrollment.", nil, false},
	{"harness", "Which coding harness ran the session, for example claude-code.", nil, false},
	{"harness_version", "Version of the harness, as recorded in its transcript.", nil, false},
	{"agent_version", "Version of the neo-tax-agent binary.", nil, false},
	{"session_id", "The harness's own random id for the session.", nil, false},
	{"event_kind", "Which kind of session event this is.", nil, false},
	{"sent_at", "When the agent created the event.", nil, false},
}

var repo = Field{"repo", "Repository host, owner, and name parsed from the git remote URL, lowercased; null outside a git repository.", []Field{
	{"host", "Git host, for example github.com.", nil, false},
	{"owner", "Organization or user that owns the repository.", nil, false},
	{"name", "Repository name.", nil, false},
}, false}

var branch = Field{"branch", "Current git branch name; null on a detached HEAD or outside git.", nil, false}
var headSHA = Field{"head_sha", "Commit hash HEAD pointed at.", nil, false}

// @spec: field-allowlist#git-user-email-included
// @spec: field-allowlist#request-id-included
var byKind = map[string][]Field{
	contract.KindOpened: {
		{"started_at", "When the session started.", nil, false},
		{"entrypoint", "How the harness was launched, for example cli or sdk.", nil, false},
		{"source", "Why the session started: startup, resume, clear, or compact.", nil, false},
		repo, branch, headSHA,
		{"git_user_email", "The git user.email in effect for the repository (its own config, else the engineer's global git config), used only to join sessions to pull-request authors.", nil, false},
		{"dropped_events", "How many events the agent discarded because its local queue was full.", nil, false},
	},
	contract.KindTurn: {
		{"message_id", "The harness's id for the assistant message.", nil, false},
		{"request_id", "The model vendor's API request id for the message.", nil, false},
		{"subagent_id", "Id of the subagent that produced the turn; null on the main thread.", nil, false},
		{"timestamp", "When the assistant message was recorded.", nil, false},
		{"model", "Model name that produced the message.", nil, false},
		branch, headSHA,
		{"usage", "Token counts for the message.", []Field{
			{"input", "Uncached input tokens.", nil, false},
			{"output", "Output tokens.", nil, false},
			{"cache_write_5m", "Input tokens written to the five-minute prompt cache.", nil, false},
			{"cache_write_1h", "Input tokens written to the one-hour prompt cache.", nil, false},
			{"cache_read", "Input tokens read from the prompt cache.", nil, false},
			{"service_tier", "Vendor service tier the request ran on.", nil, false},
			{"source", "Where the counts came from: payload, transcript, or unknown.", nil, false},
		}, false},
	},
	contract.KindPROpened: {
		repo,
		{"number", "Pull request number.", nil, false},
		{"timestamp", "When the pull request was recorded as opened.", nil, false},
	},
	contract.KindClosed: {
		{"ended_at", "When the session ended.", nil, false},
		{"reason", "Why the harness ended the session, for example logout or exit.", nil, false},
		{"dropped_events", "How many events the agent discarded because its local queue was full.", nil, false},
		// @spec: field-allowlist#side-usage-included
		{"side_usage", "Token counts, one entry per model, of the session's model calls that produced no turn of their own (the session title, the recap after time away, compaction's summary); null when the harness recorded no session totals.", []Field{
			{"model", "Model name the calls ran on.", nil, false},
			{"input", "Uncached input tokens.", nil, false},
			{"output", "Output tokens.", nil, false},
			{"cache_write_5m", "Input tokens written to the five-minute prompt cache.", nil, false},
			{"cache_write_1h", "Input tokens written to the one-hour prompt cache.", nil, false},
			{"cache_read", "Input tokens read from the prompt cache.", nil, false},
		}, true},
	},
}

// Kinds returns every event kind the allowlist covers, sorted.
func Kinds() []string {
	out := make([]string, 0, len(byKind))
	for k := range byKind {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// For returns the fields an event kind may carry, envelope first.
func For(kind string) ([]Field, bool) {
	fields, ok := byKind[kind]
	if !ok {
		return nil, false
	}
	return append(append([]Field{}, envelope...), fields...), true
}

// Apply returns a copy of ev holding only allowlisted fields, and the dotted
// names of every field it removed. It never returns a removed value. An event
// of an unknown kind is an error and must not be queued. ev holds values as
// encoding/json decodes them (maps, []any, scalars); a value whose shape the
// allowlist does not describe is dropped, not passed through.
//
// @spec: field-allowlist#unknown-field-dropped-and-counted
func Apply(ev map[string]any) (map[string]any, []string, error) {
	kind, _ := ev["event_kind"].(string)
	fields, ok := For(kind)
	if !ok {
		return nil, nil, fmt.Errorf("allowlist: unknown event kind %q", kind)
	}
	var dropped []string
	out := filter(ev, fields, "", &dropped)
	return out, dropped, nil
}

// RedactedName stands in for a dropped field whose name is not a plain
// identifier, so diagnostics never record a key that could be content.
const RedactedName = "<redacted>"

var plainName = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func report(dropped *[]string, prefix, key string) {
	if !plainName.MatchString(key) {
		key = RedactedName
		prefix = ""
	}
	*dropped = append(*dropped, prefix+key)
}

func filter(in map[string]any, fields []Field, prefix string, dropped *[]string) map[string]any {
	allowed := make(map[string]Field, len(fields))
	for _, f := range fields {
		allowed[f.Name] = f
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		f, ok := allowed[k]
		if !ok {
			report(dropped, prefix, k)
			continue
		}
		switch val := v.(type) {
		case nil:
		case []any:
			if !f.List {
				report(dropped, prefix, k)
				continue
			}
			items := make([]any, 0, len(val))
			for _, item := range val {
				obj, ok := item.(map[string]any)
				if !ok {
					// k is an allowlisted name, so it is safe to report.
					*dropped = append(*dropped, prefix+k+"[]")
					continue
				}
				items = append(items, filter(obj, f.Children, prefix+k+"[].", dropped))
			}
			v = items
		case map[string]any:
			if len(f.Children) == 0 || f.List {
				report(dropped, prefix, k)
				continue
			}
			v = filter(val, f.Children, prefix+k+".", dropped)
		case string, bool, float64, float32, json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			if len(f.Children) > 0 || f.List {
				report(dropped, prefix, k)
				continue
			}
		default:
			report(dropped, prefix, k)
			continue
		}
		out[k] = v
	}
	return out
}
