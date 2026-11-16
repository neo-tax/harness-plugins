package contract

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Event kinds.
const (
	KindOpened   = "session.opened"
	KindTurn     = "session.turn"
	KindPRLinked = "session.pr_linked"
	KindClosed   = "session.closed"
)

// Usage sources.
const (
	UsageSourcePayload    = "payload"
	UsageSourceTranscript = "transcript"
	UsageSourceUnknown    = "unknown"
)

// email_confirmed_by values.
const (
	EmailConfirmedByEngineer = "engineer"
	EmailConfirmedByAuto     = "auto"
)

// Envelope is carried by every session event. event_kind is set by the
// constructor of each kind.
type Envelope struct {
	ContractVersion string    `json:"contract_version"`
	DeviceID        string    `json:"device_id"`
	Harness         string    `json:"harness"`
	HarnessVersion  *string   `json:"harness_version"`
	AgentVersion    string    `json:"agent_version"`
	SessionID       string    `json:"session_id"`
	SentAt          time.Time `json:"sent_at"`
}

// Repo is a normalized repository identity; a nil *Repo marshals as null.
type Repo struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// Usage is the token counts and pricing attributes of one turn.
type Usage struct {
	Input        *int64  `json:"input"`
	Output       *int64  `json:"output"`
	CacheWrite5m *int64  `json:"cache_write_5m"`
	CacheWrite1h *int64  `json:"cache_write_1h"`
	CacheRead    *int64  `json:"cache_read"`
	ServiceTier  *string `json:"service_tier"`
	Speed        *string `json:"speed"`
	Source       string  `json:"source"`
}

// UnknownUsage is the usage of a turn whose counts are not available.
func UnknownUsage() Usage { return Usage{Source: UsageSourceUnknown} }

type OpenedFields struct {
	StartedAt     time.Time `json:"started_at"`
	Entrypoint    *string   `json:"entrypoint"`
	Source        *string   `json:"source"`
	Repo          *Repo     `json:"repo"`
	Branch        *string   `json:"branch"`
	HeadSHA       *string   `json:"head_sha"`
	GitUserEmail  *string   `json:"git_user_email"`
	DroppedEvents int64     `json:"dropped_events"`
}

type TurnFields struct {
	MessageID  string    `json:"message_id"`
	RequestID  *string   `json:"request_id"`
	SubagentID *string   `json:"subagent_id"`
	Timestamp  time.Time `json:"timestamp"`
	Model      *string   `json:"model"`
	Branch     *string   `json:"branch"`
	HeadSHA    *string   `json:"head_sha"`
	Usage      Usage     `json:"usage"`
}

type PRLinkedFields struct {
	Repo        *Repo     `json:"repo"`
	Number      int64     `json:"number"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}

type ClosedFields struct {
	EndedAt       time.Time `json:"ended_at"`
	Reason        *string   `json:"reason"`
	DroppedEvents int64     `json:"dropped_events"`
	// SideUsage is nil (null) when the harness recorded no session totals,
	// and empty when it did and every call produced a turn.
	SideUsage []SideUsage `json:"side_usage"`
}

// SideUsage is one model's usage across the session's API calls that
// produced no turn.
type SideUsage struct {
	Model        string `json:"model"`
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheWrite5m int64  `json:"cache_write_5m"`
	CacheWrite1h int64  `json:"cache_write_1h"`
	CacheRead    int64  `json:"cache_read"`
}

type Opened struct {
	Envelope
	EventKind string `json:"event_kind"`
	OpenedFields
}

type Turn struct {
	Envelope
	EventKind string `json:"event_kind"`
	TurnFields
}

type PRLinked struct {
	Envelope
	EventKind string `json:"event_kind"`
	PRLinkedFields
}

type Closed struct {
	Envelope
	EventKind string `json:"event_kind"`
	ClosedFields
}

func NewOpened(e Envelope, f OpenedFields) Opened       { return Opened{e, KindOpened, f} }
func NewTurn(e Envelope, f TurnFields) Turn             { return Turn{e, KindTurn, f} }
func NewPRLinked(e Envelope, f PRLinkedFields) PRLinked { return PRLinked{e, KindPRLinked, f} }
func NewClosed(e Envelope, f ClosedFields) Closed       { return Closed{e, KindClosed, f} }

// IngestRequest is a batch of session events, oldest first.
type IngestRequest struct {
	ContractVersion string            `json:"contract_version"`
	Events          []json.RawMessage `json:"events"`
}

// IngestResponse is the server's account of a batch.
type IngestResponse struct {
	Accepted   int         `json:"accepted"`
	Duplicates int         `json:"duplicates"`
	Rejected   []Rejection `json:"rejected"`
}

// MarshalJSON writes a nil Rejected as an empty array, as the schema requires.
func (r IngestResponse) MarshalJSON() ([]byte, error) {
	type plain IngestResponse
	if r.Rejected == nil {
		r.Rejected = []Rejection{}
	}
	return json.Marshal(plain(r))
}

// Rejection names one event rejected for schema reasons by its batch index.
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// ErrorResponse is the body of a non-2xx response.
type ErrorResponse struct {
	Error            string   `json:"error"`
	AcceptedVersions []string `json:"accepted_versions,omitempty"`
}

type DeviceDescription struct {
	Hostname       string  `json:"hostname"`
	OS             string  `json:"os"`
	Arch           string  `json:"arch"`
	Harness        string  `json:"harness"`
	HarnessVersion *string `json:"harness_version"`
	AgentVersion   string  `json:"agent_version"`
}

type EnrollmentRequest struct {
	ContractVersion  string            `json:"contract_version"`
	EnrollmentToken  string            `json:"enrollment_token"`
	Email            string            `json:"email"`
	EmailConfirmedBy string            `json:"email_confirmed_by"`
	Device           DeviceDescription `json:"device"`
}

type EnrollmentResponse struct {
	DeviceID      string `json:"device_id"`
	DeviceKey     string `json:"device_key"`
	IngestBaseURL string `json:"ingest_base_url"`
}

// SchemaFields returns, for each event kind, the sorted names of every field
// the session event schema permits that kind to carry: the envelope's
// properties plus the kind's own.
func SchemaFields() map[string][]string {
	b, err := schemaFS.ReadFile(SessionEventSchema)
	if err != nil {
		panic(err)
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Properties map[string]struct {
				Const string `json:"const"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(err)
	}
	out := map[string][]string{}
	for _, def := range doc.Defs {
		kind := def.Properties["event_kind"].Const
		if !strings.HasPrefix(kind, "session.") {
			continue
		}
		set := map[string]bool{}
		for f := range doc.Properties {
			set[f] = true
		}
		for f := range def.Properties {
			set[f] = true
		}
		fields := make([]string, 0, len(set))
		for f := range set {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		out[kind] = fields
	}
	return out
}

// ObjectFields returns the sorted property names of the object the session
// event schema permits at path on events of kind, or of each object in a
// list-valued field, following $ref and looking through a nullable oneOf. path
// is a field name, or a dotted path such as "usage.cache" for an object nested
// inside an object field (a list is stepped through without a [] suffix). It
// returns nil when the path holds no objects.
//
// @spec: field-allowlist#allowlist-matches-contract
func ObjectFields(kind, path string) []string {
	prop, _ := schemaProperty(kind, path)
	var out []string
	for p := range prop.Properties {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// IsList reports whether the field at path on an event kind is a list per the
// schema. path is dotted as for ObjectFields.
func IsList(kind, path string) bool {
	_, list := schemaProperty(kind, path)
	return list
}

type schemaNode struct {
	Ref        string                `json:"$ref"`
	Const      string                `json:"const"`
	Type       any                   `json:"type"`
	Properties map[string]schemaNode `json:"properties"`
	OneOf      []schemaNode          `json:"oneOf"`
	Items      *schemaNode           `json:"items"`
}

// schemaProperty resolves the field at a dotted path on an event kind to the
// object schema it holds (directly, or as the items of a list) and whether the
// last segment is a list.
func schemaProperty(kind, path string) (schemaNode, bool) {
	b, err := schemaFS.ReadFile(SessionEventSchema)
	if err != nil {
		panic(err)
	}
	var doc struct {
		schemaNode
		Defs map[string]schemaNode `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(err)
	}
	resolve := func(n schemaNode) schemaNode {
		for n.Ref != "" {
			n = doc.Defs[strings.TrimPrefix(n.Ref, "#/$defs/")]
		}
		return n
	}
	// object resolves a property to the object it holds, directly or as the
	// items of a list, looking through a nullable oneOf.
	object := func(prop schemaNode) (schemaNode, bool) {
		prop = resolve(prop)
		alts := append([]schemaNode{prop}, prop.OneOf...)
		var out schemaNode
		list := false
		for _, alt := range alts {
			alt = resolve(alt)
			if alt.Items != nil {
				alt, list = resolve(*alt.Items), true
			}
			if len(alt.Properties) > 0 {
				out = alt
			}
		}
		return out, list
	}
	segs := strings.Split(path, ".")
	prop, ok := doc.Properties[segs[0]]
	if !ok {
		for _, alt := range doc.OneOf {
			def := resolve(alt)
			if def.Properties["event_kind"].Const == kind {
				prop, ok = def.Properties[segs[0]]
			}
		}
	}
	if !ok {
		return schemaNode{}, false
	}
	out, list := object(prop)
	for _, seg := range segs[1:] {
		if prop, ok = out.Properties[seg]; !ok {
			return schemaNode{}, false
		}
		out, list = object(prop)
	}
	return out, list
}
