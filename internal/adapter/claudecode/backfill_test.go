package claudecode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/cursor"
)

// projects lays out two sessions under a Claude Code projects directory:
// an older one with subagents and a newer plain one.
func projects(t *testing.T, repo string) string {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "-some-project")
	os.MkdirAll(proj, 0o700)
	src, _ := os.ReadFile("testdata/fixtures/synthetic-2.1.310/fixture-session.jsonl")
	src = bytes.ReplaceAll(src, []byte(`"cwd":"/Users/someone/client-acme"`), []byte(`"cwd":"`+repo+`"`))
	sub, _ := os.ReadFile("testdata/fixtures/synthetic-2.1.310/fixture-session/subagents/agent-sub1.jsonl")
	old := filepath.Join(proj, "11111111-1111-4111-8111-111111111111.jsonl")
	os.WriteFile(old, src, 0o600)
	os.MkdirAll(filepath.Join(proj, "11111111-1111-4111-8111-111111111111", "subagents"), 0o700)
	os.WriteFile(filepath.Join(proj, "11111111-1111-4111-8111-111111111111", "subagents", "agent-sub1.jsonl"), sub, 0o600)
	newer := filepath.Join(proj, "22222222-2222-4222-8222-222222222222.jsonl")
	os.WriteFile(newer, []byte(`{"type":"user","timestamp":"2026-10-26T09:00:00Z","cwd":"`+repo+`","gitBranch":"b2","entrypoint":"sdk-ts","version":"2.1.310","message":{"content":"hi"}}
{"type":"assistant","timestamp":"2026-10-26T09:00:01Z","gitBranch":"b2","version":"2.1.310","requestId":"req_N","message":{"id":"msg_N","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}
`), 0o600)
	os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	return root
}

func (h *hookEnv) backfill(configDir string) (string, error) {
	var out bytes.Buffer
	err := claudecode.Backfill(context.Background(), claudecode.BackfillOptions{
		DataDir: h.data, ConfigDir: configDir, AgentVersion: "0.1.0", Now: func() time.Time { return h.now },
		HTTP: &http.Client{Timeout: 5 * time.Second}, Out: &out,
	})
	return out.String(), err
}

// @spec: event-queue-and-backfill#backfill-scope
func TestBackfillEmitsEveryTranscriptNewestFirst(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	out, err := h.backfill(root)
	if err != nil {
		t.Fatal(err)
	}
	got := h.recorded()
	var sessions []string
	for _, e := range got {
		if e["event_kind"] == "session.opened" {
			sessions = append(sessions, e["session_id"].(string))
		}
	}
	if strings.Join(sessions, ",") != "22222222-2222-4222-8222-222222222222,11111111-1111-4111-8111-111111111111" {
		t.Fatalf("opened order %v", sessions)
	}
	if k := strings.Join(kinds(got), ","); k != "session.opened,session.turn,session.opened,session.turn,session.turn,session.pr_opened,session.turn,session.turn" {
		t.Fatalf("recorded %s", k)
	}
	newOpened := got[0]
	if newOpened["started_at"] != "2026-10-26T09:00:00Z" || newOpened["entrypoint"] != "sdk-ts" || newOpened["branch"] != "b2" ||
		newOpened["source"] != nil || newOpened["head_sha"] != nil {
		t.Errorf("opened %v", newOpened)
	}
	if r, _ := newOpened["repo"].(map[string]any); r["name"] != "harness-plugins" {
		t.Errorf("repo %v", newOpened["repo"])
	}
	for _, e := range got {
		if e["event_kind"] == "session.turn" && e["head_sha"] != nil {
			t.Errorf("backfilled turn carries today's head_sha: %v", e)
		}
	}
	if !strings.Contains(out, "2 sessions") {
		t.Errorf("output %q", out)
	}
}

// @spec: event-queue-and-backfill#backfill-lock
func TestSecondBackfillExits(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	l := flock.New(filepath.Join(h.data, "backfill.lock"))
	if ok, _ := l.TryLock(); !ok {
		t.Fatal("setup")
	}
	defer l.Unlock()
	if _, err := h.backfill(root); !errors.Is(err, claudecode.ErrBackfillRunning) {
		t.Fatalf("err %v", err)
	}
	if len(h.recorded()) != 0 {
		t.Fatal("ran anyway")
	}
}

// Re-running resets every cursor and resends; the server's idempotency key
// discards what it already has.
// @spec: event-queue-and-backfill#backfill-command
// @spec: event-queue-and-backfill#backfill-idempotent-server-side
func TestRerunResetsCursorsAndResends(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	h.backfill(root)
	first := len(h.recorded())
	main := filepath.Join(root, "projects", "-some-project", "22222222-2222-4222-8222-222222222222.jsonl")
	if c := cursor.Open(h.data).Get(main); c.Offset == 0 {
		t.Fatal("cursor not advanced by backfill")
	}
	h.backfill(root)
	if got := len(h.recorded()); got != first {
		t.Fatalf("recorded %d after rerun, want %d (duplicates discarded)", got, first)
	}
}

// Hooks after a backfill do not re-emit what it read.
func TestHookAfterBackfillSendsOnlyNewTurns(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	h.backfill(root)
	before := len(h.recorded())
	h.transcript = filepath.Join(root, "projects", "-some-project", "22222222-2222-4222-8222-222222222222.jsonl")
	h.run("Stop", map[string]any{"session_id": "22222222-2222-4222-8222-222222222222"})
	if got := len(h.recorded()); got != before {
		t.Fatalf("hook re-sent %d events", got-before)
	}
}

// A device that cannot send does not backfill: its queue would fill with
// history and the cap would evict the newest sessions first.
func TestBackfillRequiresAnEnrolledDevice(t *testing.T) {
	h := newHookEnv(t, false)
	root := projects(t, h.repo)
	if _, err := h.backfill(root); !errors.Is(err, claudecode.ErrCannotSend) {
		t.Fatalf("not enrolled: %v", err)
	}
	paths, _ := filepath.Glob(filepath.Join(h.data, "queue", "*.jsonl"))
	if len(paths) != 0 {
		t.Fatal("queued while unable to send")
	}
}

// manySessions lays out n one-turn sessions under a projects directory,
// session-000 newest, each a minute older than the last.
func manySessions(t *testing.T, repo string, n int) string {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "-many")
	os.MkdirAll(proj, 0o700)
	base := time.Now().Add(-time.Hour)
	for i := range n {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		path := filepath.Join(proj, id+".jsonl")
		os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-10-26T09:00:00Z","cwd":"`+repo+`","gitBranch":"b","entrypoint":"cli","version":"2.1.310","message":{"content":"hi"}}
{"type":"assistant","timestamp":"2026-10-26T09:00:01Z","gitBranch":"b","version":"2.1.310","requestId":"req_`+id+`","message":{"id":"msg_`+id+`","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}
`), 0o600)
		mod := base.Add(-time.Duration(i) * time.Minute)
		os.Chtimes(path, mod, mod)
	}
	return root
}

// openedSessions counts the session.opened events recorded per session.
func openedSessions(evs []map[string]any) map[string]int {
	out := map[string]int{}
	for _, e := range evs {
		if e["event_kind"] == "session.opened" {
			out[e["session_id"].(string)]++
		}
	}
	return out
}

func fault(t *testing.T, h *hookEnv, mode string, count int) {
	t.Helper()
	resp, err := http.Post(h.srv.URL+"/_fake/faults", "application/json", strings.NewReader(fmt.Sprintf(`{"mode":%q,"count":%d}`, mode, count)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// backfillWaiting runs a backfill whose sleeps advance the hook env's clock
// instead of waiting, and returns its output and how long it slept.
func (h *hookEnv) backfillWaiting(configDir string, limit int64, budget time.Duration, restart bool) (string, time.Duration, error) {
	var out bytes.Buffer
	var slept time.Duration
	err := claudecode.Backfill(context.Background(), claudecode.BackfillOptions{
		DataDir: h.data, ConfigDir: configDir, AgentVersion: "v", Now: func() time.Time { return h.now },
		HTTP: &http.Client{Timeout: 5 * time.Second}, Out: &out, QueueLimit: limit, WaitBudget: budget, Restart: restart,
		Sleep: func(_ context.Context, d time.Duration) error { h.now = h.now.Add(d); slept += d; return nil },
	})
	return out.String(), slept, err
}

// One failed ingest request puts the queue into backoff. The backfill waits
// it out and keeps going rather than stopping with most sessions unread.
// @spec: event-queue-and-backfill#backfill-waits-out-backoff
func TestBackfillWaitsOutBackoff(t *testing.T) {
	h := newHookEnv(t, true)
	const n = 150
	root := manySessions(t, h.repo, n)
	fault(t, h, "server_error", 1)
	out, slept, err := h.backfillWaiting(root, 2000, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if slept < 30*time.Second {
		t.Errorf("slept %s, want at least the 30s backoff", slept)
	}
	opened := openedSessions(h.recorded())
	if len(opened) != n {
		t.Fatalf("sent %d of %d sessions; output %q", len(opened), n, out)
	}
	want := fmt.Sprintf("Backfilled %d sessions", n)
	if !strings.Contains(out, want) || strings.Contains(out, "paused") {
		t.Errorf("output %q", out)
	}
	if st, _ := claudecode.ReadBackfillStatus(h.data); st.Paused || st.Resume != nil || st.Sessions != n {
		t.Errorf("status %+v", st)
	}
}

// A backfill that cannot drain within its wait budget pauses with the queue
// under its limit and records where to resume.
// @spec: event-queue-and-backfill#backfill-waits-out-backoff
func TestBackfillPausesWhenWaitBudgetIsSpent(t *testing.T) {
	h := newHookEnv(t, true)
	root := manySessions(t, h.repo, 20)
	fault(t, h, "server_error", 1000)
	out, slept, err := h.backfillWaiting(root, 1, time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	// 30s, then the doubled 60s would pass the one-minute budget.
	if slept != 30*time.Second {
		t.Errorf("slept %s", slept)
	}
	if !strings.Contains(out, "paused") || !strings.Contains(out, "1 sessions") || !strings.Contains(out, "again later") {
		t.Fatalf("output %q", out)
	}
	st, _ := claudecode.ReadBackfillStatus(h.data)
	if !st.Paused || st.FinishedAt.IsZero() || st.Sessions != 1 || st.Resume == nil || st.Resume.AtBoundary != 1 {
		t.Fatalf("status %+v", st)
	}
}

// A backfill run after a paused one continues after the transcripts the
// paused run queued: it neither re-reads them nor resets their cursors,
// and reads every one after the resume point from the start.
// @spec: event-queue-and-backfill#backfill-resumes-after-pause
func TestBackfillResumesAfterPause(t *testing.T) {
	h := newHookEnv(t, true)
	const n = 40
	root := manySessions(t, h.repo, n)
	proj := filepath.Join(root, "projects", "-many")
	fault(t, h, "server_error", 1000)
	h.backfillWaiting(root, 1500, time.Minute, false)
	st, _ := claudecode.ReadBackfillStatus(h.data)
	if !st.Paused || st.Resume == nil || st.Sessions == 0 || st.Sessions >= n {
		t.Fatalf("setup: status %+v", st)
	}
	queued := st.Sessions
	// Rewrite the newest session, which the paused run queued, keeping its
	// modification time: a resumed run that re-read it would send msg_X.
	newest := filepath.Join(proj, "00000000-0000-4000-8000-000000000000.jsonl")
	fi, _ := os.Stat(newest)
	os.WriteFile(newest, []byte(`{"type":"user","timestamp":"2026-10-26T09:00:00Z","cwd":"`+h.repo+`","version":"2.1.310","message":{"content":"x"}}
{"type":"assistant","timestamp":"2026-10-26T09:00:01Z","version":"2.1.310","requestId":"req_X","message":{"id":"msg_X","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}
`), 0o600)
	os.Chtimes(newest, fi.ModTime(), fi.ModTime())
	cursorBefore := cursor.Open(h.data).Get(filepath.Join(proj, "00000000-0000-4000-8000-000000000001.jsonl"))
	if cursorBefore.Offset == 0 {
		t.Fatal("setup: paused run recorded no cursor for a queued transcript")
	}

	fault(t, h, "", 0)
	h.now = h.now.Add(2 * time.Hour) // past the backoff
	out, _, err := h.backfillWaiting(root, 1500, time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Resuming") || !strings.Contains(out, fmt.Sprintf("Backfilled %d sessions", n)) {
		t.Errorf("output %q", out)
	}
	got := h.recorded()
	opened := openedSessions(got)
	if len(opened) != n {
		t.Fatalf("sent %d of %d sessions after resuming (%d queued before the pause)", len(opened), n, queued)
	}
	for _, e := range got {
		if e["message_id"] == "msg_X" {
			t.Fatal("resumed run re-read a transcript the paused run queued")
		}
	}
	if c := cursor.Open(h.data).Get(filepath.Join(proj, "00000000-0000-4000-8000-000000000001.jsonl")); c != cursorBefore {
		t.Errorf("queued transcript's cursor changed: %+v -> %+v", cursorBefore, c)
	}
	if c := cursor.Open(h.data).Get(filepath.Join(proj, fmt.Sprintf("00000000-0000-4000-8000-%012d.jsonl", n-1))); c.Offset == 0 {
		t.Error("oldest transcript's cursor not advanced by the resumed run")
	}
	if st, _ := claudecode.ReadBackfillStatus(h.data); st.Paused || st.Resume != nil || st.Sessions != n {
		t.Errorf("status %+v", st)
	}
}

// A backfill killed partway leaves the resume point its last batch
// recorded, and the next run continues from there.
// @spec: event-queue-and-backfill#backfill-resumes-after-pause
func TestBackfillResumesAfterBeingKilled(t *testing.T) {
	h := newHookEnv(t, true)
	const n = 20
	root := manySessions(t, h.repo, n)
	fault(t, h, "server_error", 1000)
	h.backfillWaiting(root, 1500, time.Minute, false)
	st, _ := claudecode.ReadBackfillStatus(h.data)
	if st.Resume == nil {
		t.Fatalf("setup: status %+v", st)
	}
	// What a run killed after that batch leaves: in progress, not paused.
	st.Paused, st.FinishedAt = false, time.Time{}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(h.data, "backfill.json"), b, 0o600)
	newest := filepath.Join(root, "projects", "-many", "00000000-0000-4000-8000-000000000000.jsonl")
	fi, _ := os.Stat(newest)
	os.WriteFile(newest, []byte(`{"type":"user","timestamp":"2026-10-26T09:00:00Z","cwd":"`+h.repo+`","version":"2.1.310","message":{"content":"x"}}
{"type":"assistant","timestamp":"2026-10-26T09:00:01Z","version":"2.1.310","requestId":"req_X","message":{"id":"msg_X","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}
`), 0o600)
	os.Chtimes(newest, fi.ModTime(), fi.ModTime())
	fault(t, h, "", 0)
	h.now = h.now.Add(2 * time.Hour)
	if _, _, err := h.backfillWaiting(root, 1500, time.Minute, false); err != nil {
		t.Fatal(err)
	}
	got := h.recorded()
	for _, e := range got {
		if e["message_id"] == "msg_X" {
			t.Fatal("resumed run re-read a queued transcript")
		}
	}
	if len(openedSessions(got)) != n {
		t.Fatalf("sent %d of %d", len(openedSessions(got)), n)
	}
}

// Restart ignores a paused backfill's resume point and reads every
// transcript again from the newest.
// @spec: event-queue-and-backfill#backfill-resumes-after-pause
// @spec: event-queue-and-backfill#backfill-command
func TestBackfillRestartIgnoresResumePoint(t *testing.T) {
	h := newHookEnv(t, true)
	const n = 10
	root := manySessions(t, h.repo, n)
	fault(t, h, "server_error", 1000)
	h.backfillWaiting(root, 1, time.Minute, false)
	if st, _ := claudecode.ReadBackfillStatus(h.data); st.Resume == nil {
		t.Fatalf("setup: status %+v", st)
	}
	fault(t, h, "", 0)
	h.now = h.now.Add(2 * time.Hour)
	out, _, err := h.backfillWaiting(root, 1, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Resuming") || !strings.Contains(out, fmt.Sprintf("Backfilled %d sessions", n)) {
		t.Errorf("output %q", out)
	}
	if got := len(openedSessions(h.recorded())); got != n {
		t.Fatalf("sent %d of %d", got, n)
	}
}

// Transcripts sharing a modification time resume by their count in path
// order, so none is skipped or read twice.
// @spec: event-queue-and-backfill#backfill-resumes-after-pause
func TestBackfillResumesWithinTiedModificationTimes(t *testing.T) {
	h := newHookEnv(t, true)
	const n = 12
	root := manySessions(t, h.repo, n)
	same := time.Now().Add(-time.Hour)
	paths, _ := filepath.Glob(filepath.Join(root, "projects", "-many", "*.jsonl"))
	for _, p := range paths {
		os.Chtimes(p, same, same)
	}
	fault(t, h, "server_error", 1000)
	h.backfillWaiting(root, 1500, time.Minute, false)
	st, _ := claudecode.ReadBackfillStatus(h.data)
	if st.Resume == nil || st.Resume.AtBoundary == 0 || st.Resume.AtBoundary >= n {
		t.Fatalf("setup: status %+v", st)
	}
	fault(t, h, "", 0)
	h.now = h.now.Add(2 * time.Hour)
	if _, _, err := h.backfillWaiting(root, 1500, time.Minute, false); err != nil {
		t.Fatal(err)
	}
	opened := openedSessions(h.recorded())
	if len(opened) != n {
		t.Fatalf("sent %d of %d", len(opened), n)
	}
	if st, _ := claudecode.ReadBackfillStatus(h.data); st.Sessions != n {
		t.Errorf("counted %d sessions, want %d", st.Sessions, n)
	}
}

// One unreadable subagent transcript does not cost the session its other
// events.
func TestBackfillKeepsSessionWhenASubagentIsUnreadable(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	bad := filepath.Join(root, "projects", "-some-project", "11111111-1111-4111-8111-111111111111", "subagents", "agent-bad.jsonl")
	os.MkdirAll(bad, 0o700) // a directory where a file is expected: reading it fails
	h.backfill(root)
	var oldOpened bool
	for _, e := range h.recorded() {
		if e["session_id"] == "11111111-1111-4111-8111-111111111111" && e["event_kind"] == "session.opened" {
			oldOpened = true
		}
	}
	if !oldOpened {
		t.Fatal("session skipped")
	}
}

// A backfill records when it started and finished, for diagnosis and so a
// caller can wait for a detached one.
func TestBackfillRecordsStatus(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	h.backfill(root)
	st, err := claudecode.ReadBackfillStatus(h.data)
	if err != nil || st.StartedAt.IsZero() || st.FinishedAt.IsZero() || st.Sessions != 2 {
		t.Fatalf("%+v %v", st, err)
	}
}

// A key revoked mid-backfill stops it with the reason, instead of filling
// the queue.
func TestBackfillStopsWhenKeyRejected(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	http.Post(h.srv.URL+"/_fake/faults", "application/json", strings.NewReader(`{"mode":"unauthorized","count":100}`))
	err := claudecode.Backfill(context.Background(), claudecode.BackfillOptions{
		DataDir: h.data, ConfigDir: root, AgentVersion: "v", Now: func() time.Time { return h.now },
		HTTP: &http.Client{Timeout: 5 * time.Second}, QueueLimit: 1,
	})
	if !errors.Is(err, claudecode.ErrCannotSend) {
		t.Fatalf("err %v", err)
	}
	st, _ := claudecode.ReadBackfillStatus(h.data)
	if st.FinishedAt.IsZero() || !st.Paused || st.Sessions != 1 {
		t.Fatalf("status %+v", st)
	}
}

// Older Claude Code builds wrote subagent transcripts beside the main ones
// as agent-<id>.jsonl. They are not sessions: a backfill never opens one or
// counts its turns as main-thread turns.
// @spec: session-event-wire-contract#subagent-turns-belong-to-parent
// @spec: event-queue-and-backfill#backfill-scope
func TestBackfillSkipsLegacySubagentTranscripts(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	legacy := filepath.Join(root, "projects", "-some-project", "agent-legacy1.jsonl")
	os.WriteFile(legacy, []byte(`{"type":"user","isSidechain":true,"sessionId":"22222222-2222-4222-8222-222222222222","agentId":"legacy1","timestamp":"2026-10-26T09:00:02Z","cwd":"`+h.repo+`","version":"2.1.310","message":{"content":"x"}}
{"type":"assistant","isSidechain":true,"sessionId":"22222222-2222-4222-8222-222222222222","agentId":"legacy1","timestamp":"2026-10-26T09:00:03Z","version":"2.1.310","requestId":"req_L","message":{"id":"msg_L","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}
`), 0o600)
	out, err := h.backfill(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range h.recorded() {
		if id, _ := e["session_id"].(string); strings.HasPrefix(id, "agent-") {
			t.Fatalf("legacy subagent transcript backfilled as a session: %v", e)
		}
		if e["message_id"] == "msg_L" && e["subagent_id"] == nil {
			t.Fatalf("legacy subagent turn counted as a main-thread turn: %v", e)
		}
	}
	if !strings.Contains(out, "2 sessions") {
		t.Errorf("output %q", out)
	}
}

// A transcript's file name becomes its session id, so a file under projects/
// that Claude Code did not name, such as a hand-made backup, is skipped and
// its name never reaches an event.
// @spec: event-queue-and-backfill#backfill-session-id-from-harness
func TestBackfillSkipsTranscriptsNotNamedBySessionID(t *testing.T) {
	h := newHookEnv(t, true)
	root := projects(t, h.repo)
	src, _ := os.ReadFile(filepath.Join(root, "projects", "-some-project", "22222222-2222-4222-8222-222222222222.jsonl"))
	for _, name := range []string{"acme-migration-backup.jsonl", "22222222-2222-4222-8222-22222222222Z.jsonl"} {
		os.WriteFile(filepath.Join(root, "projects", "-some-project", name), src, 0o600)
	}
	out, err := h.backfill(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range h.recorded() {
		if id, _ := e["session_id"].(string); id == "acme-migration-backup" || strings.HasSuffix(id, "Z") {
			t.Fatalf("a file name was sent as a session id: %v", e)
		}
	}
	if !strings.Contains(out, "2 sessions") {
		t.Errorf("output %q", out)
	}
}
