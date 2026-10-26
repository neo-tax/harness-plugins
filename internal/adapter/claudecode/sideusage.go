package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

// CostStateWait bounds how long SessionEnd waits for the cost-state line
// Claude Code writes as it exits. The line is written before SessionEnd
// fires, so the wait only covers a slow write. The hook is async, so
// Claude Code does not cancel it during the wait.
var CostStateWait = 2 * time.Second

// exitReasons are the SessionEnd reasons on which the Claude Code process
// exits and so writes cost-state. On the others (/clear, logout, /resume)
// the process keeps running and no line is coming, so the totals are read
// once without waiting.
var exitReasons = map[string]bool{"prompt_input_exit": true, "other": true}

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

// modelTotals is one model's session totals. Cache creation a tally split
// by TTL is in the usage's 5m and 1h counts; a tally that did not split it
// (Claude Code through 2.1.310) adds it to unsplit instead.
type modelTotals struct {
	usage   contract.SideUsage
	unsplit int64
}

func (m costStateModel) counts() modelTotals {
	t := modelTotals{usage: contract.SideUsage{Input: m.InputTokens, Output: m.OutputTokens, CacheRead: m.CacheReadTokens}}
	if m.CacheCreation5m == nil && m.CacheCreation1h == nil {
		t.unsplit = m.CacheCreationTokens
		return t
	}
	if m.CacheCreation5m != nil {
		t.usage.CacheWrite5m = *m.CacheCreation5m
	}
	if m.CacheCreation1h != nil {
		t.usage.CacheWrite1h = *m.CacheCreation1h
	}
	return t
}

func (a modelTotals) add(b modelTotals) modelTotals {
	return modelTotals{usage: add(a.usage, b.usage), unsplit: a.unsplit + b.unsplit}
}

// less is the totals less the turns' usage, each count clamped at zero.
// The turns' cache writes come off the split counts of the same tier first;
// what those cannot cover comes off the unsplit amount, so an unsplit total
// is reduced by both tiers of the turns. What remains unsplit is reported
// as five-minute writes, as an unsplit turn's are.
//
// @spec: claude-code-transcript-parser#side-usage-cache-split
func (a modelTotals) less(turns contract.SideUsage) contract.SideUsage {
	d := contract.SideUsage{
		Input:     max(a.usage.Input-turns.Input, 0),
		Output:    max(a.usage.Output-turns.Output, 0),
		CacheRead: max(a.usage.CacheRead-turns.CacheRead, 0),
	}
	unsplit := a.unsplit
	d.CacheWrite5m = a.usage.CacheWrite5m - turns.CacheWrite5m
	if d.CacheWrite5m < 0 {
		unsplit += d.CacheWrite5m
		d.CacheWrite5m = 0
	}
	d.CacheWrite1h = a.usage.CacheWrite1h - turns.CacheWrite1h
	if d.CacheWrite1h < 0 {
		unsplit += d.CacheWrite1h
		d.CacheWrite1h = 0
	}
	d.CacheWrite5m += max(unsplit, 0)
	return d
}

// totalsScan reads a main transcript's cost-state lines incrementally. Each
// process that opens a session keeps its own tally, identified by its start
// time, and each line holds that tally so far; the session's totals are the
// latest line of each tally, summed. The totals are current unless a user or
// assistant line follows the latest cost-state line: that means a later
// process (a resumed session) has run since, and those totals leave it out.
// Other lines are bookkeeping Claude Code may write after exiting
// (artifact-comment-monitor, bridge-session, tools-used, queue-operation, and
// whatever a later version adds), and do not make the totals stale.
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
		// Only lines that could be one of the three types are decoded; the
		// substring test is a filter, the decoded type decides.
		if !bytes.Contains(raw, []byte(`"cost-state"`)) &&
			!bytes.Contains(raw, []byte(`"type":"user"`)) && !bytes.Contains(raw, []byte(`"type":"assistant"`)) {
			continue
		}
		var l costState
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		switch l.Type {
		case "cost-state":
			s.tallies[l.StartTime] = l
			s.current = true
		case "user", "assistant":
			s.current = false
		}
	}
}

func (s *totalsScan) totals() map[string]modelTotals {
	totals := map[string]modelTotals{}
	for _, c := range s.tallies {
		for model, m := range c.ModelUsage {
			totals[model] = totals[model].add(m.counts())
		}
	}
	return totals
}

// sideUsage is the usage of the session's API calls that produced no turn:
// its totals less every turn of the session, per model and count, clamped at
// zero so a harness that under-records never yields negative usage. It is
// nil when no current totals appear within wait, or when the
// transcripts could not all be read, since subtracting a partial set of
// turns would over-report.
//
// @spec: claude-code-transcript-parser#session-totals-from-cost-state
// @spec: claude-code-transcript-parser#side-usage-is-totals-minus-turns
// @spec: claude-code-transcript-parser#side-usage-null-without-totals
func sideUsage(ctx Context, mainPath string, wait time.Duration) ([]contract.SideUsage, error) {
	deadline := time.Now().Add(wait)
	scan := &totalsScan{path: mainPath, tallies: map[int64]costState{}}
	err := scan.advance()
	for err == nil && !scan.current && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		err = scan.advance()
	}
	if err != nil || !scan.current {
		return nil, err
	}
	totals := map[string]modelTotals{}
	for model, t := range scan.totals() {
		k := modelKey(model)
		totals[k] = totals[k].add(t)
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
		k := modelKey(*t.Model)
		turns[k] = add(turns[k], turnCounts(t.Usage))
	}
	if !joined(totals, turns) {
		return nil, nil
	}
	out := []contract.SideUsage{}
	for model, total := range totals {
		d := total.less(turns[model])
		d.Model = model
		if d.Input+d.Output+d.CacheWrite5m+d.CacheWrite1h+d.CacheRead > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// modelKey is a model name without a trailing bracketed suffix. Claude Code
// can name a model it was configured with that way (claude-opus-5[1m]) in its
// totals, while the API's response, which a turn records, names the model
// without it.
//
// @spec: claude-code-transcript-parser#side-usage-model-join
func modelKey(model string) string {
	if i := strings.LastIndexByte(model, '['); i > 0 && strings.HasSuffix(model, "]") {
		return model[:i]
	}
	return model
}

// joined reports whether every turn's usage has totals to come off. A turn
// model with usage but no totals, while some totals model has no turns,
// means the two name one model differently: subtracting nothing from those
// totals would report the turns as side usage. A totals model with no turns
// alone is ordinary (side calls on a model the engineer never chose), and so
// is a turn model with no totals alone (nothing to over-report).
//
// @spec: claude-code-transcript-parser#side-usage-model-join
func joined(totals map[string]modelTotals, turns map[string]contract.SideUsage) bool {
	turnOnly := false
	for model, u := range turns {
		if _, ok := totals[model]; !ok && u != (contract.SideUsage{}) {
			turnOnly = true
		}
	}
	if !turnOnly {
		return true
	}
	for model := range totals {
		if _, ok := turns[model]; !ok {
			return false
		}
	}
	return true
}

// latestTimestamp is the latest timestamp any line of a transcript records.
func latestTimestamp(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	var latest time.Time
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		raw, err := r.ReadBytes('\n')
		if err == io.EOF {
			return latest, nil
		}
		if err != nil {
			return time.Time{}, err
		}
		var l struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(raw, &l) == nil && l.Timestamp.After(latest) {
			latest = l.Timestamp
		}
	}
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
