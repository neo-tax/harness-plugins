package fakeingest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/fakeingest"
)

type harness struct {
	t   *testing.T
	srv *httptest.Server
}

func start(t *testing.T, opts fakeingest.Options) *harness {
	t.Helper()
	srv := httptest.NewServer(fakeingest.New(opts))
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv}
}

func (h *harness) post(path, key string, body any) (*http.Response, []byte) {
	h.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (h *harness) enroll() contract.EnrollmentResponse {
	h.t.Helper()
	resp, body := h.post("/v1/enroll", "", contract.EnrollmentRequest{
		ContractVersion: contract.Version, EnrollmentToken: "et_test", Email: "e@example.com",
		EmailConfirmedBy: contract.EmailConfirmedByEngineer,
		Device:           contract.DeviceDescription{Hostname: "h", OS: "darwin", Arch: "arm64", Harness: "claude-code", AgentVersion: "0.1.0"},
	})
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("enroll: %d %s", resp.StatusCode, body)
	}
	if err := contract.ValidateEnrollmentResponse(body); err != nil {
		h.t.Fatal(err)
	}
	var er contract.EnrollmentResponse
	json.Unmarshal(body, &er)
	return er
}

func (h *harness) ingest(key string, events ...any) (*http.Response, contract.IngestResponse, []byte) {
	h.t.Helper()
	raw := make([]json.RawMessage, len(events))
	for i, e := range events {
		raw[i], _ = json.Marshal(e)
	}
	resp, body := h.post("/v1/events", key, contract.IngestRequest{ContractVersion: contract.Version, Events: raw})
	var ir contract.IngestResponse
	if resp.StatusCode == http.StatusOK {
		if err := contract.ValidateIngestResponse(body); err != nil {
			h.t.Fatalf("response violates contract: %v", err)
		}
		json.Unmarshal(body, &ir)
	}
	return resp, ir, body
}

func (h *harness) recorded() []map[string]any {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + "/_fake/events")
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func turn(deviceID, msgID string) contract.Turn {
	s := func(v string) *string { return &v }
	return contract.NewTurn(contract.Envelope{
		ContractVersion: contract.Version, DeviceID: deviceID, Harness: "claude-code", HarnessVersion: s("2.1.310"),
		AgentVersion: "0.1.0", SessionID: "s-1", SentAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
	}, contract.TurnFields{MessageID: msgID, Timestamp: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), Usage: contract.UnknownUsage()})
}

// @spec: session-event-wire-contract#fake-ingest-validates-same-schemas
// @spec: session-event-wire-contract#ingest-response-per-event
// @spec: session-event-wire-contract#schema-rejection-is-partial
func TestIngestAcceptsValidAndRejectsInvalidPerEvent(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	bad := map[string]any{"event_kind": "session.turn", "cwd": "/Users/x"}
	resp, ir, body := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1"), bad, turn(er.DeviceID, "m2"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s", resp.StatusCode, body)
	}
	if ir.Accepted != 2 || ir.Duplicates != 0 || len(ir.Rejected) != 1 || ir.Rejected[0].Index != 1 || ir.Rejected[0].Reason == "" {
		t.Fatalf("response %+v", ir)
	}
	if got := len(h.recorded()); got != 2 {
		t.Fatalf("recorded %d events", got)
	}
}

// @spec: session-event-wire-contract#idempotency-key
func TestDuplicatesAreCountedNotRecorded(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	h.ingest(er.DeviceKey, turn(er.DeviceID, "m1"))
	_, ir, _ := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1"), turn(er.DeviceID, "m2"))
	if ir.Accepted != 1 || ir.Duplicates != 1 {
		t.Fatalf("response %+v", ir)
	}
	if got := len(h.recorded()); got != 2 {
		t.Fatalf("recorded %d events", got)
	}
}

// An event that claims another device's id is rejected: a device key only
// ingests for its own device.
func TestEventForAnotherDeviceRejected(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	_, ir, _ := h.ingest(er.DeviceKey, turn("someone-else", "m1"))
	if ir.Accepted != 0 || len(ir.Rejected) != 1 {
		t.Fatalf("response %+v", ir)
	}
}

// @spec: session-event-wire-contract#contract-version-on-every-request
func TestUnsupportedContractVersionNamesAcceptedVersions(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	raw, _ := json.Marshal(turn(er.DeviceID, "m1"))
	resp, body := h.post("/v1/events", er.DeviceKey, contract.IngestRequest{ContractVersion: "999", Events: []json.RawMessage{raw}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := contract.ValidateIngestError(body); err != nil {
		t.Fatal(err)
	}
	var e contract.ErrorResponse
	json.Unmarshal(body, &e)
	if len(e.AcceptedVersions) == 0 || e.AcceptedVersions[0] != contract.Version {
		t.Fatalf("accepted versions %v", e.AcceptedVersions)
	}
}

// @spec: session-event-wire-contract#auth-errors-are-distinct
func TestAuthAndSchemaFailuresHaveDistinctStatuses(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()

	if resp, _, _ := h.ingest("not-a-key", turn(er.DeviceID, "m1")); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown key: %d", resp.StatusCode)
	}
	if resp, _ := h.post("/v1/events", er.DeviceKey, map[string]any{"contract_version": contract.Version, "events": []any{}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("empty batch: %d", resp.StatusCode)
	}
	h.post("/_fake/devices/"+er.DeviceID+"/forbid", "", nil)
	if resp, _, _ := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("forbidden device: %d", resp.StatusCode)
	}
	h.post("/_fake/devices/"+er.DeviceID+"/revoke", "", nil)
	if resp, _, _ := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1")); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", resp.StatusCode)
	}
}

func TestEnrollmentValidatesRequestAndToken(t *testing.T) {
	h := start(t, fakeingest.Options{EnrollmentTokens: []string{"et_good"}})
	resp, _ := h.post("/v1/enroll", "", map[string]any{"contract_version": contract.Version})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid request: %d", resp.StatusCode)
	}
	req := contract.EnrollmentRequest{
		ContractVersion: contract.Version, EnrollmentToken: "et_bad", Email: "e@example.com", EmailConfirmedBy: "engineer",
		Device: contract.DeviceDescription{Hostname: "h", OS: "o", Arch: "a", Harness: "claude-code", AgentVersion: "v"},
	}
	if resp, _ := h.post("/v1/enroll", "", req); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown token: %d", resp.StatusCode)
	}
	req.EnrollmentToken = "et_good"
	if resp, body := h.post("/v1/enroll", "", req); resp.StatusCode != http.StatusOK {
		t.Errorf("good token: %d %s", resp.StatusCode, body)
	}
}

// Re-enrolling the same device yields a fresh key; the enrolled email is
// visible to whoever runs the fake so a security reviewer can see it.
func TestEnrollmentsAreListed(t *testing.T) {
	h := start(t, fakeingest.Options{})
	a, b := h.enroll(), h.enroll()
	if a.DeviceKey == b.DeviceKey || a.DeviceID == b.DeviceID {
		t.Fatal("enrollments must mint distinct devices and keys")
	}
	resp, _ := http.Get(h.srv.URL + "/_fake/enrollments")
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 2 || list[0]["email"] != "e@example.com" {
		t.Fatalf("enrollments %v", list)
	}
}

// @spec: test-and-release#fake-ingest-fault-modes
func TestFaultModes(t *testing.T) {
	cases := map[string]int{
		"unauthorized": http.StatusUnauthorized,
		"forbidden":    http.StatusForbidden,
		"server_error": http.StatusInternalServerError,
	}
	for mode, status := range cases {
		h := start(t, fakeingest.Options{})
		er := h.enroll()
		h.post("/_fake/faults", "", map[string]any{"mode": mode, "count": 1})
		if resp, _, _ := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1")); resp.StatusCode != status {
			t.Errorf("%s: status %d", mode, resp.StatusCode)
		}
		if resp, _, _ := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1")); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: fault did not clear after count, status %d", mode, resp.StatusCode)
		}
	}

	h := start(t, fakeingest.Options{})
	er := h.enroll()
	h.post("/_fake/faults", "", map[string]any{"mode": "schema", "count": 1})
	_, ir, _ := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1"), turn(er.DeviceID, "m2"))
	if ir.Accepted != 0 || len(ir.Rejected) != 2 {
		t.Errorf("schema fault: %+v", ir)
	}

	h = start(t, fakeingest.Options{StallFor: 200 * time.Millisecond})
	er = h.enroll()
	h.post("/_fake/faults", "", map[string]any{"mode": "stall", "count": 1})
	began := time.Now()
	h.ingest(er.DeviceKey, turn(er.DeviceID, "m1"))
	if time.Since(began) < 200*time.Millisecond {
		t.Errorf("stall returned after %s", time.Since(began))
	}
}

func TestUnknownFaultModeRejected(t *testing.T) {
	h := start(t, fakeingest.Options{})
	if resp, _ := h.post("/_fake/faults", "", map[string]any{"mode": "explode"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// @spec: session-event-wire-contract#contract-version-on-every-request
func TestEventWithUnacceptedContractVersionRejected(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	old := turn(er.DeviceID, "m1")
	old.ContractVersion = "999"
	_, ir, _ := h.ingest(er.DeviceKey, old, turn(er.DeviceID, "m2"))
	if ir.Accepted != 1 || len(ir.Rejected) != 1 || ir.Rejected[0].Index != 0 {
		t.Fatalf("response %+v", ir)
	}
}

// @spec: session-event-wire-contract#schema-rejection-is-partial
func TestNonObjectEventRejectedPerEvent(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	resp, ir, body := h.ingest(er.DeviceKey, turn(er.DeviceID, "m1"), nil)
	if resp.StatusCode != http.StatusOK || ir.Accepted != 1 || len(ir.Rejected) != 1 || ir.Rejected[0].Index != 1 {
		t.Fatalf("status %d %+v %s", resp.StatusCode, ir, body)
	}
}

// Two pull requests opened from one session are two events.
// @spec: session-event-wire-contract#idempotency-key
func TestTwoPROpenedInOneSessionAreDistinct(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	env := turn(er.DeviceID, "x").Envelope
	pr := func(n int64) contract.PROpened {
		return contract.NewPROpened(env, contract.PROpenedFields{Repo: &contract.Repo{Host: "github.com", Owner: "o", Name: "r"}, Number: n, Timestamp: env.SentAt})
	}
	_, ir, _ := h.ingest(er.DeviceKey, pr(42), pr(43), pr(42))
	if ir.Accepted != 2 || ir.Duplicates != 1 {
		t.Fatalf("%+v", ir)
	}
}

// A resumed session closes once per process; each close is kept, and a
// redelivered close is a duplicate.
// @spec: session-event-wire-contract#idempotency-key
func TestResumedSessionCloseIsKept(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	env := turn(er.DeviceID, "x").Envelope
	closed := func(at time.Time) contract.Closed {
		return contract.NewClosed(env, contract.ClosedFields{EndedAt: at})
	}
	first, later := env.SentAt, env.SentAt.Add(time.Hour)
	_, ir, body := h.ingest(er.DeviceKey, closed(first), closed(later), closed(first))
	if ir.Accepted != 2 || ir.Duplicates != 1 {
		t.Fatalf("%+v %s", ir, body)
	}
}

// A fork re-sends its parent's turns and pull requests under its own session
// id; they add nothing.
// @spec: session-event-wire-contract#idempotency-key
func TestForkCopiesAreDuplicates(t *testing.T) {
	var dups bytes.Buffer
	h := start(t, fakeingest.Options{RecordDuplicates: &dups})
	er := h.enroll()
	parent := turn(er.DeviceID, "msg_1")
	fork := turn(er.DeviceID, "msg_1")
	fork.SessionID = "s-fork"
	own := turn(er.DeviceID, "msg_2")
	own.SessionID = "s-fork"
	pr := func(session string) contract.PROpened {
		env := parent.Envelope
		env.SessionID = session
		return contract.NewPROpened(env, contract.PROpenedFields{Repo: &contract.Repo{Host: "github.com", Owner: "o", Name: "r"}, Number: 7, Timestamp: env.SentAt})
	}
	h.ingest(er.DeviceKey, parent, pr("s-1"))
	_, ir, body := h.ingest(er.DeviceKey, fork, pr("s-fork"), own)
	if ir.Accepted != 1 || ir.Duplicates != 2 {
		t.Fatalf("%+v %s", ir, body)
	}
	for _, ev := range h.recorded() {
		if ev["message_id"] == "msg_1" && ev["session_id"] != "s-1" {
			t.Errorf("the parent's turn moved to %v", ev["session_id"])
		}
	}
	// The dropped copies are recorded as sent, under the fork's session.
	lines := bytes.Split(bytes.TrimSpace(dups.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("recorded %d duplicates: %s", len(lines), dups.Bytes())
	}
	for _, l := range lines {
		var ev map[string]any
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		if ev["session_id"] != "s-fork" {
			t.Errorf("duplicate recorded under %v", ev["session_id"])
		}
	}
}

func openedAs(deviceID, session, source string, at time.Time) contract.Opened {
	env := turn(deviceID, "x").Envelope
	env.SessionID = session
	return contract.NewOpened(env, contract.OpenedFields{StartedAt: at, Source: &source})
}

func turnIn(deviceID, session, msgID string) contract.Turn {
	t := turn(deviceID, msgID)
	t.SessionID = session
	return t
}

func ownerOf(h *harness, msgID string) any {
	for _, ev := range h.recorded() {
		if ev["message_id"] == msgID {
			return ev["session_id"]
		}
	}
	return nil
}

// A backfill sends the newest transcript first, so a fork's copies of its
// parent's turns often arrive before the parent's own. The turn still goes
// to the parent.
// @spec: session-event-wire-contract#shared-event-owner
func TestSharedTurnGoesToTheParentWhicheverArrivesFirst(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	for _, order := range []string{"fork first", "parent first"} {
		h := start(t, fakeingest.Options{})
		er := h.enroll()
		fork := []any{openedAs(er.DeviceID, "s-fork", "fork", t0.Add(time.Hour)), turnIn(er.DeviceID, "s-fork", "msg_1"), turnIn(er.DeviceID, "s-fork", "msg_2")}
		parent := []any{openedAs(er.DeviceID, "s-parent", "startup", t0), turnIn(er.DeviceID, "s-parent", "msg_1")}
		if order == "fork first" {
			h.ingest(er.DeviceKey, fork...)
			h.ingest(er.DeviceKey, parent...)
		} else {
			h.ingest(er.DeviceKey, parent...)
			h.ingest(er.DeviceKey, fork...)
		}
		if got := ownerOf(h, "msg_1"); got != "s-parent" {
			t.Errorf("%s: msg_1 belongs to %v, want s-parent", order, got)
		}
		if got := ownerOf(h, "msg_2"); got != "s-fork" {
			t.Errorf("%s: msg_2 belongs to %v, want s-fork", order, got)
		}
		if n := len(h.recorded()); n != 4 {
			t.Errorf("%s: %d events stored, want 4", order, n)
		}
	}
}

// Without a fork among them, the session that started first owns a shared
// turn, and a session.opened arriving after the turn moves it.
// @spec: session-event-wire-contract#shared-event-owner
func TestSharedTurnGoesToTheEarliestSession(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	// "s-a" sorts first, so before any session.opened it owns the turn.
	h.ingest(er.DeviceKey, turnIn(er.DeviceID, "s-b", "msg_1"), turnIn(er.DeviceID, "s-a", "msg_1"))
	if got := ownerOf(h, "msg_1"); got != "s-a" {
		t.Fatalf("before any opened, msg_1 belongs to %v", got)
	}
	h.ingest(er.DeviceKey, openedAs(er.DeviceID, "s-a", "startup", t0.Add(time.Hour)))
	h.ingest(er.DeviceKey, openedAs(er.DeviceID, "s-b", "resume", t0))
	if got := ownerOf(h, "msg_1"); got != "s-b" {
		t.Fatalf("msg_1 belongs to %v, want the earlier s-b", got)
	}
}
