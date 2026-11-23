package claudecode_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

const (
	namedID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	titledID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	untitleID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// sessionsFixture lays out three sessions, newest first: one the engineer
// renamed whose events are queued, one with regenerated titles the agent has
// read, and one with no title the agent has not read. A file not named as a
// session id sits beside them.
func sessionsFixture(t *testing.T) (configDir, dataDir string) {
	t.Helper()
	configDir, dataDir = t.TempDir(), t.TempDir()
	proj := filepath.Join(configDir, "projects", "-some-project")
	os.MkdirAll(proj, 0o700)
	start := func(branch string) string {
		return `{"type":"user","timestamp":"2026-11-20T09:00:00Z","cwd":"/x","gitBranch":"` + branch + `","version":"2.1.330","message":{"content":"hi"}}` + "\n"
	}
	files := map[string]string{
		namedID: start("feat-a") +
			`{"type":"ai-title","aiTitle":"Generated A","sessionId":"` + namedID + `"}` + "\n" +
			`{"type":"agent-name","agentName":"renamed-a","sessionId":"` + namedID + `"}` + "\n" +
			`{"type":"ai-title","aiTitle":"Generated A again","sessionId":"` + namedID + `"}` + "\n",
		titledID: start("feat-b") +
			`{"type":"ai-title","aiTitle":"First B","sessionId":"` + titledID + `"}` + "\n" +
			`{"type":"user","timestamp":"2026-11-20T09:01:00Z","message":{"content":"the words ai-title appear in a prompt"}}` + "\n" +
			`{"type":"ai-title","aiTitle":"Latest B","sessionId":"` + titledID + `"}` + "\n",
		untitleID: start("main"),
		"notes":   start("main"),
	}
	now := time.Now()
	for i, id := range []string{namedID, titledID, untitleID, "notes"} {
		p := filepath.Join(proj, id+".jsonl")
		os.WriteFile(p, []byte(files[id]), 0o600)
		mod := now.Add(-time.Duration(i) * time.Hour)
		os.Chtimes(p, mod, mod)
	}
	store := cursor.Open(dataDir)
	store.Put(filepath.Join(proj, namedID+".jsonl"), cursor.Cursor{Offset: 1})
	store.Put(filepath.Join(proj, titledID+".jsonl"), cursor.Cursor{Offset: 1})
	if err := queue.New(dataDir).Append([][]byte{[]byte(`{"event_kind":"session.turn","session_id":"` + namedID + `"}`)}); err != nil {
		t.Fatal(err)
	}
	return configDir, dataDir
}

// @spec: session-list#sessions-same-scope-as-backfill
// @spec: session-list#title-precedence
// @spec: session-list#report-status
func TestListSessions(t *testing.T) {
	configDir, dataDir := sessionsFixture(t)
	got := claudecode.ListSessions(claudecode.ListOptions{DataDir: dataDir, ConfigDir: configDir})
	want := []claudecode.SessionSummary{
		{SessionID: namedID, Status: claudecode.StatusQueued, Branch: "feat-a", Title: "renamed-a"},
		{SessionID: titledID, Status: claudecode.StatusSent, Branch: "feat-b", Title: "Latest B"},
		{SessionID: untitleID, Status: claudecode.StatusUnread, Branch: "main", Title: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d sessions: %+v", len(got), got)
	}
	for i := range want {
		g := got[i]
		g.LastActive = time.Time{}
		if g != want[i] {
			t.Errorf("session %d: got %+v, want %+v", i, g, want[i])
		}
	}
	if !got[0].LastActive.After(got[1].LastActive) {
		t.Errorf("not newest first: %v, %v", got[0].LastActive, got[1].LastActive)
	}

	limited := claudecode.ListSessions(claudecode.ListOptions{DataDir: dataDir, ConfigDir: configDir, Limit: 2})
	if len(limited) != 2 || limited[1].SessionID != titledID {
		t.Errorf("limit 2 listed %+v", limited)
	}
}

// Listing changes nothing in the plugin data directory, so no title can
// land in a queue file, a cursor, backfill.json, or a log line.
//
// @spec: session-list#sessions-read-only
// @spec: session-list#title-local-only
func TestListSessionsLeavesDataDirUnchanged(t *testing.T) {
	configDir, dataDir := sessionsFixture(t)
	before := snapshot(t, dataDir)
	claudecode.ListSessions(claudecode.ListOptions{DataDir: dataDir, ConfigDir: configDir})
	after := snapshot(t, dataDir)
	if len(before) != len(after) {
		t.Fatalf("files changed: %v -> %v", keys(before), keys(after))
	}
	for p, b := range before {
		if after[p] != b {
			t.Errorf("%s changed", p)
		}
	}
	for p, b := range after {
		for _, title := range []string{"renamed-a", "Latest B", "Generated A"} {
			if strings.Contains(b, title) {
				t.Errorf("%s holds title %q", p, title)
			}
		}
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
