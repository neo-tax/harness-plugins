// Package managedsettings_test checks the Claude Code managed settings
// Neo.Tax ships to turn on telemetry export.
package managedsettings_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func load(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("managed-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s.Env
}

// The exporter is configured the way the endpoint expects: OTLP/JSON over
// HTTP, delta metrics, repository attributes on, and the ingest key as a
// bearer token. Claude Code's default protocol is protobuf from 2.1.323, and
// repository attributes are off by default.
// @spec: otlp-ingest#managed-settings-exporter
func TestManagedSettingsConfigureTheExporter(t *testing.T) {
	env := load(t)
	want := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":                      "1",
		"OTEL_LOGS_EXPORTER":                                "otlp",
		"OTEL_METRICS_EXPORTER":                             "otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT":                       "https://api.neo.tax/otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL":                       "http/json",
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
		"OTEL_METRICS_INCLUDE_REPOSITORY":                   "true",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if !strings.HasPrefix(env["OTEL_EXPORTER_OTLP_HEADERS"], "Authorization=Bearer ") {
		t.Errorf("OTEL_EXPORTER_OTLP_HEADERS = %q, want a bearer ingest key", env["OTEL_EXPORTER_OTLP_HEADERS"])
	}
	// Per-signal settings would override the ones above.
	for k := range env {
		for _, sig := range []string{"_LOGS_", "_METRICS_", "_TRACES_"} {
			if strings.HasPrefix(k, "OTEL_EXPORTER_OTLP"+sig) && k != "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE" {
				t.Errorf("%s overrides the exporter settings", k)
			}
		}
	}
}

// Content switches stay off, so Claude Code redacts prompts, responses,
// tool details, and API bodies before they leave the machine.
// @spec: otlp-ingest#managed-settings-no-content
func TestManagedSettingsLeaveContentOff(t *testing.T) {
	env := load(t)
	for _, k := range []string{"OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_RAW_API_BODIES"} {
		if v, ok := env[k]; ok {
			t.Errorf("%s is set to %q", k, v)
		}
	}
}
