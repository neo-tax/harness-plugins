package fakeingest_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/fakeingest"
)

const logsExport = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},
"scopeLogs":[{"logRecords":[{"timeUnixNano":"1762800000000000000","eventName":"claude_code.api_request",
"attributes":[{"key":"session.id","value":{"stringValue":"s1"}},{"key":"input_tokens","value":{"intValue":"12"}},
{"key":"cost_usd","value":{"doubleValue":0.5}}]}]}]}]}`

const metricsExport = `{"resourceMetrics":[{"resource":{"attributes":[]},"scopeMetrics":[{"metrics":[
{"name":"claude_code.active_time.total","sum":{"aggregationTemporality":1,"isMonotonic":true,
"dataPoints":[{"attributes":[{"key":"session.id","value":{"stringValue":"s1"}},{"key":"type","value":{"stringValue":"user"}}],
"startTimeUnixNano":"1","timeUnixNano":"2","asDouble":3.5}]}}]}]}]}`

func (h *harness) postRaw(path, key, contentType, encoding string, body []byte) int {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) otlpRecords() []fakeingest.OTLPRecord {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + "/_fake/otlp")
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []fakeingest.OTLPRecord
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// @spec: otlp-ingest#fake-ingest-accepts-otlp
func TestOTLPLogsAndMetricsRecorded(t *testing.T) {
	h := start(t, fakeingest.Options{IngestKeys: []string{"ik"}})
	if c := h.postRaw("/otlp/v1/logs", "ik", "application/json", "", []byte(logsExport)); c != 200 {
		t.Fatalf("logs: %d", c)
	}
	if c := h.postRaw("/otlp/v1/metrics", "ik", "application/json; charset=utf-8", "", []byte(metricsExport)); c != 200 {
		t.Fatalf("metrics: %d", c)
	}
	recs := h.otlpRecords()
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	l, m := recs[0], recs[1]
	if l.Signal != "log" || l.EventName != "claude_code.api_request" || l.Attributes["session.id"] != "s1" ||
		l.Attributes["input_tokens"] != "12" || l.Attributes["cost_usd"] != 0.5 || l.Resource["service.name"] != "claude-code" {
		t.Errorf("log record: %+v", l)
	}
	if m.Signal != "metric" || m.Name != "claude_code.active_time.total" || m.Kind != "sum" || m.Temporality != 1 ||
		m.Value != 3.5 || m.Attributes["type"] != "user" {
		t.Errorf("metric point: %+v", m)
	}
}

// @spec: otlp-ingest#fake-ingest-accepts-otlp
func TestOTLPAuthenticatesFirst(t *testing.T) {
	h := start(t, fakeingest.Options{IngestKeys: []string{"ik"}})
	for _, key := range []string{"", "wrong"} {
		if c := h.postRaw("/otlp/v1/logs", key, "text/plain", "", []byte("junk")); c != 401 {
			t.Errorf("key %q: got %d, want 401", key, c)
		}
	}
}

// @spec: otlp-ingest#fake-ingest-accepts-otlp
func TestOTLPRefusesDeviceKey(t *testing.T) {
	h := start(t, fakeingest.Options{})
	er := h.enroll()
	if c := h.postRaw("/otlp/v1/logs", er.DeviceKey, "application/json", "", []byte(logsExport)); c != 401 {
		t.Errorf("device key: got %d, want 401", c)
	}
	if c := h.postRaw("/otlp/v1/logs", "any-other-key", "application/json", "", []byte(logsExport)); c != 200 {
		t.Errorf("unconfigured key: got %d, want 200", c)
	}
}

// @spec: otlp-ingest#fake-ingest-accepts-otlp
func TestOTLPContentTypeAndShape(t *testing.T) {
	h := start(t, fakeingest.Options{})
	if c := h.postRaw("/otlp/v1/logs", "ik", "application/x-protobuf", "", []byte{1, 2}); c != 415 {
		t.Errorf("protobuf: got %d, want 415", c)
	}
	if c := h.postRaw("/otlp/v1/logs", "ik", "application/json", "", []byte(metricsExport)); c != 400 {
		t.Errorf("metrics body on logs path: got %d, want 400", c)
	}
	if c := h.postRaw("/otlp/v1/metrics", "ik", "application/json", "", []byte(`not json`)); c != 400 {
		t.Errorf("malformed: got %d, want 400", c)
	}
	if n := len(h.otlpRecords()); n != 0 {
		t.Errorf("stored %d records from refused requests", n)
	}
}

// @spec: otlp-ingest#fake-ingest-accepts-otlp
func TestOTLPGzip(t *testing.T) {
	h := start(t, fakeingest.Options{})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(logsExport))
	gz.Close()
	if c := h.postRaw("/otlp/v1/logs", "ik", "application/json", "gzip", buf.Bytes()); c != 200 {
		t.Fatalf("gzip: %d", c)
	}
	if n := len(h.otlpRecords()); n != 1 {
		t.Errorf("got %d records", n)
	}

	buf.Reset()
	gz = gzip.NewWriter(&buf)
	gz.Write(bytes.Repeat([]byte(" "), 17<<20))
	gz.Close()
	if c := h.postRaw("/otlp/v1/logs", "ik", "application/json", "gzip", buf.Bytes()); c != 413 {
		t.Errorf("oversized: got %d, want 413", c)
	}
}
