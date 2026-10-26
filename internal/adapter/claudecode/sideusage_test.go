package claudecode_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
)

// The synthetic fixture's turns, per model (main thread and subagent):
//
//	claude-opus-5-5    input 10  output 20  cache_write_5m 100  cache_write_1h 200  cache_read 4000
//	claude-haiku-5-5   input 5   output 6   cache_read 7
//	claude-sonnet-5-5  input 1   output 2   cache_write_5m 3    cache_read 4

func costState(startTime int64, models map[string]map[string]int64) string {
	usage := map[string]any{}
	for m, counts := range models {
		usage[m] = counts
	}
	b, _ := json.Marshal(map[string]any{
		"type": "cost-state", "sessionId": "fixture-session", "startTime": startTime,
		"totalCostUSD": 1.5, "totalLinesAdded": 3, "modelUsage": usage,
	})
	return string(b)
}

func (h *hookEnv) appendLines(lines ...string) {
	h.t.Helper()
	f, err := os.OpenFile(h.transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

func (h *hookEnv) closed() map[string]any {
	h.t.Helper()
	for _, e := range h.recorded() {
		if e["event_kind"] == "session.closed" {
			return e
		}
	}
	h.t.Fatal("no session.closed")
	return nil
}

func entry(model string, input, output, cw5m, cw1h, cacheRead float64) map[string]any {
	return map[string]any{"model": model, "input": input, "output": output, "cache_write_5m": cw5m, "cache_write_1h": cw1h, "cache_read": cacheRead}
}

// @spec: claude-code-transcript-parser#session-totals-from-cost-state
// @spec: claude-code-transcript-parser#side-usage-is-totals-minus-turns
// @spec: claude-code-transcript-parser#side-usage-cache-split
// @spec: session-event-wire-contract#closed-fields
// @spec: session-event-wire-contract#side-usage-shape
func TestSessionEndReportsSideUsage(t *testing.T) {
	h := newHookEnv(t, true)
	h.appendLines(
		// Process 1's tally: an earlier, smaller line, then its latest.
		costState(1000, map[string]map[string]int64{
			"claude-opus-5-5": {"inputTokens": 500, "outputTokens": 100},
		}),
		costState(1000, map[string]map[string]int64{
			"claude-opus-5-5": {"inputTokens": 1010, "outputTokens": 520, "cacheReadInputTokens": 9000,
				"cacheCreationInputTokens": 400, "cacheCreationEphemeral5mInputTokens": 150, "cacheCreationEphemeral1hInputTokens": 250},
			// Equal to its turns: nothing left over, so no entry.
			"claude-haiku-5-5": {"inputTokens": 5, "outputTokens": 6, "cacheReadInputTokens": 7},
			// Below its turns on some counts: those clamp to zero.
			"claude-sonnet-5-5": {"inputTokens": 1, "outputTokens": 50, "cacheCreationInputTokens": 3,
				"cacheCreationEphemeral5mInputTokens": 3, "cacheCreationEphemeral1hInputTokens": 0},
		}),
		// Process 2 opened the same session and kept its own tally, with no
		// per-TTL split.
		costState(2000, map[string]map[string]int64{
			"claude-opus-5-5": {"inputTokens": 100, "outputTokens": 30, "cacheReadInputTokens": 1000, "cacheCreationInputTokens": 50},
		}),
	)
	h.run("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	got := h.closed()["side_usage"]
	want := []any{
		// opus totals: input 1110, output 550, cache read 10000,
		// 5m 150+50, 1h 250; less its turns.
		entry("claude-opus-5-5", 1100, 530, 100, 50, 6000),
		entry("claude-sonnet-5-5", 0, 48, 0, 0, 0),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("side_usage:\n got  %v\n want %v", got, want)
	}
}

// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func TestSessionEndSideUsageNullWithoutCostState(t *testing.T) {
	defer setCostStateWait(50 * time.Millisecond)()
	h := newHookEnv(t, true)
	h.run("SessionEnd", map[string]any{"reason": "other"})
	c := h.closed()
	if v, ok := c["side_usage"]; !ok || v != nil {
		t.Fatalf("side_usage %v (present %v), want null", v, ok)
	}
}

// Session totals that cover only what the turns already report leave an
// empty list, not null: the totals were there.
// @spec: session-event-wire-contract#side-usage-shape
func TestSessionEndSideUsageEmptyWhenTurnsCoverTotals(t *testing.T) {
	h := newHookEnv(t, true)
	h.appendLines(costState(1, map[string]map[string]int64{
		"claude-haiku-5-5": {"inputTokens": 5, "outputTokens": 6, "cacheReadInputTokens": 7},
	}))
	h.run("SessionEnd", map[string]any{"reason": "other"})
	if got := h.closed()["side_usage"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("side_usage %v, want []", got)
	}
}

// Claude Code writes cost-state as it exits; a SessionEnd hook that reads
// the transcript first waits a bounded time for it.
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func TestSessionEndWaitsForCostState(t *testing.T) {
	defer setCostStateWait(5 * time.Second)()
	h := newHookEnv(t, true)
	go func() {
		time.Sleep(200 * time.Millisecond)
		h.appendLines(costState(1, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 1205, "outputTokens": 35}}))
	}()
	h.run("SessionEnd", map[string]any{"reason": "other"})
	want := []any{entry("claude-haiku-5-5", 1200, 29, 0, 0, 0)}
	if got := h.closed()["side_usage"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("side_usage %v, want %v", got, want)
	}
}

func (h *hookEnv) prependLines(lines ...string) {
	h.t.Helper()
	b, err := os.ReadFile(h.transcript)
	if err != nil {
		h.t.Fatal(err)
	}
	var head string
	for _, l := range lines {
		head += l + "\n"
	}
	if err := os.WriteFile(h.transcript, append([]byte(head), b...), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// A resumed session's transcript holds the cost-state line its earlier exit
// wrote, followed by the turns since. Those totals predate the turns, so
// subtracting the turns would under-report: with no newer line, side usage
// is unknown.
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func TestSessionEndIgnoresCostStateOlderThanTheLastTurn(t *testing.T) {
	defer setCostStateWait(50 * time.Millisecond)()
	h := newHookEnv(t, true)
	h.prependLines(costState(1, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 900}}))
	h.run("SessionEnd", map[string]any{"reason": "other"})
	if v := h.closed()["side_usage"]; v != nil {
		t.Fatalf("side_usage %v, want null", v)
	}
}

// The line this exit writes, arriving during the wait, is the one used.
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
// @spec: claude-code-transcript-parser#session-totals-from-cost-state
func TestSessionEndWaitsPastAnOlderCostState(t *testing.T) {
	defer setCostStateWait(5 * time.Second)()
	h := newHookEnv(t, true)
	h.prependLines(costState(1, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 900}}))
	go func() {
		time.Sleep(200 * time.Millisecond)
		h.appendLines(costState(1, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 1205, "outputTokens": 35}}))
	}()
	h.run("SessionEnd", map[string]any{"reason": "other"})
	want := []any{entry("claude-haiku-5-5", 1200, 29, 0, 0, 0)}
	if got := h.closed()["side_usage"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("side_usage %v, want %v", got, want)
	}
}

// A resumed session that exits with no new assistant message still wrote
// lines of its own after the earlier exit's totals, so those are stale too.
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func TestSessionEndWaitsPastTotalsFollowedByAnyLine(t *testing.T) {
	defer setCostStateWait(5 * time.Second)()
	h := newHookEnv(t, true)
	h.appendLines(
		costState(1, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 900}}),
		`{"type":"last-prompt","sessionId":"fixture-session"}`,
	)
	go func() {
		time.Sleep(200 * time.Millisecond)
		h.appendLines(costState(2, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 305}}))
	}()
	h.run("SessionEnd", map[string]any{"reason": "other"})
	// Tallies 1 and 2 sum to 1205 input, less the turns' 5.
	want := []any{entry("claude-haiku-5-5", 1200, 0, 0, 0, 0)}
	if got := h.closed()["side_usage"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("side_usage %v, want %v", got, want)
	}
}

// Claude Code can write an artifact-comment-monitor line after its exit
// totals; that does not make them stale.
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func TestSessionEndTotalsStayCurrentAfterExitLines(t *testing.T) {
	defer setCostStateWait(50 * time.Millisecond)()
	h := newHookEnv(t, true)
	h.appendLines(
		costState(1, map[string]map[string]int64{"claude-haiku-5-5": {"inputTokens": 905}}),
		`{"type":"artifact-comment-monitor","sessionId":"fixture-session"}`,
	)
	h.run("SessionEnd", map[string]any{"reason": "other"})
	want := []any{entry("claude-haiku-5-5", 900, 0, 0, 0, 0)}
	if got := h.closed()["side_usage"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("side_usage %v, want %v", got, want)
	}
}

func setCostStateWait(d time.Duration) func() {
	old := claudecode.CostStateWait
	claudecode.CostStateWait = d
	return func() { claudecode.CostStateWait = old }
}

// A real session with a title, an away recap, and a /compact. Its expected
// side usage is what a recording proxy logged for the three API requests no
// turn reported, independent of the transcript.
// @spec: claude-code-transcript-parser#side-usage-is-totals-minus-turns
// @spec: claude-code-transcript-parser#session-totals-from-cost-state
// @spec: claude-code-transcript-parser#fixture-driven
func TestSessionEndSideUsageMatchesTheAPI(t *testing.T) {
	h := newHookEnv(t, true)
	h.transcript, _ = copyFixture(t, "testdata/fixtures/real-2.1.310-side-usage")
	h.run("SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	want := []any{entry("claude-opus-5-5", 4076, 2956, 0, 935, 151837)}
	if got := h.closed()["side_usage"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("side_usage %v, want %v", got, want)
	}
}
