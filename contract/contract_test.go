package contract_test

import (
	"encoding/json"
	"testing"

	"github.com/neo-tax/harness-plugins/contract"
)

func envelope(kind string) map[string]any {
	return map[string]any{
		"contract_version": contract.Version,
		"device_id":        "dev_123",
		"harness":          "claude-code",
		"harness_version":  "2.1.310",
		"agent_version":    "0.1.0",
		"session_id":       "6f1c1d2e-0000-4000-8000-000000000000",
		"event_kind":       kind,
		"sent_at":          "2026-10-25T16:00:00Z",
	}
}

func with(base map[string]any, fields map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

func repo() map[string]any {
	return map[string]any{"host": "github.com", "owner": "neo-tax", "name": "harness-plugins"}
}

func usage() map[string]any {
	return map[string]any{
		"input": 2, "output": 200, "cache_write_5m": 0, "cache_write_1h": 13707,
		"cache_read": 29105, "service_tier": "standard", "speed": "standard", "source": "transcript",
	}
}

func validTurn() map[string]any {
	return with(envelope("session.turn"), map[string]any{
		"message_id":  "msg_01",
		"request_id":  "req_01",
		"subagent_id": nil,
		"timestamp":   "2026-10-25T16:00:00Z",
		"model":       "claude-opus-5-5",
		"branch":      "main",
		"head_sha":    "96e5e8f0000000000000000000000000000000aa",
		"usage":       usage(),
	})
}

func validOpened() map[string]any {
	return with(envelope("session.opened"), map[string]any{
		"started_at":     "2026-10-25T16:00:00Z",
		"entrypoint":     "cli",
		"source":         "startup",
		"repo":           repo(),
		"branch":         "main",
		"head_sha":       "96e5e8f0000000000000000000000000000000aa",
		"dropped_events": 0,
	})
}

func validPRLinked() map[string]any {
	return with(envelope("session.pr_linked"), map[string]any{
		"repo":          repo(),
		"number":        42,
		"first_seen_at": "2026-10-25T16:00:00Z",
	})
}

func validClosed() map[string]any {
	return with(envelope("session.closed"), map[string]any{
		"ended_at":       "2026-10-25T16:00:00Z",
		"reason":         "prompt_input_exit",
		"dropped_events": 0,
		"side_usage":     []any{sideEntry()},
	})
}

func sideEntry() map[string]any {
	return map[string]any{
		"model": "claude-opus-5-5", "input": 4076, "output": 2956,
		"cache_write_5m": 0, "cache_write_1h": 935, "cache_read": 151837,
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// @spec: session-event-wire-contract#event-kinds
// @spec: session-event-wire-contract#envelope-fields
func TestEveryEventKindValidates(t *testing.T) {
	for name, ev := range map[string]map[string]any{
		"opened": validOpened(), "turn": validTurn(), "pr_linked": validPRLinked(), "closed": validClosed(),
	} {
		if err := contract.ValidateEvent(marshal(t, ev)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEventRejections(t *testing.T) {
	cases := map[string]map[string]any{
		// @spec: session-event-wire-contract#event-kinds
		"unknown kind": with(validClosed(), map[string]any{"event_kind": "session.paused"}),
		// @spec: session-event-wire-contract#envelope-fields
		"extra envelope field": with(validClosed(), map[string]any{"cwd": "/Users/someone/client"}),
		"missing device_id":    without(validClosed(), "device_id"),
		// @spec: session-event-wire-contract#turn-fields
		"turn field on closed": with(validClosed(), map[string]any{"message_id": "msg_01"}),
		"turn missing usage":   without(validTurn(), "usage"),
		// @spec: session-event-wire-contract#usage-shape
		"unknown usage with counts": with(validTurn(), map[string]any{"usage": with(usage(), map[string]any{"source": "unknown"})}),
		"bad usage source":          with(validTurn(), map[string]any{"usage": with(usage(), map[string]any{"source": "guess"})}),
		// @spec: session-event-wire-contract#repo-shape
		"uppercase repo owner": with(validOpened(), map[string]any{"repo": with(repo(), map[string]any{"owner": "Neo-Tax"})}),
		"repo missing name":    with(validPRLinked(), map[string]any{"repo": without(repo(), "name")}),
		// @spec: session-event-wire-contract#closed-fields
		"closed missing side_usage": without(validClosed(), "side_usage"),
		// @spec: session-event-wire-contract#side-usage-shape
		"side_usage as object":      with(validClosed(), map[string]any{"side_usage": sideEntry()}),
		"side entry missing model":  with(validClosed(), map[string]any{"side_usage": []any{without(sideEntry(), "model")}}),
		"side entry empty model":    with(validClosed(), map[string]any{"side_usage": []any{with(sideEntry(), map[string]any{"model": ""})}}),
		"side entry null count":     with(validClosed(), map[string]any{"side_usage": []any{with(sideEntry(), map[string]any{"output": nil})}}),
		"side entry negative count": with(validClosed(), map[string]any{"side_usage": []any{with(sideEntry(), map[string]any{"cache_read": -1})}}),
		"side entry extra field":    with(validClosed(), map[string]any{"side_usage": []any{with(sideEntry(), map[string]any{"cost_usd": 0.4})}}),
		"side entry service tier":   with(validClosed(), map[string]any{"side_usage": []any{with(sideEntry(), map[string]any{"service_tier": "standard"})}}),
		// @spec: session-event-wire-contract#subagent-turns-belong-to-parent
		"empty subagent id": with(validTurn(), map[string]any{"subagent_id": ""}),
	}
	for name, ev := range cases {
		if err := contract.ValidateEvent(marshal(t, ev)); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

// @spec: session-event-wire-contract#side-usage-shape
func TestSideUsageNullOrEmpty(t *testing.T) {
	for _, v := range []any{nil, []any{}} {
		if err := contract.ValidateEvent(marshal(t, with(validClosed(), map[string]any{"side_usage": v}))); err != nil {
			t.Errorf("side_usage %v: %v", v, err)
		}
	}
}

// @spec: session-event-wire-contract#usage-shape
func TestUnknownUsageHasNullCounts(t *testing.T) {
	ev := with(validTurn(), map[string]any{"usage": map[string]any{
		"input": nil, "output": nil, "cache_write_5m": nil, "cache_write_1h": nil,
		"cache_read": nil, "service_tier": nil, "speed": nil, "source": "unknown",
	}})
	if err := contract.ValidateEvent(marshal(t, ev)); err != nil {
		t.Fatal(err)
	}
}

// @spec: session-event-wire-contract#repo-shape
func TestNullRepo(t *testing.T) {
	ev := with(validOpened(), map[string]any{"repo": nil, "branch": nil, "head_sha": nil})
	if err := contract.ValidateEvent(marshal(t, ev)); err != nil {
		t.Fatal(err)
	}
}

func without(m map[string]any, key string) map[string]any {
	out := with(m, nil)
	delete(out, key)
	return out
}
