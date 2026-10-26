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

// totalsScan reads a main transcript's cost-state lines incrementally. Each
// process that opens a session keeps its own tally, identified by its start
// time, and each line holds that tally so far; the session's totals are the
// latest line of each tally, summed. The totals are current only when no
// other line follows the latest cost-state line, apart from the lines Claude
// Code writes after exiting: anything else means a later process (a resumed
// session) has run since, and those totals leave it out.
type totalsScan struct {
	path    string
	offset  int64
	tallies map[int64]costState
	current bool
}

// advance reads the complete lines written since the last call.
func (s *totalsScan) advance() error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		raw, err := r.ReadBytes('\n')
		if err == io.EOF {
			return nil // a partial last line is read again next time
		}
		if err != nil {
			return err
		}
		s.offset += int64(len(raw))
		// Most lines name neither type, and are told apart without decoding.
		var l costState
		if !bytes.Contains(raw, []byte(`"cost-state"`)) && !bytes.Contains(raw, []byte(`"artifact-comment-monitor"`)) ||
			json.Unmarshal(raw, &l) != nil {
			s.current = false
			continue
		}
		switch l.Type {
		case "cost-state":
			s.tallies[l.StartTime] = l
			s.current = true
		case "artifact-comment-monitor":
		default:
			s.current = false
		}
	}
}

func (s *totalsScan) totals() map[string]contract.SideUsage {
	totals := map[string]contract.SideUsage{}
	for _, c := range s.tallies {
		for model, m := range c.ModelUsage {
			totals[model] = add(totals[model], m.counts())
		}
	}
	return totals
}

// sideUsage is the usage of the session's API calls that produced no turn:
// its totals less every turn of the session, per model and count, clamped at
// zero so a harness that under-records never yields negative usage. It is
// nil when no current totals appear within CostStateWait, or when the
// transcripts could not all be read, since subtracting a partial set of
// turns would over-report.
//
// @spec: claude-code-transcript-parser#session-totals-from-cost-state
// @spec: claude-code-transcript-parser#side-usage-is-totals-minus-turns
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func sideUsage(ctx Context, mainPath string) ([]contract.SideUsage, error) {
	deadline := time.Now().Add(CostStateWait)
	scan := &totalsScan{path: mainPath, tallies: map[int64]costState{}}
	err := scan.advance()
	for err == nil && !scan.current && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		err = scan.advance()
	}
	if err != nil || !scan.current {
		return nil, err
	}
	totals := scan.totals()
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
