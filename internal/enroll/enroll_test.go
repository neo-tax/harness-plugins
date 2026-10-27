package enroll_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/agent"
	"github.com/neo-tax/harness-plugins/internal/enroll"
	"github.com/neo-tax/harness-plugins/internal/fakeingest"
	"github.com/neo-tax/harness-plugins/internal/ingest"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

type fixture struct {
	t      *testing.T
	dir    string
	srv    *httptest.Server
	out    bytes.Buffer
	last   contract.EnrollmentRequest
	gitDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: filepath.Join(t.TempDir(), "data")}
	fake := fakeingest.New(fakeingest.Options{EnrollmentTokens: []string{"et_good"}})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/enroll" {
			var buf bytes.Buffer
			buf.ReadFrom(r.Body)
			json.Unmarshal(buf.Bytes(), &f.last)
			r.Body = ioNopCloser{bytes.NewReader(buf.Bytes())}
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	// A git with a known global user.email and nothing else.
	f.gitDir = t.TempDir()
	os.WriteFile(filepath.Join(f.gitDir, "gitconfig"), []byte("[user]\n\temail = Engineer@Example.com\n"), 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(f.gitDir, "gitconfig"))
	return f
}

type ioNopCloser struct{ *bytes.Reader }

func (ioNopCloser) Close() error { return nil }

func (f *fixture) opts(token, email string) enroll.Options {
	return enroll.Options{
		DataDir: f.dir, Server: f.srv.URL, Token: token, Email: email,
		Harness: "claude-code", HarnessVersion: "2.1.310", AgentVersion: "0.1.0",
		AccountEmail: func() string { return "account@example.com" },
		Out:          &f.out,
	}
}

// @spec: device-enrollment#enrollment-is-the-only-way-to-get-a-key
// @spec: device-enrollment#email-confirmed-by-engineer
// @spec: session-event-wire-contract#enrollment-request
func TestLoginEnrollsAndStoresKey(t *testing.T) {
	f := newFixture(t)
	if _, err := enroll.Load(f.dir); !errors.Is(err, enroll.ErrNotEnrolled) {
		t.Fatalf("before login: %v", err)
	}
	if err := enroll.Login(context.Background(), f.opts("et_good", "engineer@example.com")); err != nil {
		t.Fatal(err)
	}
	d, err := enroll.Load(f.dir)
	if err != nil || d.DeviceID == "" || d.DeviceKey == "" || d.IngestBaseURL != f.srv.URL {
		t.Fatalf("%+v %v", d, err)
	}
	if f.last.EmailConfirmedBy != contract.EmailConfirmedByEngineer || f.last.Email != "engineer@example.com" {
		t.Errorf("request %+v", f.last)
	}
	dev := f.last.Device
	if dev.OS != runtime.GOOS || dev.Arch != runtime.GOARCH || dev.Harness != "claude-code" || dev.AgentVersion != "0.1.0" || dev.HarnessVersion == nil {
		t.Errorf("device %+v", dev)
	}
	b, _ := json.Marshal(f.last)
	if err := contract.ValidateEnrollmentRequest(b); err != nil {
		t.Error(err)
	}
	if strings.Contains(f.out.String(), d.DeviceKey) {
		t.Error("device key printed")
	}
}

// @spec: device-enrollment#key-storage-location
func TestKeyStoredReadableOnlyByOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	f := newFixture(t)
	if err := enroll.Login(context.Background(), f.opts("et_good", "e@example.com")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(f.dir, enroll.DeviceFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("device file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(f.dir)
	if di.Mode().Perm()&0o077 != 0 {
		t.Errorf("data dir mode %v", di.Mode().Perm())
	}
}

// @spec: device-enrollment#one-key-per-device
func TestEnrollingAgainReplacesKey(t *testing.T) {
	f := newFixture(t)
	enroll.Login(context.Background(), f.opts("et_good", "e@example.com"))
	first, _ := enroll.Load(f.dir)
	if err := enroll.Login(context.Background(), f.opts("et_good", "e@example.com")); err != nil {
		t.Fatal(err)
	}
	second, _ := enroll.Load(f.dir)
	if second.DeviceKey == first.DeviceKey {
		t.Fatal("key not replaced")
	}
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "device") && e.Name() != enroll.DeviceFile {
			t.Errorf("extra key file %s", e.Name())
		}
	}
}

// @spec: device-enrollment#rejected-key-stops-flushing
func TestLoginClearsReEnrollmentFlag(t *testing.T) {
	f := newFixture(t)
	q := queue.New(f.dir)
	q.Append([][]byte{[]byte(`{"event_kind":"session.closed"}`)})
	// Mark as needing enrollment the way a 401 would.
	q.Flush(context.Background(), failingAuth{})
	if !q.NeedsEnrollment() {
		t.Fatal("setup")
	}
	if err := enroll.Login(context.Background(), f.opts("et_good", "e@example.com")); err != nil {
		t.Fatal(err)
	}
	if q.NeedsEnrollment() {
		t.Fatal("still needs enrollment")
	}
}

func TestRejectedTokenStoresNothing(t *testing.T) {
	f := newFixture(t)
	err := enroll.Login(context.Background(), f.opts("et_bad", "e@example.com"))
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err %v", err)
	}
	if _, err := enroll.Load(f.dir); !errors.Is(err, enroll.ErrNotEnrolled) {
		t.Fatal("stored a device after rejection")
	}
}

func TestInvalidEmailRejectedBeforeRequest(t *testing.T) {
	f := newFixture(t)
	if err := enroll.Login(context.Background(), f.opts("et_good", "not-an-email")); err == nil {
		t.Fatal("expected error")
	}
	if f.last.EnrollmentToken != "" {
		t.Fatal("sent a request")
	}
}

// Without a terminal the agent cannot prompt, so it says exactly what it
// needs and which emails it found, and stores nothing.
// With neither a terminal nor the browser form, the agent never asks the
// model for the token: it names the command to run in a terminal.
// @spec: device-enrollment#token-never-in-conversation
func TestMissingInputPointsToTheTerminal(t *testing.T) {
	f := newFixture(t)
	o := f.opts("", "")
	o.TerminalCommand = "/path/to/neo-tax-agent login"
	err := enroll.Login(context.Background(), o)
	if !errors.Is(err, enroll.ErrNeedInput) {
		t.Fatalf("err %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "/path/to/neo-tax-agent login") {
		t.Errorf("output lacks the terminal command:\n%s", out)
	}
	for _, never := range []string{"--token", "Ask the engineer", "paste"} {
		if strings.Contains(out, never) {
			t.Errorf("output asks for the token through the conversation (%q):\n%s", never, out)
		}
	}
	if f.last.EnrollmentToken != "" {
		t.Fatal("sent a request")
	}
}

// @spec: device-enrollment#email-candidates
func TestCandidatesDedupedAndAccountOptional(t *testing.T) {
	f := newFixture(t)
	o := f.opts("", "")
	o.Interactive = true
	o.In = strings.NewReader("\n\n")
	o.AccountEmail = func() string { return "ENGINEER@example.com" }
	enroll.Login(context.Background(), o)
	if n := strings.Count(strings.ToLower(f.out.String()), "engineer@example.com"); n != 1 {
		t.Errorf("candidate listed %d times:\n%s", n, f.out.String())
	}
	f.out.Reset()
	o.AccountEmail = nil
	o.In = strings.NewReader("\n\n")
	enroll.Login(context.Background(), o)
	if strings.Contains(f.out.String(), "account@") {
		t.Error("account email without a source")
	}
}

// On a terminal the agent prompts, offering candidates by number.
// @spec: device-enrollment#interactive-enrollment
// @spec: device-enrollment#email-candidates
func TestPromptsWhenInteractive(t *testing.T) {
	f := newFixture(t)
	o := f.opts("", "")
	o.Interactive = true
	o.In = strings.NewReader("et_good\n2\n")
	if err := enroll.Login(context.Background(), o); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	if f.last.Email != "account@example.com" {
		t.Fatalf("chose %q", f.last.Email)
	}
	f.last = contract.EnrollmentRequest{}
	o.In = strings.NewReader("et_good\nTyped@Example.com\n")
	if err := enroll.Login(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if f.last.Email != "Typed@Example.com" {
		t.Fatalf("typed %q", f.last.Email)
	}
}

// @spec: device-enrollment#key-never-in-events
func TestKeyNeverOutsideDeviceFile(t *testing.T) {
	f := newFixture(t)
	if err := enroll.Login(context.Background(), f.opts("et_good", "e@example.com")); err != nil {
		t.Fatal(err)
	}
	d, _ := enroll.Load(f.dir)
	a := agent.New(f.dir)
	a.Emit(contract.NewClosed(contract.Envelope{ContractVersion: "1", DeviceID: d.DeviceID, Harness: "h", AgentVersion: "v", SessionID: "s", SentAt: time.Now()}, contract.ClosedFields{EndedAt: time.Now()}))
	a.Emit(map[string]any{"event_kind": "session.closed", "device_key": d.DeviceKey})
	client, _ := enroll.Client(f.dir, http.DefaultClient)
	http.Post(f.srv.URL+"/_fake/faults", "application/json", strings.NewReader(`{"mode":"schema"}`))
	a.Queue.Flush(context.Background(), client)
	filepath.WalkDir(f.dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || de.Name() == enroll.DeviceFile {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(d.DeviceKey)) {
			t.Errorf("device key in %s", p)
		}
		return nil
	})
}

type failingAuth struct{}

func (failingAuth) Send(context.Context, []json.RawMessage) (contract.IngestResponse, error) {
	return contract.IngestResponse{}, &ingest.StatusError{Code: http.StatusUnauthorized, Message: "revoked"}
}

func TestTokenReadWithoutEcho(t *testing.T) {
	f := newFixture(t)
	o := f.opts("", "e@example.com")
	o.Interactive = true
	o.In = strings.NewReader("")
	o.ReadSecret = func() (string, error) { return "et_good", nil }
	if err := enroll.Login(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if f.last.EnrollmentToken != "et_good" || strings.Contains(f.out.String(), "et_good") {
		t.Fatalf("token %q out %q", f.last.EnrollmentToken, f.out.String())
	}
}

// Events queued while the device awaited re-enrollment are delivered under
// the new device, not dead-lettered for carrying the old device's id.
// @spec: device-enrollment#rejected-key-stops-flushing
func TestReEnrollmentDeliversEventsQueuedWhileRejected(t *testing.T) {
	f := newFixture(t)
	if err := enroll.Login(context.Background(), f.opts("et_good", "e@example.com")); err != nil {
		t.Fatal(err)
	}
	old, _ := enroll.Load(f.dir)
	a := agent.New(f.dir)
	a.Emit(contract.NewClosed(contract.Envelope{ContractVersion: "1", DeviceID: old.DeviceID, Harness: "h", AgentVersion: "v", SessionID: "s", SentAt: time.Now()}, contract.ClosedFields{EndedAt: time.Now()}))
	http.Post(f.srv.URL+"/_fake/devices/"+old.DeviceID+"/revoke", "", nil)
	client, _ := enroll.Client(f.dir, http.DefaultClient)
	if res, _ := a.Queue.Flush(context.Background(), client); !res.NeedsEnrollment {
		t.Fatalf("%+v", res)
	}
	if err := enroll.Login(context.Background(), f.opts("et_good", "e@example.com")); err != nil {
		t.Fatal(err)
	}
	client, _ = enroll.Client(f.dir, http.DefaultClient)
	res, _ := a.Queue.Flush(context.Background(), client)
	if res.Sent != 1 || res.DeadLettered != 0 {
		t.Fatalf("%+v", res)
	}
}
