package enroll

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// BrowserWait bounds how long login waits for the browser form.
//
// @spec: device-enrollment#browser-wait-bounded
const BrowserWait = 5 * time.Minute

// ErrLoginNotCompleted means the browser form was not submitted in time.
var ErrLoginNotCompleted = errors.New("login was not completed in the browser")

// OpenBrowser opens url in the engineer's default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Run()
}

// browserLogin serves a one-page form on the loopback interface, at a path
// holding a one-time secret, and performs the enrollment exchange when the
// form is submitted, so the token goes from the browser to the agent and
// never through the harness conversation. A failed try is shown in the form
// for another; the form stops being served once enrollment succeeds or the
// wait ends.
//
// @spec: device-enrollment#browser-form-loopback
// @spec: device-enrollment#browser-form-retries
// @spec: device-enrollment#browser-terminal-alternative
func browserLogin(ctx context.Context, o Options, candidates []string, email string) error {
	wait := o.Wait
	if wait == 0 {
		wait = BrowserWait
	}
	if email == "" && len(candidates) > 0 {
		email = candidates[0]
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(o.Out, "Could not start the login form. Run this in a terminal:\n  %s\n", o.TerminalCommand)
		return ErrNeedInput
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		ln.Close()
		return err
	}
	host := ln.Addr().String()
	path := "/" + hex.EncodeToString(secret)
	formURL := "http://" + host + path

	var (
		mu       sync.Mutex
		enrolled = make(chan string, 1)
		done     bool
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A page on another origin that rebinds its name to 127.0.0.1
		// reaches this port under its own host name.
		if r.Host != host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		page := formPage{Candidates: candidates, Email: email}
		switch r.Method {
		case http.MethodGet:
			page.render(w)
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			token := strings.TrimSpace(r.PostFormValue("token"))
			// The page runs no script, so typing another email does not
			// select its radio button: a typed email wins over the
			// candidate that was selected by default.
			chosen := r.PostFormValue("email")
			if typed := strings.TrimSpace(r.PostFormValue("email_other")); typed != "" || chosen == "other" {
				chosen = typed
			}
			chosen = strings.TrimSpace(chosen)
			mu.Lock()
			defer mu.Unlock()
			if done {
				page.Done = true
				page.render(w)
				return
			}
			if err := exchange(r.Context(), o, token, chosen); err != nil {
				page.Error, page.Email = err.Error(), chosen
				page.render(w)
				return
			}
			done = true
			page.Done, page.Email = true, chosen
			page.render(w)
			enrolled <- chosen
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()

	opener := "Opened your browser to finish enrolling with Neo.Tax:"
	if o.OpenURL == nil || o.OpenURL(formURL) != nil {
		opener = "Open this address in a browser on this machine to finish enrolling with Neo.Tax:"
	}
	fmt.Fprintf(o.Out, "%s\n  %s\nWaiting up to %s for the form. If this machine has no browser that can reach it (for example over SSH), run this in a terminal instead:\n  %s\n",
		opener, formURL, minutes(wait), o.TerminalCommand)

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-enrolled:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	fmt.Fprintln(o.Out, "Login was not completed; run neo-tax:login again when ready.")
	return ErrLoginNotCompleted
}

func minutes(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if d == time.Minute {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}

type formPage struct {
	Candidates []string
	Email      string
	Error      string
	Done       bool
}

func (p formPage) render(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	formTemplate.Execute(w, p)
}

// IsCandidate reports whether the page's email is one of its candidates.
func (p formPage) IsCandidate(e string) bool { return strings.EqualFold(p.Email, e) }

// Other reports whether the page's email is a typed one.
func (p formPage) Other() bool {
	if p.Email == "" {
		return len(p.Candidates) == 0
	}
	for _, c := range p.Candidates {
		if strings.EqualFold(c, p.Email) {
			return false
		}
	}
	return true
}

var formTemplate = template.Must(template.New("form").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Neo.Tax login</title>
<style>
:root { color-scheme: light dark; --fg: #1a1a1a; --muted: #5c5c5c; --bg: #fafafa; --card: #fff; --line: #d9d9d9; --accent: #0b6e4f; --err: #a4262c; }
@media (prefers-color-scheme: dark) { :root { --fg: #ececec; --muted: #a3a3a3; --bg: #161616; --card: #1f1f1f; --line: #3a3a3a; --accent: #4cc39a; --err: #ff8a8a; } }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width: 28rem; margin: 10vh auto; padding: 0 16px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 12px; padding: 24px; }
h1 { font-size: 1.25rem; margin: 0 0 4px; }
p { margin: 0 0 16px; color: var(--muted); }
label { display: block; font-weight: 600; margin: 16px 0 6px; }
fieldset { border: 0; padding: 0; margin: 0; }
legend { font-weight: 600; margin: 16px 0 6px; padding: 0; }
.choice { display: flex; align-items: center; gap: 8px; margin: 6px 0; font-weight: 400; }
input[type=password], input[type=email] { width: 100%; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: transparent; color: inherit; font: inherit; }
button { margin-top: 20px; width: 100%; padding: 10px; border: 0; border-radius: 8px; background: var(--accent); color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
.err { color: var(--err); margin: 12px 0 0; }
</style>
</head>
<body>
<main><div class="card">
{{if .Done}}
<h1>Enrolled</h1>
<p>This device is enrolled with Neo.Tax as {{.Email}}. You can close this tab and return to Claude Code.</p>
{{else}}
<h1>Enroll with Neo.Tax</h1>
<p>Paste the enrollment token your Neo.Tax admin shared. It goes from this page to the agent on your machine, and never into your Claude Code conversation.</p>
<form method="post">
<label for="token">Enrollment token</label>
<input id="token" name="token" type="password" autocomplete="off" required autofocus>
{{if .Candidates}}<fieldset>
<legend>Your work email</legend>
{{range .Candidates}}<label class="choice"><input type="radio" name="email" value="{{.}}"{{if $.IsCandidate .}} checked{{end}}> {{.}}</label>
{{end}}<label class="choice"><input type="radio" name="email" value="other"{{if .Other}} checked{{end}}> Another email</label>
<input name="email_other" type="email" aria-label="Another email" placeholder="you@company.com"{{if .Other}} value="{{.Email}}"{{end}}>
</fieldset>{{else}}<label for="email_other">Your work email</label>
<input id="email_other" name="email_other" type="email" required placeholder="you@company.com" value="{{.Email}}">{{end}}
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
<button type="submit">Enroll this device</button>
</form>
{{end}}
</div></main>
</body>
</html>
`))
