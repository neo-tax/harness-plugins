package enroll

import (
	"context"
	"crypto/rand"
	_ "embed"
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
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// interFont is Inter's Latin subset (weights 400 to 500), the typeface of
// Neo.Tax's site, served from the form's own address so the page loads
// nothing from elsewhere. Licensed under the SIL Open Font License 1.1;
// see fonts/OFL.txt.
//
//go:embed fonts/inter-latin-400-500.woff2
var interFont []byte

// BrowserWait bounds how long login waits for the browser form.
//
// @spec: device-enrollment#browser-wait-bounded
const BrowserWait = 5 * time.Minute

// ErrLoginNotCompleted means the browser form was not submitted in time.
var ErrLoginNotCompleted = errors.New("login was not completed in the browser")

// OpenBrowser opens url in the engineer's default browser without waiting
// for it: some launchers (xdg-open with $BROWSER set) run the browser in the
// foreground until it exits.
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
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
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
func browserLogin(ctx context.Context, o Options, candidates []string, token, email string) error {
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
		// closed is set once the form stops taking submits: on success
		// (with as set to the enrolled email) or when the wait ends.
		closed bool
		as     string
		// expired is set as soon as the wait ends, before the lock is
		// taken, so a submit failing at that moment is not offered a
		// retry the form will no longer take.
		expired atomic.Bool
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A page on another origin that rebinds its name to 127.0.0.1
		// reaches this port under its own host name.
		if r.Host != host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == path+"/inter.woff2" && r.Method == http.MethodGet {
			// Cached, so the result page, rendered after the form has
			// stopped serving, still has the font the form loaded.
			w.Header().Set("Content-Type", "font/woff2")
			w.Header().Set("Cache-Control", "private, max-age=3600, immutable")
			w.Write(interFont)
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
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src 'self'; form-action 'self'; frame-ancestors 'none'")
		mu.Lock()
		page := formPage{Candidates: candidates, Email: email, HasToken: token != "", Font: path + "/inter.woff2"}
		mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			page.render(w)
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
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
			page.HasToken = token != ""
			submitted := token
			if submitted == "" {
				submitted = strings.TrimSpace(r.PostFormValue("token"))
			}
			if closed {
				page.Done, page.Expired, page.Email = as != "", as == "", as
				page.render(w)
				return
			}
			if err := exchange(r.Context(), o, submitted, chosen); err != nil {
				// A given token that is refused is dropped, so the form
				// asks for another.
				if errors.Is(err, ErrTokenRejected) {
					token = ""
				}
				page.Error, page.Email, page.Expired, page.HasToken = sentence(err.Error()), chosen, expired.Load(), token != ""
				page.render(w)
				return
			}
			closed, as = true, chosen
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

	// The address is printed under the lock before the browser is opened,
	// so a submit (which writes its result to the same output) cannot come
	// first, and the lock is not held while the launcher runs.
	mu.Lock()
	fmt.Fprintf(o.Out, "To finish enrolling with Neo.Tax, open this address in a browser on this machine:\n  %s\nWaiting up to %s for the form. If this machine has no browser that can reach it (for example over SSH), run this in a terminal instead:\n  %s\n",
		formURL, minutes(wait), o.TerminalCommand)
	mu.Unlock()
	if o.OpenURL != nil {
		o.OpenURL(formURL)
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-enrolled:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	// Close the form under the lock, so a submit already enrolling finishes
	// first and is reported as the success it was.
	expired.Store(true)
	mu.Lock()
	finished := as != ""
	closed = true
	mu.Unlock()
	if finished {
		return nil
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

// sentence makes an error message read as a sentence on the page: an
// initial capital and a closing period.
func sentence(msg string) string {
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

type formPage struct {
	Candidates []string
	Email      string
	Error      string
	Done       bool
	Expired    bool
	HasToken   bool
	Font       string
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
@font-face { font-family: "Inter"; font-style: normal; font-weight: 400 500; font-display: swap; src: url("{{.Font}}") format("woff2"); }
:root {
  color-scheme: light dark;
  /* Neo.Tax's site palette: ink and body text on paper, navy actions. */
  --page: #f4f6f8; --card: #fff; --ink: #111827; --body: #4b5563; --muted: #5f687c; --line: #d9dee3;
  --action: #194a80; --action-hover: #0f2e52; --action-text: #fff; --focus: #5b84b8; --err: #b42318;
}
@media (prefers-color-scheme: dark) {
  /* The site's night sections: navy surfaces, white actions with navy text. */
  :root {
    --page: #07172a; --card: #0c233e; --ink: #fff; --body: #b8cce4; --muted: #8aaad1; --line: #143e6c;
    --action: #fff; --action-hover: #dce7f3; --action-text: #0f2e52; --focus: #8aaad1; --err: #ffa2a2;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--page); color: var(--body); font: 15px/1.55 "Inter", system-ui, -apple-system, "Helvetica Neue", Arial, sans-serif; -webkit-font-smoothing: antialiased; }
main { max-width: 27rem; margin: 10vh auto; padding: 0 16px; }
.brand { color: var(--ink); font-weight: 500; font-size: 14px; letter-spacing: -0.01em; margin: 0 0 12px 2px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 28px; }
h1 { color: var(--ink); font-size: 24px; line-height: 1.2; font-weight: 500; letter-spacing: -0.035em; margin: 0 0 8px; }
p { margin: 0; }
form { margin-top: 24px; }
.field { margin-top: 20px; }
.field:first-child { margin-top: 0; }
label.title, legend { display: block; color: var(--ink); font-weight: 500; font-size: 14px; margin: 0 0 8px; padding: 0; }
fieldset { border: 0; padding: 0; margin: 0; min-width: 0; }
.choice { display: flex; align-items: center; gap: 10px; margin: 0 0 6px; color: var(--ink); cursor: pointer; }
input[type=radio] { margin: 0; accent-color: var(--action); width: 16px; height: 16px; }
input[type=password], input[type=email] { width: 100%; height: 44px; padding: 0 12px; border: 1px solid var(--line); border-radius: 4px; background: transparent; color: var(--ink); font: inherit; }
.choice + input[type=email] { margin-top: 4px; }
input::placeholder { color: var(--muted); }
input:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
button { margin-top: 24px; width: 100%; height: 44px; border: 0; border-radius: 4px; background: var(--action); color: var(--action-text); font: inherit; font-weight: 500; letter-spacing: -0.01em; cursor: pointer; transition: background-color .15s; }
button:hover { background: var(--action-hover); }
.err { color: var(--err); margin-top: 16px; }
</style>
</head>
<body>
<main><p class="brand">Neo.Tax</p><div class="card">
{{if .Expired}}
<h1>Login expired</h1>
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
<p>This login form is no longer waiting. Run /neo-tax:login in Claude Code again.</p>
{{else if .Done}}
<h1>Enrolled</h1>
<p>This device is enrolled with Neo.Tax as {{.Email}}. You can close this tab and return to Claude Code.</p>
{{else}}
<h1>Enroll this device</h1>
{{if .HasToken}}<p>Confirm the work email Neo.Tax should know you by.</p>{{else}}<p>Paste the enrollment token your Neo.Tax admin shared. It goes from this page to the agent on your machine, and never into your Claude Code conversation.</p>{{end}}
<form method="post">
{{if not .HasToken}}<div class="field"><label class="title" for="token">Enrollment token</label>
<input id="token" name="token" type="password" autocomplete="off" required autofocus></div>{{end}}
<div class="field">{{if .Candidates}}<fieldset>
<legend>Your work email</legend>
{{range .Candidates}}<label class="choice"><input type="radio" name="email" value="{{.}}"{{if $.IsCandidate .}} checked{{end}}> {{.}}</label>
{{end}}<label class="choice"><input type="radio" name="email" value="other"{{if .Other}} checked{{end}}> Another email</label>
<input name="email_other" type="email" aria-label="Another email" placeholder="you@company.com"{{if .Other}} value="{{.Email}}"{{end}}>
</fieldset>{{else}}<label class="title" for="email_other">Your work email</label>
<input id="email_other" name="email_other" type="email" required placeholder="you@company.com" value="{{.Email}}">{{end}}</div>
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
<button type="submit">Enroll this device</button>
</form>
{{end}}
</div></main>
</body>
</html>
`))
