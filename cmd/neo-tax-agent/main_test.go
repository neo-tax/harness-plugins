package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if code := run([]string{"login", "--data-dir", dir, "--server", srv.URL, "--token", "t", "--email", "e@example.com"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
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

// Run by a harness, with no terminal, login collects the token in the
// browser form and names the terminal command as the alternative.
// @spec: device-enrollment#interactive-enrollment
// @spec: device-enrollment#browser-terminal-alternative
func TestLoginWithoutTerminalUsesTheBrowserForm(t *testing.T) {
	startBackfill = func(string, bool) error { return nil }
	srv := httptest.NewServer(fakeingest.New(fakeingest.Options{}))
	defer srv.Close()
	dir := t.TempDir()
	opened := make(chan string, 1)
	openURL = func(u string) error { opened <- u; return nil }
	defer func() { openURL = enroll.OpenBrowser }()
	go func() {
		u := <-opened
		resp, err := http.PostForm(u, url.Values{"token": {"t"}, "email": {"other"}, "email_other": {"e@example.com"}})
		if err == nil {
			resp.Body.Close()
		}
	}()
	var out, errOut bytes.Buffer
	if code := run([]string{"login", "--data-dir", dir, "--server", srv.URL}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), " login --data-dir ") || !strings.Contains(out.String(), " --server "+srv.URL) {
		t.Errorf("no terminal command for this server:\n%s", out.String())
	}
	if d, err := enroll.Load(dir); err != nil || d.Email != "e@example.com" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if run([]string{"version"}, nil, &out, &out) != 0 || strings.TrimSpace(out.String()) != Version {
		t.Fatal(out.String())
	}
}
