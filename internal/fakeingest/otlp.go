package fakeingest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
)

// maxOTLPBody caps an OTLP request body after decompression.
const maxOTLPBody = 16 << 20

type otlpSignal string

const (
	signalLogs    otlpSignal = "logs"
	signalMetrics otlpSignal = "metrics"
)

// OTLPRecord is one log record or one metric data point as the fake received
// it, flattened out of its resource and scope. Nothing is filtered: the fake
// shows everything a harness's telemetry sends, which is what the real
// endpoint's allowlist has to be written against.
type OTLPRecord struct {
	Signal     string         `json:"signal"`
	Resource   map[string]any `json:"resource"`
	Attributes map[string]any `json:"attributes"`
	// Logs.
	EventName            string          `json:"event_name,omitempty"`
	TimeUnixNano         string          `json:"time_unix_nano,omitempty"`
	ObservedTimeUnixNano string          `json:"observed_time_unix_nano,omitempty"`
	Body                 json.RawMessage `json:"body,omitempty"`
	// Metrics.
	Name              string `json:"name,omitempty"`
	Kind              string `json:"kind,omitempty"`
	Temporality       int    `json:"temporality,omitempty"`
	StartTimeUnixNano string `json:"start_time_unix_nano,omitempty"`
	Value             any    `json:"value,omitempty"`
}

type otlpKeyValue struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpLogsRequest struct {
	ResourceLogs *[]struct {
		Resource  otlpResource `json:"resource"`
		ScopeLogs []struct {
			LogRecords []struct {
				TimeUnixNano         string          `json:"timeUnixNano"`
				ObservedTimeUnixNano string          `json:"observedTimeUnixNano"`
				EventName            string          `json:"eventName"`
				Body                 json.RawMessage `json:"body"`
				Attributes           []otlpKeyValue  `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type otlpDataPoint struct {
	Attributes        []otlpKeyValue `json:"attributes"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	TimeUnixNano      string         `json:"timeUnixNano"`
	AsInt             any            `json:"asInt"`
	AsDouble          any            `json:"asDouble"`
}

type otlpPoints struct {
	DataPoints             []otlpDataPoint `json:"dataPoints"`
	AggregationTemporality int             `json:"aggregationTemporality"`
}

type otlpMetricsRequest struct {
	ResourceMetrics *[]struct {
		Resource     otlpResource `json:"resource"`
		ScopeMetrics []struct {
			Metrics []struct {
				Name  string      `json:"name"`
				Sum   *otlpPoints `json:"sum"`
				Gauge *otlpPoints `json:"gauge"`
			} `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

// attrs flattens OTLP key-values to key → the value's single typed field.
func attrs(kvs []otlpKeyValue) map[string]any {
	out := map[string]any{}
	for _, kv := range kvs {
		for _, v := range kv.Value {
			out[kv.Key] = v
		}
	}
	return out
}

// otlp serves one OTLP/HTTP signal: authenticate, then content type, then
// body, in that order.
//
// @spec: otlp-ingest#fake-ingest-accepts-otlp
func (s *Server) otlp(signal otlpSignal) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !s.ingestKeyAccepted(key) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or revoked ingest key"})
			return
		}
		if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "only application/json is accepted"})
			return
		}
		body, err := readOTLPBody(r)
		if errors.Is(err, errTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		records, err := decodeOTLP(signal, body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		s.mu.Lock()
		for _, rec := range records {
			line, _ := json.Marshal(rec)
			s.otlpRecords = append(s.otlpRecords, line)
			if s.opts.RecordOTLP != nil {
				s.opts.RecordOTLP.Write(append(line, '\n'))
			}
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, struct{}{})
	}
}

// ingestKeyAccepted is true for a configured ingest key, or for any key when
// none is configured, but never for a device key.
func (s *Server) ingestKeyAccepted(key string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	_, isDevice := s.byKey[key]
	s.mu.Unlock()
	if isDevice {
		return false
	}
	return len(s.opts.IngestKeys) == 0 || slices.Contains(s.opts.IngestKeys, key)
}

var errTooLarge = errors.New("body exceeds the size limit")

func readOTLPBody(r *http.Request) ([]byte, error) {
	var src io.Reader = r.Body
	switch enc := r.Header.Get("Content-Encoding"); enc {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, errors.New("invalid gzip body")
		}
		defer gz.Close()
		src = gz
	default:
		return nil, errors.New("unsupported Content-Encoding " + enc)
	}
	body, err := io.ReadAll(io.LimitReader(src, maxOTLPBody+1))
	if err != nil {
		return nil, errors.New("unreadable body")
	}
	if len(body) > maxOTLPBody {
		return nil, errTooLarge
	}
	return body, nil
}

func decodeOTLP(signal otlpSignal, body []byte) ([]OTLPRecord, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var out []OTLPRecord
	switch signal {
	case signalLogs:
		var req otlpLogsRequest
		if dec.Decode(&req) != nil || req.ResourceLogs == nil {
			return nil, errors.New("not an OTLP logs export request")
		}
		for _, rl := range *req.ResourceLogs {
			res := attrs(rl.Resource.Attributes)
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					out = append(out, OTLPRecord{
						Signal: "log", Resource: res, Attributes: attrs(lr.Attributes), EventName: lr.EventName,
						TimeUnixNano: lr.TimeUnixNano, ObservedTimeUnixNano: lr.ObservedTimeUnixNano, Body: lr.Body,
					})
				}
			}
		}
	case signalMetrics:
		var req otlpMetricsRequest
		if dec.Decode(&req) != nil || req.ResourceMetrics == nil {
			return nil, errors.New("not an OTLP metrics export request")
		}
		for _, rm := range *req.ResourceMetrics {
			res := attrs(rm.Resource.Attributes)
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					kind, pts := "sum", m.Sum
					if pts == nil {
						kind, pts = "gauge", m.Gauge
					}
					if pts == nil {
						out = append(out, OTLPRecord{Signal: "metric", Resource: res, Name: m.Name, Kind: "other"})
						continue
					}
					for _, dp := range pts.DataPoints {
						v := dp.AsInt
						if v == nil {
							v = dp.AsDouble
						}
						out = append(out, OTLPRecord{
							Signal: "metric", Resource: res, Attributes: attrs(dp.Attributes), Name: m.Name, Kind: kind,
							Temporality: pts.AggregationTemporality, StartTimeUnixNano: dp.StartTimeUnixNano,
							TimeUnixNano: dp.TimeUnixNano, Value: v,
						})
					}
				}
			}
		}
	}
	return out, nil
}

func (s *Server) listOTLP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.otlpRecords
	if out == nil {
		out = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) resetOTLP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.otlpRecords = nil
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
