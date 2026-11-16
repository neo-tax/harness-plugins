package claudecode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/enroll"
	"github.com/neo-tax/harness-plugins/internal/fakeingest"
	"github.com/neo-tax/harness-plugins/internal/gitident"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

type hookEnv struct {
	t          *testing.T
	data       string
	repo       string
	transcript string
	srv        *httptest.Server
	now        time.Time
}

func newHookEnv(t *testing.T, enrolled bool) *hookEnv {
	t.Helper()
	h := &hookEnv{t: t, data: t.TempDir(), repo: t.TempDir(), now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	h.srv = httptest.NewServer(fakeingest.New(fakeingest.Options{}))
	t.Cleanup(h.srv.Close)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	for _, args := range [][]string{
		{"init", "-q", "-b", "Main-Branch"},
		{"remote", "add", "origin", "git@github.com:Neo-Tax/Harness-Plugins.git"},
		{"config", "user.email", "dev@example.com"},
		{"-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	main, _ := copyFixture(t, "testdata/fixtures/synthetic-2.1.310")
	h.transcript = main
	if enrolled {
		err := enroll.Login(context.Background(), enroll.Options{DataDir: h.data, Server: h.srv.URL, Token: "t", Email: "e@example.com", Harness: "claude-code", AgentVersion: "v"})
		if err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *hookEnv) run(name string, payload map[string]any) error {
	h.t.Helper()
	base := map[string]any{"session_id": "fixture-session", "transcript_path": h.transcript, "cwd": h.repo, "hook_event_name": name}
	for k, v := range payload {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	return claudecode.RunHook(context.Background(), name, bytes.NewReader(b), claudecode.HookOptions{
		DataDir: h.data, AgentVersion: "0.1.0", Entrypoint: "cli", Now: func() time.Time { return h.now },
		HTTP: &http.Client{Timeout: 5 * time.Second},
	})
}

func (h *hookEnv) recorded() []map[string]any {
	resp, err := http.Get(h.srv.URL + "/_fake/events")
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func kinds(evs []map[string]any) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e["event_kind"].(string))
	}
	return out
}

// @spec: claude-code-transcript-parser#opened-on-session-start
// @spec: claude-code-transcript-parser#hook-exec-shape
// @spec: event-queue-and-backfill#flush-on-every-hook
// @spec: repo-and-branch-identity#git-failure-is-null
func TestSessionStartEmitsOpened(t *testing.T) {
	h := newHookEnv(t, true)
	if err := h.run("SessionStart", map[string]any{"source": "resume"}); err != nil {
		t.Fatal(err)
	}
	got := h.recorded()
	if len(got) != 1 {
		t.Fatalf("recorded %v", kinds(got))
	}
	o := got[0]
	dev, _ := enroll.Load(h.data)
	want := map[string]any{
		"event_kind": "session.opened", "source": "resume", "entrypoint": "cli", "branch": "Main-Branch",
		"session_id": "fixture-session", "device_id": dev.DeviceID,
		"harness": "claude-code", "harness_version": "2.1.311", "started_at": "2026-10-25T12:00:00Z", "dropped_events": float64(0),
	}
	for k, v := range want {
		if o[k] != v {
			t.Errorf("%s = %v, want %v", k, o[k], v)
		}
	}
	if r, _ := o["repo"].(map[string]any); r["owner"] != "neo-tax" || r["name"] != "harness-plugins" {
		t.Errorf("repo %v", o["repo"])
	}
	if sha, _ := o["head_sha"].(string); len(sha) != 40 {
		t.Errorf("head_sha %v", o["head_sha"])
	}
}

// @spec: repo-and-branch-identity#git-failure-is-null
func TestSessionStartOutsideGitStillEmits(t *testing.T) {
	h := newHookEnv(t, true)
	h.repo = t.TempDir()
	h.run("SessionStart", map[string]any{"source": "startup"})
	got := h.recorded()
	if len(got) != 1 || got[0]["repo"] != nil || got[0]["branch"] != nil || got[0]["head_sha"] != nil {
		t.Fatalf("%v", got)
	}
}

// @spec: claude-code-transcript-parser#turns-from-transcript-tail
// @spec: claude-code-transcript-parser#subagent-transcripts-scanned
func TestStopAndPreCompactEmitNewTurns(t *testing.T) {
	h := newHookEnv(t, true)
	h.run("Stop", nil)
	if got := kinds(h.recorded()); len(got) != 5 {
		t.Fatalf("after Stop: %v", got)
	}
	f, _ := os.OpenFile(h.transcript, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","timestamp":"2026-10-25T10:05:00Z","version":"2.1.311","message":{"id":"msg_E","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	f.Close()
	h.run("PreCompact", map[string]any{"trigger": "auto", "custom_instructions": "keep the Acme details"})
	got := h.recorded()
	if len(got) != 6 || got[5]["message_id"] != "msg_E" {
		t.Fatalf("after PreCompact: %v", kinds(got))
	}
}

// @spec: claude-code-transcript-parser#closed-on-session-end
func TestSessionEndEmitsTurnsThenClosed(t *testing.T) {
	h := newHookEnv(t, true)
	h.run("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	got := h.recorded()
	k := kinds(got)
	if len(k) != 6 || k[5] != "session.closed" {
		t.Fatalf("%v", k)
	}
	if got[5]["reason"] != "prompt_input_exit" || got[5]["ended_at"] != "2026-10-25T12:00:00Z" {
		t.Fatalf("%v", got[5])
	}
}

// @spec: field-allowlist#content-bearing-payloads-reduced
// @spec: claude-code-transcript-parser#content-never-read-into-events
func TestPayloadContentNeverWrittenOrSent(t *testing.T) {
	h := newHookEnv(t, true)
	http.Post(h.srv.URL+"/_fake/faults", "application/json", strings.NewReader(`{"mode":"server_error","count":10}`))
	h.run("Stop", map[string]any{
		"last_assistant_message": "the Acme secret plan", "prompt": "tell me about Acme",
		"stop_hook_active": false, "permission_mode": "default",
	})
	h.run("SessionEnd", map[string]any{"reason": "other", "summary": "Acme summary"})
	filepath.WalkDir(h.data, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, s := range []string{"Acme", h.repo, h.transcript} {
			if bytes.Contains(b, []byte(s)) && !(strings.HasSuffix(p, "cursors.json") && s == h.transcript) {
				t.Errorf("%s contains %q", filepath.Base(p), s)
			}
		}
		return nil
	})
}

// Before enrollment, hooks queue events and make no request.
func TestNotEnrolledQueuesWithoutFlushing(t *testing.T) {
	h := newHookEnv(t, false)
	if err := h.run("SessionStart", map[string]any{"source": "startup"}); err != nil {
		t.Fatal(err)
	}
	if got := h.recorded(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
	paths, _ := filepath.Glob(filepath.Join(h.data, "queue", "*.jsonl"))
	if len(paths) != 1 {
		t.Fatalf("queued %v", paths)
	}
}

// A hook never fails the harness, whatever it is handed.
func TestBadInputIsLoggedNotFatal(t *testing.T) {
	h := newHookEnv(t, true)
	for _, in := range []string{"", "not json", `{"session_id": 5}`, `{"session_id":"s","transcript_path":"/nope/x.jsonl","cwd":"/nope"}`} {
		err := claudecode.RunHook(context.Background(), "Stop", strings.NewReader(in), claudecode.HookOptions{DataDir: h.data, AgentVersion: "v"})
		if err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	if err := claudecode.RunHook(context.Background(), "UserPromptSubmit", strings.NewReader(`{"prompt":"x"}`), claudecode.HookOptions{DataDir: h.data}); err != nil {
		t.Error(err)
	}
}

// @spec: event-queue-and-backfill#dropped-events-reported
func TestDroppedEventsRideOnOpenedAndClosed(t *testing.T) {
	for hook, kind := range map[string]string{"SessionStart": "session.opened", "SessionEnd": "session.closed"} {
		h := newHookEnv(t, true)
		q := queue.New(h.data)
		q.SegmentMax, q.Cap = 200, 400
		for i := 0; i < 20; i++ {
			q.Append([][]byte{[]byte(`{"event_kind":"session.closed","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)})
		}
		want := q.State().DroppedEvents
		if want == 0 {
			t.Fatal("setup: nothing dropped")
		}
		h.run(hook, map[string]any{"source": "startup", "reason": "other"})
		var carrier map[string]any
		for _, e := range h.recorded() {
			if e["event_kind"] == kind {
				carrier = e
			}
		}
		if carrier == nil || carrier["dropped_events"].(float64) != float64(want) {
			t.Fatalf("%s: %v, want %d", kind, carrier, want)
		}
		if q.State().DroppedEvents != 0 {
			t.Fatalf("%s: count not reset", kind)
		}
	}
}

// @spec: event-queue-and-backfill#dead-letter-retention
func TestHookPrunesOldDeadLetters(t *testing.T) {
	h := newHookEnv(t, true)
	dl := filepath.Join(h.data, "deadletter")
	os.MkdirAll(dl, 0o700)
	old := filepath.Join(dl, "old.json")
	os.WriteFile(old, []byte("{}"), 0o600)
	os.Chtimes(old, h.now.Add(-15*24*time.Hour), h.now.Add(-15*24*time.Hour))
	h.run("Stop", nil)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old dead letter kept")
	}
}

// @spec: repo-and-branch-identity#single-git-invocation
func TestOneGitProcessPerHook(t *testing.T) {
	h := newHookEnv(t, true)
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	real, _ := exec.LookPath("git")
	os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho x >> "+log+"\nexec "+real+" \"$@\"\n"), 0o755)
	old := gitident.GitCommand
	gitident.GitCommand = filepath.Join(bin, "git")
	defer func() { gitident.GitCommand = old }()
	for i, name := range claudecode.Hooks {
		h.run(name, map[string]any{"source": "startup", "reason": "other"})
		b, _ := os.ReadFile(log)
		if n := strings.Count(string(b), "x"); n != i+1 {
			t.Fatalf("after %s: %d git processes for %d hooks", name, n, i+1)
		}
	}
}

// A dropped-event count is not lost when the event carrying it cannot be
// queued.
func TestDroppedCountSurvivesFailedEmit(t *testing.T) {
	h := newHookEnv(t, false)
	q := queue.New(h.data)
	q.SegmentMax, q.Cap = 100, 150
	for i := 0; i < 10; i++ {
		q.Append([][]byte{[]byte(`{"event_kind":"session.closed","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)})
	}
	before := q.State().DroppedEvents
	if before == 0 {
		t.Fatal("setup: nothing dropped")
	}
	qdir := filepath.Join(h.data, "queue")
	os.Chmod(qdir, 0o500) // appends fail
	h.run("SessionStart", map[string]any{"source": "startup"})
	os.Chmod(qdir, 0o700)
	if got := q.State().DroppedEvents; got != before {
		t.Fatalf("dropped count %d, want %d", got, before)
	}
}

// A busy cursor lock delays the transcript tail but never loses the
// lifecycle events.
func TestBusyCursorLockStillEmitsLifecycleEvents(t *testing.T) {
	h := newHookEnv(t, true)
	unlock, err := cursor.Open(h.data).Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	old := claudecode.CursorWait
	claudecode.CursorWait = 50 * time.Millisecond
	defer func() { claudecode.CursorWait = old }()
	h.run("SessionStart", map[string]any{"source": "startup"})
	h.run("SessionEnd", map[string]any{"reason": "other"})
	if k := strings.Join(kinds(h.recorded()), ","); k != "session.opened,session.closed" {
		t.Fatalf("recorded %s", k)
	}
}

// A fork's transcript starts with copies of its parent's lines, which the
// parent's hooks already sent. The fork sends only the turns after its
// start, so the server never sees the parent's turns under the fork's id.
// @spec: claude-code-transcript-parser#fork-skips-copied-lines
func TestForkSkipsTurnsCopiedFromItsParent(t *testing.T) {
	h := newHookEnv(t, true)
	// The fixture's lines all predate h.now: they stand for the copies.
	if err := h.run("SessionStart", map[string]any{"source": "fork"}); err != nil {
		t.Fatal(err)
	}
	if err := h.run("Stop", nil); err != nil {
		t.Fatal(err)
	}
	if got := kinds(h.recorded()); !reflect.DeepEqual(got, []string{"session.opened"}) {
		t.Fatalf("after the copies, recorded %v", got)
	}
	h.appendLines(`{"type":"assistant","uuid":"f1","sessionId":"fixture-session","timestamp":"2026-10-25T12:00:05.000Z","version":"2.1.310","requestId":"req_F","message":{"id":"msg_F","model":"claude-opus-5-5","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":0}}}`)
	if err := h.run("Stop", nil); err != nil {
		t.Fatal(err)
	}
	evs := h.recorded()
	if got := kinds(evs); !reflect.DeepEqual(got, []string{"session.opened", "session.turn"}) {
		t.Fatalf("recorded %v", got)
	}
	if evs[1]["message_id"] != "msg_F" {
		t.Fatalf("turn %v, want the fork's own msg_F", evs[1]["message_id"])
	}
}

// Claude Code may write a fork's transcript only after its SessionStart, and
// another session's hook can prune cursors in between; the fork's start is
// kept until its transcript appears.
// @spec: claude-code-transcript-parser#fork-skips-copied-lines
func TestForkStartSurvivesUntilItsTranscriptIsWritten(t *testing.T) {
	h := newHookEnv(t, true)
	copies, err := os.ReadFile(h.transcript)
	if err != nil {
		t.Fatal(err)
	}
	fork := filepath.Join(filepath.Dir(h.transcript), "fork-session.jsonl")
	// A pending fork's start is kept for a day of wall-clock time.
	h.now = time.Now().UTC()
	if err := h.run("SessionStart", map[string]any{"source": "fork", "session_id": "fork-session", "transcript_path": fork}); err != nil {
		t.Fatal(err)
	}
	// Another session's Stop rewrites the cursor file. It has no turns, so
	// the fork's copies, if sent, would be the first the server stores.
	other := filepath.Join(filepath.Dir(h.transcript), "other-session.jsonl")
	if err := os.WriteFile(other, []byte(`{"type":"user","sessionId":"other-session","timestamp":"2026-10-25T11:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.run("Stop", map[string]any{"session_id": "other-session", "transcript_path": other}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fork, copies, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.run("Stop", map[string]any{"session_id": "fork-session", "transcript_path": fork}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(h.recorded()); !reflect.DeepEqual(got, []string{"session.opened"}) {
		t.Fatalf("the fork sent its copies: %v", got)
	}
}
