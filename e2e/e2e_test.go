// Package e2e execs the built agent exactly as a harness hook does, with the
// hook name on the command line and the payload on standard input, against
// the built fake ingest server.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
)

var agentBin, fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "neo-tax-e2e")
	if err != nil {
		panic(err)
	}
	agentBin = filepath.Join(dir, "neo-tax-agent")
	fakeBin = filepath.Join(dir, "fake-ingest")
	for out, pkg := range map[string]string{agentBin: "../cmd/neo-tax-agent", fakeBin: "../cmd/fake-ingest"} {
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, b)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type world struct {
	t          *testing.T
	url        string
	data       string
	repo       string
	transcript string
	config     string // CLAUDE_CONFIG_DIR, so backfill never reads this machine's sessions
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newWorld(t *testing.T) *world {
	t.Helper()
	addr := freePort(t)
	cmd := exec.Command(fakeBin, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	w := &world{t: t, url: "http://" + addr, data: t.TempDir(), repo: t.TempDir(), config: t.TempDir()}
	for i := 0; ; i++ {
		if resp, err := http.Get(w.url + "/_fake/events"); err == nil {
			resp.Body.Close()
			break
		}
		if i > 100 {
			t.Fatal("fake ingest did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/neo-tax/harness-plugins.git"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		c := exec.Command("git", args...)
		c.Dir = w.repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, b)
		}
	}
	src, _ := os.ReadFile("../internal/adapter/claudecode/testdata/fixtures/synthetic-2.1.310/fixture-session.jsonl")
	w.transcript = filepath.Join(t.TempDir(), "fixture-session.jsonl")
	os.WriteFile(w.transcript, src, 0o600)
	return w
}

func (w *world) env() []string {
	return append(os.Environ(), "NEO_TAX_AGENT_BIN="+agentBin, "NEO_TAX_DATA_DIR="+w.data, "CLAUDE_CODE_ENTRYPOINT=cli", "NEO_TAX_NO_BROWSER=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "CLAUDE_CONFIG_DIR="+w.config)
}

// shim runs the plugin's bin/neo-tax-agent, as hooks and skills do.
func (w *world) shim(stdin string, args ...string) (string, int) {
	w.t.Helper()
	cmd := exec.Command(filepath.Join(pluginDir, "bin", "neo-tax-agent"), args...)
	cmd.Env = w.env()
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return string(out), code
}

func (w *world) hook(name string, extra map[string]any) {
	w.t.Helper()
	p := map[string]any{"session_id": "fixture-session", "transcript_path": w.transcript, "cwd": w.repo, "hook_event_name": name}
	for k, v := range extra {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	if out, code := w.shim(string(b), "hook", name); code != 0 {
		w.t.Fatalf("hook %s exit %d: %s", name, code, out)
	}
}

func (w *world) login() {
	w.t.Helper()
	before, _ := claudecode.ReadBackfillStatus(w.data)
	if out, code := w.shim("et\n", "login", "--server", w.url, "--token-stdin", "--email", "e@example.com"); code != 0 {
		w.t.Fatalf("login exit %d: %s", code, out)
	}
	w.waitBackfill(before.StartedAt)
}

// waitBackfill waits for the detached backfill that login or --background
// started after since to finish, so it never races the test's own hooks or
// its temp-dir cleanup.
func (w *world) waitBackfill(since time.Time) {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := claudecode.ReadBackfillStatus(w.data)
		if err == nil && st.StartedAt.After(since) && !st.FinishedAt.IsZero() {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatal("background backfill did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *world) recorded() []map[string]any {
	resp, err := http.Get(w.url + "/_fake/events")
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (w *world) fault(mode string, count int) {
	b, _ := json.Marshal(map[string]any{"mode": mode, "count": count})
	http.Post(w.url+"/_fake/faults", "application/json", bytes.NewReader(b))
}

func kinds(evs []map[string]any) string {
	var k []string
	for _, e := range evs {
		k = append(k, strings.TrimPrefix(e["event_kind"].(string), "session."))
	}
	return strings.Join(k, ",")
}

// @spec: test-and-release#end-to-end-against-fake-ingest
// @spec: claude-code-transcript-parser#hook-exec-shape
// @spec: event-queue-and-backfill#flush-on-every-hook
func TestSessionLifecycle(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.hook("SessionStart", map[string]any{"source": "startup"})
	w.hook("Stop", nil)
	w.hook("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	got := w.recorded()
	if k := kinds(got); k != "opened,turn,turn,pr_linked,turn,closed" {
		t.Fatalf("recorded %s", k)
	}
	for _, e := range got {
		if id, _ := e["device_id"].(string); id == "" || e["session_id"] != "fixture-session" {
			t.Fatalf("envelope %v", e)
		}
	}
}

// Events queued before enrollment are delivered by the first hook after it.
func TestQueuedBeforeEnrollmentDeliveredAfter(t *testing.T) {
	w := newWorld(t)
	w.hook("SessionStart", map[string]any{"source": "startup"})
	if len(w.recorded()) != 0 {
		t.Fatal("sent before enrollment")
	}
	w.login()
	w.hook("Stop", nil)
	if k := kinds(w.recorded()); k != "opened,turn,turn,pr_linked,turn" {
		t.Fatalf("recorded %s", k)
	}
}

// @spec: test-and-release#fake-ingest-fault-modes
// @spec: event-queue-and-backfill#server-and-network-errors-back-off
func TestServerErrorLeavesEventsQueued(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.fault("server_error", 1)
	w.hook("SessionStart", map[string]any{"source": "startup"})
	w.hook("Stop", nil) // within backoff: no flush
	if len(w.recorded()) != 0 {
		t.Fatal("flushed during backoff")
	}
	paths, _ := filepath.Glob(filepath.Join(w.data, "queue", "*.jsonl"))
	if len(paths) == 0 {
		t.Fatal("events not queued")
	}
}

// @spec: test-and-release#fake-ingest-fault-modes
// @spec: event-queue-and-backfill#auth-rejection-stops-flushing
func TestAuthRejectionNeedsReEnrollment(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.fault("unauthorized", 1)
	w.hook("SessionStart", map[string]any{"source": "startup"})
	w.hook("Stop", nil)
	if len(w.recorded()) != 0 {
		t.Fatal("flushed while rejected")
	}
	w.login()
	w.hook("SessionEnd", map[string]any{"reason": "other"})
	if k := kinds(w.recorded()); k != "opened,turn,turn,pr_linked,turn,closed" {
		t.Fatalf("recorded %s", k)
	}
}

// @spec: test-and-release#fake-ingest-fault-modes
// @spec: event-queue-and-backfill#schema-rejection-dead-letters
func TestSchemaRejectionDeadLetters(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.fault("schema", 1)
	w.hook("SessionStart", map[string]any{"source": "startup"})
	dl, _ := filepath.Glob(filepath.Join(w.data, "deadletter", "*.json"))
	if len(dl) != 1 {
		t.Fatalf("dead letters %v", dl)
	}
	w.hook("Stop", nil)
	if k := kinds(w.recorded()); k != "turn,turn,pr_linked,turn" {
		t.Fatalf("recorded %s", k)
	}
}

// A stalled server does not hang the hook past its flush budget.
// @spec: test-and-release#fake-ingest-fault-modes
func TestStallDoesNotBlockForever(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.fault("stall", 1)
	start := time.Now()
	w.hook("SessionStart", map[string]any{"source": "startup"})
	if d := time.Since(start); d > 45*time.Second {
		t.Fatalf("hook took %s", d)
	}
}

// Hooks always exit 0, whatever they are given.
func TestHookNeverFails(t *testing.T) {
	w := newWorld(t)
	for _, in := range []string{"", "garbage", "{}"} {
		if out, code := w.shim(in, "hook", "Stop"); code != 0 {
			t.Fatalf("%q: exit %d %s", in, code, out)
		}
	}
}

// Run as the login skill runs it, with no terminal, the login entry point
// serves the browser form and enrolls when it is submitted; the token never
// appears in its output.
// @spec: device-enrollment#interactive-enrollment
// @spec: device-enrollment#token-never-in-conversation
func TestLoginWithoutTerminalEnrollsThroughTheForm(t *testing.T) {
	w := newWorld(t)
	cmd := exec.Command(filepath.Join(pluginDir, "bin", "neo-tax-agent"), "login", "--server", w.url)
	cmd.Env = w.env()
	cmd.Stdin = strings.NewReader("")
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	sc := bufio.NewScanner(stdout)
	formURL := ""
	for formURL == "" && sc.Scan() {
		line := sc.Text()
		out.WriteString(line + "\n")
		if u := strings.TrimSpace(line); strings.HasPrefix(u, "http://127.0.0.1:") {
			formURL = u
		}
	}
	if formURL == "" {
		cmd.Process.Kill()
		t.Fatalf("no form address:\n%s", out.String())
	}
	resp, err := http.PostForm(formURL, url.Values{"token": {"et_e2e_secret"}, "email": {"other"}, "email_other": {"e2e@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Enrolled this device as e2e@example.com.") || strings.Contains(out.String(), "et_e2e_secret") {
		t.Fatalf("output:\n%s", out.String())
	}
}

// pastSession puts a finished session under CLAUDE_CONFIG_DIR/projects.
func (w *world) pastSession() {
	w.t.Helper()
	dir := filepath.Join(w.config, "projects", "-past")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "33333333-3333-4333-8333-333333333333.jsonl"), []byte(`{"type":"user","timestamp":"2026-10-20T09:00:00Z","cwd":"`+w.repo+`","gitBranch":"old","version":"2.1.310","message":{"content":"x"}}
{"type":"assistant","timestamp":"2026-10-20T09:00:01Z","gitBranch":"old","version":"2.1.310","message":{"id":"msg_P","model":"m","usage":{"input_tokens":3,"output_tokens":4}}}
`), 0o600)
}

// The backfill skill's --background form returns at once and the detached
// backfill still runs.
// @spec: event-queue-and-backfill#backfill-command
func TestBackfillInBackground(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.pastSession()
	before, _ := claudecode.ReadBackfillStatus(w.data)
	out, code := w.shim("", "backfill", "--background")
	if code != 0 || !strings.Contains(out, "background") {
		t.Fatalf("exit %d: %s", code, out)
	}
	w.waitBackfill(before.StartedAt)
	if k := kinds(w.recorded()); k != "opened,turn" {
		t.Fatalf("recorded %s", k)
	}
}

// @spec: event-queue-and-backfill#backfill-after-login
// @spec: device-enrollment#backfill-after-interactive-login
func TestLoginStartsBackfill(t *testing.T) {
	w := newWorld(t)
	w.pastSession()
	out, code := w.shim("et\n", "login", "--server", w.url, "--token-stdin", "--email", "e@example.com")
	if code != 0 || !strings.Contains(out, "background") {
		t.Fatalf("exit %d: %s", code, out)
	}
	w.waitBackfill(time.Time{})
	got := w.recorded()
	if k := kinds(got); k != "opened,turn" || got[0]["session_id"] != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("recorded %s", k)
	}
}

// @spec: event-queue-and-backfill#backfill-command
func TestBackfillCommand(t *testing.T) {
	w := newWorld(t)
	w.login()
	w.pastSession()
	out, code := w.shim("", "backfill")
	if code != 0 || !strings.Contains(out, "Backfilled 1 sessions") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if k := kinds(w.recorded()); k != "opened,turn" {
		t.Fatalf("recorded %s", k)
	}
}

// --background refuses up front, with the reason, when the backfill could
// not run.
func TestBackgroundBackfillRefusesBeforeLogin(t *testing.T) {
	w := newWorld(t)
	out, code := w.shim("", "backfill", "--background")
	if code == 0 || !strings.Contains(out, "neo-tax:login") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
