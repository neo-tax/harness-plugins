package usageaudit_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/usageaudit"
)

const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"text","text":"SECRET"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":1198,"cache_read_input_tokens":120792,"output_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1198},"service_tier":"standard"}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"SECRET"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1178}}

event: message_stop
data: {"type":"message_stop"}

`

func upstream(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("request-id", "req_1")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func through(t *testing.T, up *httptest.Server) (string, []usageaudit.Record) {
	t.Helper()
	var log bytes.Buffer
	p, err := usageaudit.NewProxy(up.URL, &log)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(p)
	defer proxy.Close()
	resp, err := http.Post(proxy.URL+"/v1/messages?beta=true", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"SECRET"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	p.Close()
	if strings.Contains(log.String(), "SECRET") {
		t.Fatalf("log contains message content: %s", log.String())
	}
	recs, err := usageaudit.ReadRecords(&log)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), recs
}

func TestProxyRecordsStreamedUsage(t *testing.T) {
	for _, ct := range []string{"text/event-stream", "text/event-stream; charset=utf-8"} {
		t.Run(ct, func(t *testing.T) { testStreamedUsage(t, ct) })
	}
}

func testStreamedUsage(t *testing.T, contentType string) {
	body, recs := through(t, upstream(t, contentType, stream))
	if body != stream {
		t.Fatalf("proxy altered the stream:\n%s", body)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]
	if r.Path != "/v1/messages" || r.RequestID != "req_1" || r.MessageID != "msg_1" || r.Model != "claude-opus-5-5" || r.Status != 200 {
		t.Fatalf("record = %+v", r)
	}
	// Output comes from message_delta, the rest from message_start.
	want := usageaudit.Usage{Input: 2, Output: 1178, CacheRead: 120792, CacheWrite5m: 0, CacheWrite1h: 1198}
	if r.Usage != want {
		t.Fatalf("usage = %+v, want %+v", r.Usage, want)
	}
}

func TestProxyRecordsJSONUsage(t *testing.T) {
	const js = `{"id":"msg_2","model":"claude-haiku-5-5","content":[{"type":"text","text":"SECRET"}],"usage":{"input_tokens":310,"output_tokens":12,"cache_creation_input_tokens":40,"cache_read_input_tokens":0}}`
	_, recs := through(t, upstream(t, "application/json", js))
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]
	// Without the per-TTL split, cache writes are the five-minute tier.
	want := usageaudit.Usage{Input: 310, Output: 12, CacheWrite5m: 40}
	if r.MessageID != "msg_2" || r.Model != "claude-haiku-5-5" || r.Usage != want {
		t.Fatalf("record = %+v", r)
	}
}

func TestCompare(t *testing.T) {
	recs := []usageaudit.Record{
		{Path: "/v1/messages", RequestID: "req_turn", Model: "claude-opus-5-5", Status: 200, Usage: usageaudit.Usage{Input: 2, Output: 10, CacheRead: 100}},
		{Path: "/v1/messages", RequestID: "req_title", Model: "claude-haiku-5-5", Status: 200, Usage: usageaudit.Usage{Input: 300, Output: 8}},
		{Path: "/v1/messages", RequestID: "req_off", Model: "claude-opus-5-5", Status: 200, Usage: usageaudit.Usage{Input: 5, Output: 50}},
		{Path: "/v1/messages/count_tokens", RequestID: "req_count", Status: 200},
		{Path: "/v1/messages", RequestID: "req_err", Model: "claude-opus-5-5", Status: 529},
	}
	events := []json.RawMessage{
		json.RawMessage(`{"event_kind":"session.opened","session_id":"s"}`),
		json.RawMessage(`{"event_kind":"session.turn","request_id":"req_turn","message_id":"msg_a","usage":{"input":2,"output":10,"cache_read":100,"cache_write_5m":0,"cache_write_1h":0}}`),
		json.RawMessage(`{"event_kind":"session.turn","request_id":"req_off","message_id":"msg_b","usage":{"input":5,"output":49,"cache_read":0,"cache_write_5m":0,"cache_write_1h":0}}`),
		json.RawMessage(`{"event_kind":"session.turn","request_id":"req_ghost","message_id":"msg_c","usage":{"input":1,"output":1,"cache_read":0,"cache_write_5m":0,"cache_write_1h":0}}`),
	}
	rep, err := usageaudit.Compare(recs, events)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Matched != 2 {
		t.Errorf("matched = %d, want 2", rep.Matched)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].RequestID != "req_title" {
		t.Errorf("missing = %+v, want req_title only (count_tokens and errors bill nothing)", rep.Missing)
	}
	if len(rep.Mismatched) != 1 || rep.Mismatched[0].RequestID != "req_off" {
		t.Errorf("mismatched = %+v, want req_off", rep.Mismatched)
	}
	if len(rep.Unexplained) != 1 || rep.Unexplained[0] != "req_ghost" {
		t.Errorf("unexplained = %v, want req_ghost", rep.Unexplained)
	}
	if got := rep.API.Total(); got != 2+10+100+300+8+5+50 {
		t.Errorf("API total = %d", got)
	}
	if got := rep.Events.Total(); got != 2+10+100+5+49 {
		t.Errorf("events total = %d (unmatched events excluded)", got)
	}
}
