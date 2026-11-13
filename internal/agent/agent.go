// Package agent is the shared core every adapter hands its session events
// to. Everything after an adapter (allowlist, queue, flush, enrollment) is
// here and shared across harnesses.
package agent

import (
	"bytes"
	"encoding/json"
	"log"
	"os"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/allowlist"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

// Agent is one invocation of neo-tax-agent over a plugin data directory.
type Agent struct {
	DataDir string
	Queue   *queue.Queue
	// Log receives local diagnostic lines. It never receives an event value.
	Log *log.Logger
}

// New returns an agent over the harness-provided plugin data directory.
func New(dataDir string) *Agent {
	return &Agent{DataDir: dataDir, Queue: queue.New(dataDir), Log: log.New(os.Stderr, "neo-tax-agent: ", 0)}
}

// preEnrollmentDeviceID stands in for an empty device_id while validating, so
// that an event emitted before the device has enrolled is not refused for the
// one field it cannot yet have. The queued event is not changed.
const preEnrollmentDeviceID = "pre-enrollment"

// Emit passes each event through the field allowlist, validates what remains
// against the wire contract, and queues the events that pass. Removed fields
// are counted by name in local diagnostics; an event of an unknown kind is
// dropped whole and counted under event_kind; an event whose values do not
// match the contract is dropped whole, counted by kind, and logged by kind and
// failing keyword, never by value.
//
// @spec: field-allowlist#enforced-below-adapters
// @spec: field-allowlist#unknown-field-dropped-and-counted
// @spec: field-allowlist#values-validated-before-queue
func (a *Agent) Emit(events ...any) error {
	var lines [][]byte
	var dropped, invalid []string
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
		if err := validate(kept, line); err != nil {
			// Apply accepted the kind, so it is one of the contract's kinds.
			kind, _ := kept["event_kind"].(string)
			invalid = append(invalid, kind)
			a.logf("dropped %s event: values do not match the wire contract (%s)", kind, contract.ErrorClass(err))
			continue
		}
		lines = append(lines, line)
	}
	if err := a.Queue.RecordDroppedFields(dropped); err != nil {
		return err
	}
	if err := a.Queue.RecordInvalidEvents(invalid); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	return a.Queue.Append(lines)
}

// validate checks an allowlisted event against the wire contract, standing in
// a placeholder for a device_id the device does not have yet.
func validate(kept map[string]any, line []byte) error {
	if id, ok := kept["device_id"]; !ok || id == "" {
		c := make(map[string]any, len(kept)+1)
		for k, v := range kept {
			c[k] = v
		}
		c["device_id"] = preEnrollmentDeviceID
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		line = b
	}
	return contract.ValidateEvent(line)
}

func (a *Agent) logf(format string, args ...any) {
	l := a.Log
	if l == nil {
		l = log.Default()
	}
	l.Printf(format, args...)
}
