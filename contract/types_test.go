package contract_test

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

func ptr[T any](v T) *T { return &v }

var testEnvelope = contract.Envelope{
	ContractVersion: contract.Version,
	DeviceID:        "dev_123",
	Harness:         "claude-code",
	HarnessVersion:  ptr("2.1.310"),
	AgentVersion:    "0.1.0",
	SessionID:       "s-1",
	SentAt:          time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC),
}

func typedEvents() []any {
	sha := ptr("96e5e8f0000000000000000000000000000000aa")
	r := &contract.Repo{Host: "github.com", Owner: "neo-tax", Name: "harness-plugins"}
	ts := time.Date(2026, 10, 25, 16, 0, 1, 0, time.UTC)
	return []any{
		contract.NewOpened(testEnvelope, contract.OpenedFields{StartedAt: ts, Entrypoint: ptr("cli"), Source: ptr("startup"), Repo: r, Branch: ptr("main"), HeadSHA: sha, GitUserEmail: ptr("e@example.com")}),
		contract.NewTurn(testEnvelope, contract.TurnFields{MessageID: "msg_1", Timestamp: ts, Usage: contract.UnknownUsage()}),
		contract.NewTurn(testEnvelope, contract.TurnFields{MessageID: "msg_2", SubagentID: ptr("a1"), Timestamp: ts, Model: ptr("claude-opus-5-5"), Usage: contract.Usage{
			Input: ptr(int64(1)), Output: ptr(int64(2)), CacheWrite5m: ptr(int64(0)), CacheWrite1h: ptr(int64(3)), CacheRead: ptr(int64(4)), ServiceTier: ptr("standard"), Source: contract.UsageSourceTranscript,
		}}),
		contract.NewPROpened(testEnvelope, contract.PROpenedFields{Repo: r, Number: 7, Timestamp: ts}),
		contract.NewClosed(testEnvelope, contract.ClosedFields{EndedAt: ts, Reason: ptr("other"), DroppedEvents: 3}),
	}
}

// @spec: session-event-wire-contract#schema-is-source-of-truth
// @spec: test-and-release#contract-test
func TestTypedEventsValidateAgainstSchema(t *testing.T) {
	for _, ev := range typedEvents() {
		if err := contract.ValidateEvent(marshal(t, ev)); err != nil {
			t.Errorf("%T: %v", ev, err)
		}
	}
}

// The Go types must carry exactly the properties the schema defines for each
// event kind, so neither side can drift from the other.
// @spec: session-event-wire-contract#schema-is-source-of-truth
// @spec: session-event-wire-contract#opened-fields
// @spec: session-event-wire-contract#turn-fields
// @spec: session-event-wire-contract#pr-opened-fields
// @spec: session-event-wire-contract#closed-fields
func TestTypedFieldsMatchSchemaProperties(t *testing.T) {
	for kind, fields := range contract.SchemaFields() {
		var ev any
		for _, e := range typedEvents() {
			var m map[string]any
			json.Unmarshal(marshal(t, e), &m)
			if m["event_kind"] == kind {
				ev = m
			}
		}
		if ev == nil {
			t.Fatalf("no typed event for %s", kind)
		}
		got := keys(ev.(map[string]any))
		if !reflect.DeepEqual(got, fields) {
			t.Errorf("%s: typed fields %v, schema fields %v", kind, got, fields)
		}
	}
}

func TestSchemaFieldsCoverEveryKind(t *testing.T) {
	got := keys(anyMap(contract.SchemaFields()))
	want := []string{"session.closed", "session.opened", "session.pr_opened", "session.turn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds %v", got)
	}
	for kind, fields := range contract.SchemaFields() {
		for _, f := range []string{"contract_version", "device_id", "session_id", "event_kind", "sent_at"} {
			if !contains(fields, f) {
				t.Errorf("%s missing envelope field %s", kind, f)
			}
		}
	}
}

// @spec: session-event-wire-contract#four-schema-documents
func TestFourSchemaDocuments(t *testing.T) {
	names, _ := fs.Glob(contract.Schemas(), "*.schema.json")
	want := []string{"enrollment.schema.json", "ingest-request.schema.json", "ingest-response.schema.json", "session-event.schema.json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("schema documents %v", names)
	}
}

// @spec: session-event-wire-contract#enrollment-request
// @spec: session-event-wire-contract#enrollment-response
// @spec: session-event-wire-contract#ingest-response-per-event
func TestEnrollmentAndIngestShapes(t *testing.T) {
	req := contract.EnrollmentRequest{
		ContractVersion: contract.Version, EnrollmentToken: "et_1", Email: "e@example.com",
		EmailConfirmedBy: contract.EmailConfirmedByEngineer,
		Device:           contract.DeviceDescription{Hostname: "h", OS: "darwin", Arch: "arm64", Harness: "claude-code", HarnessVersion: ptr("2.1"), AgentVersion: "0.1.0"},
	}
	if err := contract.ValidateEnrollmentRequest(marshal(t, req)); err != nil {
		t.Error(err)
	}
	resp := contract.EnrollmentResponse{DeviceID: "d", DeviceKey: "k", IngestBaseURL: "https://ingest.example.com"}
	if err := contract.ValidateEnrollmentResponse(marshal(t, resp)); err != nil {
		t.Error(err)
	}
	ir := contract.IngestResponse{Accepted: 1, Duplicates: 0, Rejected: []contract.Rejection{{Index: 1, Reason: "bad"}}}
	if err := contract.ValidateIngestResponse(marshal(t, ir)); err != nil {
		t.Error(err)
	}
	ir.Rejected = nil
	if err := contract.ValidateIngestResponse(marshal(t, ir)); err != nil {
		t.Errorf("nil rejections must marshal as []: %v", err)
	}
	e := contract.ErrorResponse{Error: "unsupported contract_version", AcceptedVersions: contract.AcceptedVersions}
	if err := contract.ValidateIngestError(marshal(t, e)); err != nil {
		t.Error(err)
	}
}

// @spec: session-event-wire-contract#batched-ingest
func TestIngestRequestBatchBounds(t *testing.T) {
	ev := json.RawMessage(marshal(t, typedEvents()[0]))
	for n, ok := range map[int]bool{0: false, 1: true, 500: true, 501: false} {
		events := make([]json.RawMessage, n)
		for i := range events {
			events[i] = ev
		}
		err := contract.ValidateIngestRequest(marshal(t, contract.IngestRequest{ContractVersion: contract.Version, Events: events}))
		if (err == nil) != ok {
			t.Errorf("batch of %d: err=%v", n, err)
		}
	}
}

// @spec: session-event-wire-contract#email-confirmed-by-values
func TestEmailConfirmedByValues(t *testing.T) {
	if contract.EmailConfirmedByEngineer != "engineer" || contract.EmailConfirmedByAuto != "auto" {
		t.Fatal("unexpected email_confirmed_by values")
	}
	b := marshal(t, map[string]any{"contract_version": "1", "enrollment_token": "t", "email": "e@example.com", "email_confirmed_by": "admin",
		"device": map[string]any{"hostname": "h", "os": "o", "arch": "a", "harness": "h", "harness_version": nil, "agent_version": "v"}})
	if contract.ValidateEnrollmentRequest(b) == nil {
		t.Fatal("expected rejection of email_confirmed_by admin")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func anyMap(m map[string][]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if strings.EqualFold(x, y) {
			return true
		}
	}
	return false
}
