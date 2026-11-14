package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/enroll"
	"github.com/neo-tax/harness-plugins/internal/fakeingest"
)

// @spec: device-enrollment#interactive-enrollment
func TestLoginCommand(t *testing.T) {
	var started string
	var restarted bool
	startBackfill = func(d string, restart bool) error { started, restarted = d, restart; return nil }
	srv := httptest.NewServer(fakeingest.New(fakeingest.Options{}))
	defer srv.Close()
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"login", "--data-dir", dir, "--server", srv.URL}, strings.NewReader(""), &out, &errOut); code != 3 {
		t.Fatalf("missing input: exit %d", code)
	}
	if !strings.Contains(out.String(), "--token") {
		t.Fatalf("no instructions: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"login", "--data-dir", dir, "--server", srv.URL, "--token", "t", "--email", "e@example.com"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := enroll.Load(dir); err != nil {
		t.Fatal(err)
	}
	if started != dir || restarted {
		t.Fatalf("backfill started for %q (restart %v)", started, restarted)
	}
	// @spec: event-queue-and-backfill#backfill-command
	if code := run([]string{"backfill", "--data-dir", dir, "--background", "--restart"}, nil, &out, &errOut); code != 0 || !restarted {
		t.Fatalf("backfill --restart: exit %d, restart %v: %s", code, restarted, errOut.String())
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if run([]string{"version"}, nil, &out, &out) != 0 || strings.TrimSpace(out.String()) != Version {
		t.Fatal(out.String())
	}
}
