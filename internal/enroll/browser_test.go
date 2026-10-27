package enroll_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/internal/enroll"
)

// browserLogin runs Login with no terminal and the browser form, and returns
// the form's address once Login has opened it, and Login's result.
func (f *fixture) browserLogin(t *testing.T, wait time.Duration, open func(string) error) (string, <-chan error) {
	t.Helper()
	o := f.opts("", "")
	o.Browser = true
	o.Wait = wait
	o.TerminalCommand = "/path/to/neo-tax-agent login"
	addr := make(chan string, 1)
	o.OpenURL = func(u string) error {
		addr <- u
		if open != nil {
			return open(u)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- enroll.Login(context.Background(), o) }()
	select {
	case u := <-addr:
		return u, done
	case err := <-done:
		t.Fatalf("Login returned before opening the form: %v\n%s", err, f.out.String())
	case <-time.After(5 * time.Second):
		t.Fatal("form never opened")
	}
	return "", nil
}

func get(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func submit(t *testing.T, u string, fields url.Values) (int, string) {
	t.Helper()
	resp, err := http.PostForm(u, fields)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Login did not return")
		return nil
	}
}

// With no terminal, the agent collects the token and email in a local
// browser form and enrolls when it is submitted; the conversation sees only
// the form's address and the result.
// @spec: device-enrollment#interactive-enrollment
// @spec: device-enrollment#token-never-in-conversation
// @spec: device-enrollment#browser-terminal-alternative
// @spec: device-enrollment#email-candidates
func TestBrowserFormEnrolls(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, time.Minute, nil)
	parsed, _ := url.Parse(u)
	if parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("form served at %s", u)
	}
	code, page := get(t, u)
	if code != http.StatusOK {
		t.Fatalf("GET %d", code)
	}
	for _, want := range []string{"Engineer@Example.com", "account@example.com", `type="password"`} {
		if !strings.Contains(page, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	code, page = submit(t, u, url.Values{"token": {"et_good"}, "email": {"account@example.com"}})
	if code != http.StatusOK || !strings.Contains(page, "Enrolled") {
		t.Fatalf("submit %d: %s", code, page)
	}
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if f.last.EnrollmentToken != "et_good" || f.last.Email != "account@example.com" {
		t.Fatalf("enrolled with %+v", f.last)
	}
	if _, err := enroll.Load(f.dir); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{u, "/path/to/neo-tax-agent login", "Enrolled this device as account@example.com."} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "et_good") {
		t.Errorf("output carries the token:\n%s", out)
	}
}

// A typed email is used when the engineer picks "other".
// @spec: device-enrollment#email-candidates
func TestBrowserFormTypedEmail(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, time.Minute, nil)
	submit(t, u, url.Values{"token": {"et_good"}, "email": {"other"}, "email_other": {" Typed@Example.com "}})
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if f.last.Email != "Typed@Example.com" {
		t.Fatalf("email %q", f.last.Email)
	}
}

// The first candidate is chosen by default, and a typed email wins even when
// its radio button was not clicked (the page runs no script).
// @spec: device-enrollment#email-candidates
func TestBrowserFormDefaults(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, time.Minute, nil)
	_, page := get(t, u)
	if !strings.Contains(page, `value="Engineer@Example.com" checked`) {
		t.Errorf("first candidate not chosen by default:\n%s", page)
	}
	// What a browser sends when the engineer types another email but leaves
	// the default candidate selected.
	submit(t, u, url.Values{"token": {"et_good"}, "email": {"Engineer@Example.com"}, "email_other": {"typed@example.com"}})
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if f.last.Email != "typed@example.com" {
		t.Fatalf("email %q", f.last.Email)
	}
}

// With no email found on the machine, the form asks for one plainly, with no
// choices to pick from.
// @spec: device-enrollment#email-candidates
func TestBrowserFormWithoutCandidates(t *testing.T) {
	f := newFixture(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	o := f.opts("", "")
	o.AccountEmail = nil
	o.Browser, o.Wait = true, time.Minute
	addr := make(chan string, 1)
	o.OpenURL = func(u string) error { addr <- u; return nil }
	done := make(chan error, 1)
	go func() { done <- enroll.Login(context.Background(), o) }()
	u := <-addr
	_, page := get(t, u)
	if strings.Contains(page, `type="radio"`) || strings.Contains(page, "Another email") {
		t.Errorf("choices offered with no candidates:\n%s", page)
	}
	if !strings.Contains(page, `name="email_other" type="email" required`) {
		t.Errorf("no required email field:\n%s", page)
	}
	submit(t, u, url.Values{"token": {"et_good"}, "email_other": {"solo@example.com"}})
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if f.last.Email != "solo@example.com" {
		t.Fatalf("email %q", f.last.Email)
	}
}

// A rejected token or an invalid email is shown in the form, which stays
// open for another try.
// @spec: device-enrollment#browser-form-retries
func TestBrowserFormRetries(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, time.Minute, nil)
	code, page := submit(t, u, url.Values{"token": {"et_expired"}, "email": {"account@example.com"}})
	if code != http.StatusOK || !strings.Contains(page, "rejected") || strings.Contains(page, "et_expired") {
		t.Fatalf("rejected token: %d %s", code, page)
	}
	code, page = submit(t, u, url.Values{"token": {"et_good"}, "email": {"other"}, "email_other": {"not an email"}})
	if code != http.StatusOK || !strings.Contains(page, "not an email address") {
		t.Fatalf("bad email: %d %s", code, page)
	}
	select {
	case err := <-done:
		t.Fatalf("Login returned after a failed try: %v", err)
	default:
	}
	submit(t, u, url.Values{"token": {"et_good"}, "email": {"account@example.com"}})
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// @spec: device-enrollment#browser-form-loopback
func TestBrowserFormRefusesOtherHostsAndPaths(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, time.Minute, nil)
	parsed, _ := url.Parse(u)

	if code, _ := get(t, parsed.Scheme+"://"+parsed.Host+"/"); code != http.StatusNotFound {
		t.Errorf("root: %d", code)
	}
	if code, _ := get(t, parsed.Scheme+"://"+parsed.Host+"/not-the-secret"); code != http.StatusNotFound {
		t.Errorf("wrong secret: %d", code)
	}
	// A DNS-rebinding page reaches the port under its own host name.
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader("token=et_good&email=account%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "attacker.example:" + parsed.Port()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: %d", resp.StatusCode)
	}
	if f.last.EnrollmentToken != "" {
		t.Fatal("enrolled through a refused request")
	}

	submit(t, u, url.Values{"token": {"et_good"}, "email": {"account@example.com"}})
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	// Once enrolled, the form is gone.
	if _, err := http.Get(u); err == nil {
		t.Error("form still served after enrollment")
	}
}

// @spec: device-enrollment#browser-wait-bounded
func TestBrowserWaitBounded(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, 300*time.Millisecond, nil)
	err := wait(t, done)
	if !errors.Is(err, enroll.ErrLoginNotCompleted) {
		t.Fatalf("err %v", err)
	}
	if _, err := http.Get(u); err == nil {
		t.Error("form still served after the wait")
	}
	if _, err := enroll.Load(f.dir); !errors.Is(err, enroll.ErrNotEnrolled) {
		t.Errorf("enrolled: %v", err)
	}
}

// When no browser can be opened, the address and the terminal command are
// still printed, and the form still works if the engineer opens it.
// @spec: device-enrollment#browser-terminal-alternative
func TestBrowserFormWorksWhenNoBrowserOpens(t *testing.T) {
	f := newFixture(t)
	u, done := f.browserLogin(t, time.Minute, func(string) error { return errors.New("no display") })
	submit(t, u, url.Values{"token": {"et_good"}, "email": {"account@example.com"}})
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), u) {
		t.Errorf("address not printed:\n%s", f.out.String())
	}
}
