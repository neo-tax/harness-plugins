package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

// CostStateWait bounds how long SessionEnd waits for the cost-state line
// Claude Code writes as it exits.
var CostStateWait = 2 * time.Second

// costState is the subset of a cost-state line the adapter decodes: the
// session's per-model token totals for one process's tally.
type costState struct {
	Type       string                    `json:"type"`
	StartTime  int64                     `json:"startTime"`
	ModelUsage map[string]costStateModel `json:"modelUsage"`
}

type costStateModel struct {
	InputTokens         int64  `json:"inputTokens"`
	OutputTokens        int64  `json:"outputTokens"`
	CacheReadTokens     int64  `json:"cacheReadInputTokens"`
	CacheCreationTokens int64  `json:"cacheCreationInputTokens"`
	CacheCreation5m     *int64 `json:"cacheCreationEphemeral5mInputTokens"`
	CacheCreation1h     *int64 `json:"cacheCreationEphemeral1hInputTokens"`
}

// counts maps a model's totals to the contract's terms; without the per-TTL
// split, cache creation is the five-minute tier, as for turns.
//
// @spec: claude-code-transcript-parser#side-usage-cache-split
func (m costStateModel) counts() contract.SideUsage {
	u := contract.SideUsage{Input: m.InputTokens, Output: m.OutputTokens, CacheRead: m.CacheReadTokens}
	if m.CacheCreation5m == nil && m.CacheCreation1h == nil {
		u.CacheWrite5m = m.CacheCreationTokens
		return u
	}
	if m.CacheCreation5m != nil {
		u.CacheWrite5m = *m.CacheCreation5m
	}
	if m.CacheCreation1h != nil {
		u.CacheWrite1h = *m.CacheCreation1h
	}
	return u
}

// sessionTotals reads the main transcript's cost-state lines. Each process
// that opens a session keeps its own tally, identified by its start time,
// and each line holds that tally so far; the session's totals are the
// latest line of each tally, summed. ok is false when there is no line.
//
// @spec: claude-code-transcript-parser#session-totals-from-cost-state
func sessionTotals(path string) (map[string]contract.SideUsage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	tallies := map[int64]costState{}
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		raw, err := r.ReadBytes('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		if !bytes.Contains(raw, []byte(`"cost-state"`)) {
			continue
		}
		var c costState
		if json.Unmarshal(raw, &c) != nil || c.Type != "cost-state" {
			continue
		}
		tallies[c.StartTime] = c
	}
	if len(tallies) == 0 {
		return nil, false, nil
	}
	totals := map[string]contract.SideUsage{}
	for _, c := range tallies {
		for model, m := range c.ModelUsage {
			totals[model] = add(totals[model], m.counts())
		}
	}
	return totals, true, nil
}

// sideUsage is the usage of the session's API calls that produced no turn:
// its totals less every turn of the session, per model and count, clamped at
// zero so a harness that under-records never yields negative usage. It is
// nil when there are no totals, or when the transcripts could not all be
// read, since subtracting a partial set of turns would over-report.
//
// @spec: claude-code-transcript-parser#side-usage-is-totals-minus-turns
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func sideUsage(ctx Context, mainPath string) ([]contract.SideUsage, error) {
	deadline := time.Now().Add(CostStateWait)
	totals, ok, err := sessionTotals(mainPath)
	for err == nil && !ok && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		totals, ok, err = sessionTotals(mainPath)
	}
	if err != nil || !ok {
		return nil, err
	}
	res, _, err := tailWith(ctx, mainPath, nil)
	if err != nil {
		return nil, err
	}
	turns := map[string]contract.SideUsage{}
	for _, ev := range res.Events {
		t, ok := ev.(contract.Turn)
		if !ok || t.Model == nil {
			continue
		}
		turns[*t.Model] = add(turns[*t.Model], turnCounts(t.Usage))
	}
	out := []contract.SideUsage{}
	for model, total := range totals {
		less := turns[model]
		d := contract.SideUsage{
			Model:        model,
			Input:        max(total.Input-less.Input, 0),
			Output:       max(total.Output-less.Output, 0),
			CacheWrite5m: max(total.CacheWrite5m-less.CacheWrite5m, 0),
			CacheWrite1h: max(total.CacheWrite1h-less.CacheWrite1h, 0),
			CacheRead:    max(total.CacheRead-less.CacheRead, 0),
		}
		if d.Input+d.Output+d.CacheWrite5m+d.CacheWrite1h+d.CacheRead > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

func add(a, b contract.SideUsage) contract.SideUsage {
	return contract.SideUsage{
		Input: a.Input + b.Input, Output: a.Output + b.Output, CacheRead: a.CacheRead + b.CacheRead,
		CacheWrite5m: a.CacheWrite5m + b.CacheWrite5m, CacheWrite1h: a.CacheWrite1h + b.CacheWrite1h,
	}
}

func turnCounts(u contract.Usage) contract.SideUsage {
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return contract.SideUsage{
		Input: v(u.Input), Output: v(u.Output), CacheRead: v(u.CacheRead),
		CacheWrite5m: v(u.CacheWrite5m), CacheWrite1h: v(u.CacheWrite1h),
	}
}
