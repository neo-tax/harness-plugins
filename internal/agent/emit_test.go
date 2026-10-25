package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/agent"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

func queued(t *testing.T, dir string) []map[string]any {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "queue", "*.jsonl"))
	var out []map[string]any
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
	}
	return out
}

// An adapter that hands the agent an over-full event cannot get the extra
// field onto disk: the allowlist runs between the adapter and the queue.
// @spec: field-allowlist#enforced-below-adapters
// @spec: field-allowlist#unknown-field-dropped-and-counted
func TestEmitAppliesAllowlistBeforeQueuing(t *testing.T) {
	dir := t.TempDir()
	a := agent.New(dir)
	type leaky struct {
		contract.Closed
		Cwd   string `json:"cwd"`
		Title string `json:"title"`
	}
	ev := leaky{
		Closed: contract.NewClosed(contract.Envelope{ContractVersion: contract.Version, DeviceID: "d", Harness: "claude-code", AgentVersion: "v", SessionID: "s", SentAt: time.Now()},
			contract.ClosedFields{EndedAt: time.Now()}),
		Cwd: "/Users/alice/acme", Title: "Fix the Acme billing bug",
	}
	if err := a.Emit(ev); err != nil {
		t.Fatal(err)
	}
	got := queued(t, dir)
	if len(got) != 1 {
		t.Fatalf("queued %d", len(got))
	}
	if _, ok := got[0]["cwd"]; ok {
		t.Error("cwd queued")
	}
	if _, ok := got[0]["title"]; ok {
		t.Error("title queued")
	}
	b, _ := json.Marshal(got[0])
	if err := contract.ValidateEvent(b); err != nil {
		t.Errorf("queued event invalid: %v", err)
	}
	st := queue.New(dir).State()
	if st.DroppedFields["cwd"] != 1 || st.DroppedFields["title"] != 1 {
		t.Errorf("diagnostics %v", st.DroppedFields)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if strings.Contains(string(raw), "acme") || strings.Contains(string(raw), "Acme") {
		t.Error("diagnostics carry a value")
	}
}

func TestEmitDropsUnknownKind(t *testing.T) {
	dir := t.TempDir()
	a := agent.New(dir)
	if err := a.Emit(map[string]any{"event_kind": "session.paused"}); err != nil {
		t.Fatal(err)
	}
	if got := queued(t, dir); len(got) != 0 {
		t.Fatalf("queued %v", got)
	}
	if queue.New(dir).State().DroppedFields["event_kind"] != 1 {
		t.Error("unknown kind not counted")
	}
}

// Large integers survive the round trip through the allowlist unchanged.
func TestEmitPreservesNumbers(t *testing.T) {
	dir := t.TempDir()
	a := agent.New(dir)
	big := int64(9007199254740993)
	ev := contract.NewTurn(contract.Envelope{ContractVersion: "1", DeviceID: "d", Harness: "h", AgentVersion: "v", SessionID: "s", SentAt: time.Now()},
		contract.TurnFields{MessageID: "m", Timestamp: time.Now(), Usage: contract.Usage{Input: &big, Source: "transcript"}})
	a.Emit(ev)
	paths, _ := filepath.Glob(filepath.Join(dir, "queue", "*.jsonl"))
	b, _ := os.ReadFile(paths[0])
	if !strings.Contains(string(b), "9007199254740993") {
		t.Fatalf("number changed: %s", b)
	}
}
