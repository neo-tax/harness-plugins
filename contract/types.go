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
	KindPROpened = "session.pr_opened"
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

type PROpenedFields struct {
	Repo      *Repo     `json:"repo"`
	Number    int64     `json:"number"`
	Timestamp time.Time `json:"timestamp"`
}

type ClosedFields struct {
	EndedAt       time.Time `json:"ended_at"`
	Reason        *string   `json:"reason"`
	DroppedEvents int64     `json:"dropped_events"`
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

type PROpened struct {
	Envelope
	EventKind string `json:"event_kind"`
	PROpenedFields
}

type Closed struct {
	Envelope
	EventKind string `json:"event_kind"`
	ClosedFields
}

func NewOpened(e Envelope, f OpenedFields) Opened       { return Opened{e, KindOpened, f} }
func NewTurn(e Envelope, f TurnFields) Turn             { return Turn{e, KindTurn, f} }
func NewPROpened(e Envelope, f PROpenedFields) PROpened { return PROpened{e, KindPROpened, f} }
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
// event schema permits as field on events of kind, following $ref and looking
// through a nullable oneOf. It returns nil when the field is not an object.
func ObjectFields(kind, field string) []string {
	b, err := schemaFS.ReadFile(SessionEventSchema)
	if err != nil {
		panic(err)
	}
	type node struct {
		Ref        string          `json:"$ref"`
		Const      string          `json:"const"`
		Properties map[string]node `json:"properties"`
		OneOf      []node          `json:"oneOf"`
	}
	var doc struct {
		node
		Defs map[string]node `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(err)
	}
	resolve := func(n node) node {
		for n.Ref != "" {
			n = doc.Defs[strings.TrimPrefix(n.Ref, "#/$defs/")]
		}
		return n
	}
	prop, ok := doc.Properties[field]
	if !ok {
		for _, alt := range doc.OneOf {
			def := resolve(alt)
			if def.Properties["event_kind"].Const == kind {
				prop, ok = def.Properties[field]
			}
		}
	}
	if !ok {
		return nil
	}
	prop = resolve(prop)
	props := prop.Properties
	for _, alt := range prop.OneOf {
		if alt := resolve(alt); len(alt.Properties) > 0 {
			props = alt.Properties
		}
	}
	var out []string
	for p := range props {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
