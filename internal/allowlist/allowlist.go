// Package allowlist holds the single list of fields the agent may emit, keyed
// by event kind, and enforces it before any event is queued. The published
// privacy statement is generated from this list.
//
// Adding or removing a field here updates docs/specs/architectural/field-allowlist.md
// in the same change.
//
// @spec: field-allowlist#one-allowlist
// @spec: field-allowlist#change-updates-this-spec
package allowlist

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/neo-tax/harness-plugins/contract"
)

// Field is one permitted field and the one-line description the privacy
// statement publishes for it. Children, when set, is the allowlist for the
// fields of an object-valued field.
type Field struct {
	Name        string
	Description string
	Children    []Field
}

var envelope = []Field{
	{"contract_version", "Version of the wire contract the event conforms to.", nil},
	{"device_id", "Random id minted for this device at enrollment.", nil},
	{"harness", "Which coding harness ran the session, for example claude-code.", nil},
	{"harness_version", "Version of the harness, as recorded in its transcript.", nil},
	{"agent_version", "Version of the neo-tax-agent binary.", nil},
	{"session_id", "The harness's own random id for the session.", nil},
	{"event_kind", "Which kind of session event this is.", nil},
	{"sent_at", "When the agent created the event.", nil},
}

var repo = Field{"repo", "Repository host, owner, and name parsed from the git remote URL, lowercased; null outside a git repository.", []Field{
	{"host", "Git host, for example github.com.", nil},
	{"owner", "Organization or user that owns the repository.", nil},
	{"name", "Repository name.", nil},
}}

var branch = Field{"branch", "Current git branch name; null on a detached HEAD or outside git.", nil}
var headSHA = Field{"head_sha", "Commit hash HEAD pointed at.", nil}

// @spec: field-allowlist#git-user-email-included
// @spec: field-allowlist#request-id-included
var byKind = map[string][]Field{
	contract.KindOpened: {
		{"started_at", "When the session started.", nil},
		{"entrypoint", "How the harness was launched, for example cli or sdk.", nil},
		{"source", "Why the session started: startup, resume, clear, or compact.", nil},
		repo, branch, headSHA,
		{"git_user_email", "The repository-level git user.email, used only to join sessions to pull-request authors.", nil},
		{"dropped_events", "How many events the agent discarded because its local queue was full.", nil},
	},
	contract.KindTurn: {
		{"message_id", "The harness's id for the assistant message.", nil},
		{"request_id", "The model vendor's API request id for the message.", nil},
		{"subagent_id", "Id of the subagent that produced the turn; null on the main thread.", nil},
		{"timestamp", "When the assistant message was recorded.", nil},
		{"model", "Model name that produced the message.", nil},
		branch, headSHA,
		{"usage", "Token counts for the message.", []Field{
			{"input", "Uncached input tokens.", nil},
			{"output", "Output tokens.", nil},
			{"cache_write_5m", "Input tokens written to the five-minute prompt cache.", nil},
			{"cache_write_1h", "Input tokens written to the one-hour prompt cache.", nil},
			{"cache_read", "Input tokens read from the prompt cache.", nil},
			{"service_tier", "Vendor service tier the request ran on.", nil},
			{"source", "Where the counts came from: payload, transcript, or unknown.", nil},
		}},
	},
	contract.KindPROpened: {
		repo,
		{"number", "Pull request number.", nil},
		{"timestamp", "When the pull request was recorded as opened.", nil},
	},
	contract.KindClosed: {
		{"ended_at", "When the session ended.", nil},
		{"reason", "Why the harness ended the session, for example logout or exit.", nil},
		{"dropped_events", "How many events the agent discarded because its local queue was full.", nil},
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
		case map[string]any:
			if len(f.Children) == 0 {
				report(dropped, prefix, k)
				continue
			}
			v = filter(val, f.Children, prefix+k+".", dropped)
		case []any:
			report(dropped, prefix, k)
			continue
		default:
			if len(f.Children) > 0 {
				report(dropped, prefix, k)
				continue
			}
		}
		out[k] = v
	}
	return out
}
