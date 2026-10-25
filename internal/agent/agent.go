// Package agent is the shared core every adapter hands its session events
// to. Everything after an adapter (allowlist, queue, flush, enrollment) is
// here and shared across harnesses.
package agent

import (
	"bytes"
	"encoding/json"

	"github.com/neo-tax/harness-plugins/internal/allowlist"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

// Agent is one invocation of neo-tax-agent over a plugin data directory.
type Agent struct {
	DataDir string
	Queue   *queue.Queue
}

// New returns an agent over the harness-provided plugin data directory.
func New(dataDir string) *Agent {
	return &Agent{DataDir: dataDir, Queue: queue.New(dataDir)}
}

// Emit passes each event through the field allowlist and queues what
// remains. Removed fields are counted by name in local diagnostics; an
// event of an unknown kind is dropped whole and counted under event_kind.
//
// @spec: field-allowlist#enforced-below-adapters
// @spec: field-allowlist#unknown-field-dropped-and-counted
func (a *Agent) Emit(events ...any) error {
	var lines [][]byte
	var dropped []string
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return err
		}
		kept, removed, err := allowlist.Apply(m)
		if err != nil {
			dropped = append(dropped, "event_kind")
			continue
		}
		dropped = append(dropped, removed...)
		line, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		lines = append(lines, line)
	}
	if err := a.Queue.RecordDroppedFields(dropped); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	return a.Queue.Append(lines)
}
