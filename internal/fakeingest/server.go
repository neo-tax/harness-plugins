// Package fakeingest is a stand-in for Neo.Tax's enrollment, ingest, and OTLP
// endpoints. It validates every request against the schema documents in this
// repository's contract package and records what it accepts, so tests,
// dogfooding, and security reviewers can see exactly what the agent sends.
//
// @spec: session-event-wire-contract#fake-ingest-validates-same-schemas
package fakeingest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

// Options configures a fake ingest server.
type Options struct {
	// EnrollmentTokens, when non-empty, is the set of tokens enrollment
	// accepts. When empty, any token enrolls.
	EnrollmentTokens []string
	// IngestBaseURL is returned to enrolling devices. When empty, the base
	// URL of the enrollment request is used.
	IngestBaseURL string
	// StallFor is how long the stall fault holds a request. Defaults to 30s.
	StallFor time.Duration
	// Record, when set, receives one JSON line per accepted event, as it
	// was first accepted; a shared event later filed under another session
	// (see owners) is not rewritten there, but GET /_fake/events shows it.
	Record io.Writer
	// RecordDuplicates, when set, receives one JSON line per event dropped
	// as a duplicate, so a caller can see what was re-sent, not only how
	// many.
	RecordDuplicates io.Writer
	// IngestKeys, when non-empty, is the set of keys the OTLP endpoints
	// accept. When empty, any key that is not a device key is accepted.
	IngestKeys []string
	// RecordOTLP, when set, receives one JSON line per OTLP log record or
	// metric data point received.
	RecordOTLP io.Writer
}

// Fault modes the server can be instructed to return.
const (
	FaultSchema       = "schema"
	FaultUnauthorized = "unauthorized"
	FaultForbidden    = "forbidden"
	FaultServerError  = "server_error"
	FaultStall        = "stall"
)

type device struct {
	ID        string `json:"device_id"`
	Email     string `json:"email"`
	ConfirmBy string `json:"email_confirmed_by"`
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Harness   string `json:"harness"`
	key       string
	revoked   bool
	forbidden bool
}

// Server is an http.Handler implementing the fake endpoints.
type Server struct {
	opts Options
	mux  *http.ServeMux

	mu          sync.Mutex
	devices     []*device
	byKey       map[string]*device
	seen        map[string]int // idempotency key to its index in events
	events      []json.RawMessage
	owners      owners
	otlpRecords []json.RawMessage
	fault       string
	faultLeft   int
}

// New returns a fake ingest server.
func New(opts Options) *Server {
	if opts.StallFor == 0 {
		opts.StallFor = 30 * time.Second
	}
	s := &Server{opts: opts, mux: http.NewServeMux(), byKey: map[string]*device{}, seen: map[string]int{}, owners: newOwners()}
	s.mux.HandleFunc("POST /v1/enroll", s.enroll)
	s.mux.HandleFunc("POST /v1/events", s.ingest)
	s.mux.HandleFunc("POST /otlp/v1/logs", s.otlp(signalLogs))
	s.mux.HandleFunc("POST /otlp/v1/metrics", s.otlp(signalMetrics))
	s.mux.HandleFunc("GET /_fake/otlp", s.listOTLP)
	s.mux.HandleFunc("DELETE /_fake/otlp", s.resetOTLP)
	s.mux.HandleFunc("GET /_fake/events", s.listEvents)
	s.mux.HandleFunc("DELETE /_fake/events", s.resetEvents)
	s.mux.HandleFunc("GET /_fake/enrollments", s.listEnrollments)
	s.mux.HandleFunc("POST /_fake/faults", s.setFault)
	s.mux.HandleFunc("POST /_fake/devices/{id}/revoke", s.deviceFlag(func(d *device) { d.revoked = true }))
	s.mux.HandleFunc("POST /_fake/devices/{id}/forbid", s.deviceFlag(func(d *device) { d.forbidden = true }))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, contract.ErrorResponse{Error: msg})
}

func randomID(prefix string) string {
	b := make([]byte, 16)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable body")
		return nil, false
	}
	return body, true
}

// versionAccepted rejects a request whose contract_version the server does
// not accept, naming the versions it does.
//
// @spec: session-event-wire-contract#contract-version-on-every-request
func versionAccepted(w http.ResponseWriter, body []byte) bool {
	var v struct {
		ContractVersion *string `json:"contract_version"`
	}
	if json.Unmarshal(body, &v) != nil || v.ContractVersion == nil {
		return true // left to schema validation
	}
	if slices.Contains(contract.AcceptedVersions, *v.ContractVersion) {
		return true
	}
	writeJSON(w, http.StatusBadRequest, contract.ErrorResponse{
		Error:            fmt.Sprintf("unsupported contract_version %q", *v.ContractVersion),
		AcceptedVersions: contract.AcceptedVersions,
	})
	return false
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok || !versionAccepted(w, body) {
		return
	}
	if err := contract.ValidateEnrollmentRequest(body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var req contract.EnrollmentRequest
	json.Unmarshal(body, &req)
	if len(s.opts.EnrollmentTokens) > 0 && !slices.Contains(s.opts.EnrollmentTokens, req.EnrollmentToken) {
		writeError(w, http.StatusUnauthorized, "unknown enrollment token")
		return
	}
	d := &device{
		ID: randomID("dev_"), key: randomID("ntdk_"), Email: req.Email, ConfirmBy: req.EmailConfirmedBy,
		Hostname: req.Device.Hostname, OS: req.Device.OS, Arch: req.Device.Arch, Harness: req.Device.Harness,
	}
	s.mu.Lock()
	s.devices = append(s.devices, d)
	s.byKey[d.key] = d
	s.mu.Unlock()

	base := s.opts.IngestBaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	writeJSON(w, http.StatusOK, contract.EnrollmentResponse{DeviceID: d.ID, DeviceKey: d.key, IngestBaseURL: base})
}

// takeFault consumes one use of the active fault, if any.
func (s *Server) takeFault() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.faultLeft == 0 {
		return ""
	}
	s.faultLeft--
	return s.fault
}

// idempotencyKey is the server's notion of "the same event". Turns and pull
// requests are keyed without the session, so a fork's copies of its parent's
// add nothing.
//
// @spec: session-event-wire-contract#idempotency-key
func idempotencyKey(ev map[string]any) string {
	key := fmt.Sprint(ev["device_id"], "\x00", ev["event_kind"])
	switch ev["event_kind"] {
	case contract.KindTurn:
		return key + "\x00" + fmt.Sprint(ev["message_id"])
	case contract.KindPROpened:
		repo, _ := json.Marshal(ev["repo"])
		return key + "\x00" + string(repo) + "\x00" + fmt.Sprint(ev["number"])
	case contract.KindClosed:
		// A resumed session closes again; its later close is kept.
		return key + "\x00" + fmt.Sprint(ev["session_id"], "\x00", ev["ended_at"])
	default:
		return key + "\x00" + fmt.Sprint(ev["session_id"])
	}
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	fault := s.takeFault()
	switch fault {
	case FaultUnauthorized:
		writeError(w, http.StatusUnauthorized, "fault: unauthorized")
		return
	case FaultForbidden:
		writeError(w, http.StatusForbidden, "fault: forbidden")
		return
	case FaultServerError:
		writeError(w, http.StatusInternalServerError, "fault: server error")
		return
	case FaultStall:
		select {
		case <-time.After(s.opts.StallFor):
		case <-r.Context().Done():
			return
		}
	}

	// @spec: session-event-wire-contract#auth-errors-are-distinct
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	d := s.byKey[key]
	var revoked, forbidden bool
	if d != nil {
		revoked, forbidden = d.revoked, d.forbidden
	}
	s.mu.Unlock()
	if d == nil || revoked {
		writeError(w, http.StatusUnauthorized, "invalid or revoked device key")
		return
	}
	if forbidden {
		writeError(w, http.StatusForbidden, "device key not permitted to ingest")
		return
	}

	body, ok := readBody(w, r)
	if !ok || !versionAccepted(w, body) {
		return
	}
	if err := contract.ValidateIngestRequest(body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var req contract.IngestRequest
	json.Unmarshal(body, &req)

	// @spec: session-event-wire-contract#schema-rejection-is-partial
	resp := contract.IngestResponse{Rejected: []contract.Rejection{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, raw := range req.Events {
		if fault == FaultSchema {
			resp.Rejected = append(resp.Rejected, contract.Rejection{Index: i, Reason: "fault: schema rejection"})
			continue
		}
		if err := contract.ValidateEvent(raw); err != nil {
			resp.Rejected = append(resp.Rejected, contract.Rejection{Index: i, Reason: err.Error()})
			continue
		}
		var ev map[string]any
		json.Unmarshal(raw, &ev)
		if v, _ := ev["contract_version"].(string); !slices.Contains(contract.AcceptedVersions, v) {
			resp.Rejected = append(resp.Rejected, contract.Rejection{Index: i, Reason: fmt.Sprintf("unsupported contract_version %q", v)})
			continue
		}
		if ev["device_id"] != d.ID {
			resp.Rejected = append(resp.Rejected, contract.Rejection{Index: i, Reason: "device_id does not match the device key"})
			continue
		}
		k := idempotencyKey(ev)
		if idx, ok := s.seen[k]; ok {
			if s.owners.report(k, ev, raw) {
				s.events[idx] = s.owners.best(k)
			}
			resp.Duplicates++
			if s.opts.RecordDuplicates != nil {
				s.opts.RecordDuplicates.Write(append(append([]byte{}, raw...), '\n'))
			}
			continue
		}
		s.seen[k] = len(s.events)
		s.events = append(s.events, raw)
		s.owners.report(k, ev, raw)
		if ev["event_kind"] == contract.KindOpened {
			for _, moved := range s.owners.opened(ev) {
				s.events[s.seen[moved]] = s.owners.best(moved)
			}
		}
		resp.Accepted++
		if s.opts.Record != nil {
			s.opts.Record.Write(append(append([]byte{}, raw...), '\n'))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listEvents(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.events
	if events == nil {
		events = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) resetEvents(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.events, s.seen, s.owners = nil, map[string]int{}, newOwners()
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listEnrollments(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*device, len(s.devices))
	copy(out, s.devices)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setFault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode  string `json:"mode"`
		Count int    `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Mode {
	case FaultSchema, FaultUnauthorized, FaultForbidden, FaultServerError, FaultStall, "":
	default:
		writeError(w, http.StatusBadRequest, "unknown fault mode "+req.Mode)
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Mode == "" {
		req.Count = 0
	}
	s.mu.Lock()
	s.fault, s.faultLeft = req.Mode, req.Count
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deviceFlag(set func(*device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, d := range s.devices {
			if d.ID == r.PathValue("id") {
				set(d)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		writeError(w, http.StatusNotFound, "no such device")
	}
}
