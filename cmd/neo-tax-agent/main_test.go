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
	startBackfill = func(d string) error { started = d; return nil }
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
	if started != dir {
		t.Fatalf("backfill started for %q", started)
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if run([]string{"version"}, nil, &out, &out) != 0 || strings.TrimSpace(out.String()) != Version {
		t.Fatal(out.String())
	}
}
