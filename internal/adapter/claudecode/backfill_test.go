package claudecode_test

import (
	"bytes"
	"context"
	"errors"
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
	old := filepath.Join(proj, "old-session.jsonl")
	os.WriteFile(old, src, 0o600)
	os.MkdirAll(filepath.Join(proj, "old-session", "subagents"), 0o700)
	os.WriteFile(filepath.Join(proj, "old-session", "subagents", "agent-sub1.jsonl"), sub, 0o600)
	newer := filepath.Join(proj, "new-session.jsonl")
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
	if strings.Join(sessions, ",") != "new-session,old-session" {
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
	main := filepath.Join(root, "projects", "-some-project", "new-session.jsonl")
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
	h.transcript = filepath.Join(root, "projects", "-some-project", "new-session.jsonl")
	h.run("Stop", map[string]any{"session_id": "new-session"})
	if got := len(h.recorded()); got != before {
		t.Fatalf("hook re-sent %d events", got-before)
	}
}
