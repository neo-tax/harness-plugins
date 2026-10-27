// Command neo-tax-agent is the single binary every adapter execs. A hook
// runs it with the hook name on the command line and the harness payload on
// standard input; engineers reach its other commands through the harness's
// neo-tax:* entry points.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/agent"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/enroll"
)

// Version is set at release time with -ldflags "-X main.Version=1.2.3", the
// tag without its leading v, matching the plugin manifest's version.
var Version = "dev"

const usage = `usage: neo-tax-agent <command> [flags]

commands:
  hook <name>  handle a harness hook; the payload is read from standard input
  login        enroll this device with an enrollment token, then start a backfill
  backfill     resend events for every transcript on disk, continuing a paused
               backfill where it stopped (--restart starts over from the newest)
  version    print the agent version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, Version)
		return 0
	case "login":
		return login(args[1:], stdin, stdout, stderr)
	case "hook":
		return hook(args[1:], stdin)
	case "backfill":
		return backfill(args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}

// dataDir is the harness-provided plugin data directory.
func dataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if d := os.Getenv("NEO_TAX_DATA_DIR"); d != "" {
		return d
	}
	return os.Getenv("CLAUDE_PLUGIN_DATA")
}

// hook handles one Claude Code hook. It always exits 0: a hook must never
// fail the engineer's session.
func hook(args []string, stdin io.Reader) int {
	if len(args) < 1 {
		return 0
	}
	d := dataDir("")
	if d == "" {
		return 0
	}
	claudecode.RunHook(context.Background(), args[0], stdin, claudecode.HookOptions{
		DataDir: d, AgentVersion: Version, Entrypoint: os.Getenv("CLAUDE_CODE_ENTRYPOINT"),
	})
	return 0
}

func login(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("data-dir", "", "plugin data directory (default $CLAUDE_PLUGIN_DATA)")
	server := fs.String("server", os.Getenv("NEO_TAX_SERVER"), "enrollment server base URL")
	token := fs.String("token", "", "enrollment token")
	email := fs.String("email", "", "the engineer's confirmed work email")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	d := dataDir(*dir)
	if d == "" {
		fmt.Fprintln(stderr, "neo-tax-agent: no plugin data directory; pass --data-dir")
		return 2
	}
	interactive := false
	var readSecret func() (string, error)
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		interactive = true
		readSecret = func() (string, error) {
			// Restore echo if the engineer interrupts at the prompt.
			if st, err := term.GetState(int(f.Fd())); err == nil {
				sig := make(chan os.Signal, 1)
				signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
				done := make(chan struct{})
				defer func() { signal.Stop(sig); close(done) }()
				go func() {
					select {
					case <-sig:
						term.Restore(int(f.Fd()), st)
						fmt.Fprintln(stderr)
						os.Exit(130)
					case <-done:
					}
				}()
			}
			b, err := term.ReadPassword(int(f.Fd()))
			return string(b), err
		}
	}
	// No overall deadline: the engineer may take a while at the prompts.
	// The HTTP client bounds the request itself, and the browser form has
	// its own bound. Without a terminal, the form collects the token so it
	// never passes through the harness conversation.
	err := enroll.Login(context.Background(), enroll.Options{
		DataDir: d, Server: *server, Token: *token, Email: *email,
		Harness: claudecode.Harness, AgentVersion: Version,
		AccountEmail: claudecode.AccountEmail,
		Interactive:  interactive, In: stdin, Out: stdout, ReadSecret: readSecret,
		Browser: !interactive, OpenURL: browserOpener(), TerminalCommand: terminalCommand(d, *server),
		HTTP: &http.Client{Timeout: 30 * time.Second},
	})
	switch {
	case errors.Is(err, enroll.ErrNeedInput):
		return 3
	case errors.Is(err, enroll.ErrLoginNotCompleted):
		return 1 // already reported on stdout
	case err != nil:
		fmt.Fprintln(stderr, "neo-tax-agent:", err)
		return 1
	}
	// @spec: event-queue-and-backfill#backfill-after-login
	// @spec: device-enrollment#backfill-after-interactive-login
	if err := startBackfill(d, false); err != nil {
		fmt.Fprintln(stderr, "neo-tax-agent: could not start the backfill; run neo-tax:backfill:", err)
		return 0
	}
	fmt.Fprintln(stdout, "Sending your existing Claude Code sessions in the background.")
	return 0
}

// openURL opens the login form in the engineer's browser.
var openURL = enroll.OpenBrowser

// browserOpener is openURL, or nil when NEO_TAX_NO_BROWSER is set (tests and
// headless machines), in which case only the form's address is printed.
func browserOpener() func(string) error {
	if os.Getenv("NEO_TAX_NO_BROWSER") != "" {
		return nil
	}
	return openURL
}

// terminalCommand is this login command as an engineer would type it in a
// terminal: this binary, with the data directory the harness would use and
// the server this login was pointed at, which their shell may not set.
func terminalCommand(dataDir, server string) string {
	self, err := os.Executable()
	if err != nil {
		self = "neo-tax-agent"
	}
	c := shellQuote(self) + " login --data-dir " + shellQuote(dataDir)
	if server != "" && server != enroll.DefaultServer {
		c += " --server " + shellQuote(server)
	}
	return c
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '-' || r == '_' || r == '.' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// startBackfill runs "neo-tax-agent backfill" detached from this process,
// so login returns while the backfill runs under its own command.
var startBackfill = func(dataDir string, restart bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"backfill", "--data-dir", dataDir}
	if restart {
		args = append(args, "--restart")
	}
	cmd := exec.Command(self, args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func backfill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("data-dir", "", "plugin data directory (default $CLAUDE_PLUGIN_DATA)")
	background := fs.Bool("background", false, "start the backfill detached and return")
	restart := fs.Bool("restart", false, "ignore a paused backfill and start over from the newest transcript")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	d := dataDir(*dir)
	if d == "" {
		fmt.Fprintln(stderr, "neo-tax-agent: no plugin data directory; pass --data-dir")
		return 2
	}
	if *background {
		switch err := claudecode.CheckBackfill(d); {
		case errors.Is(err, claudecode.ErrBackfillRunning):
			fmt.Fprintln(stdout, "A backfill is already running.")
			return 0
		case err != nil:
			fmt.Fprintln(stderr, "neo-tax-agent:", err)
			return 1
		}
		if err := startBackfill(d, *restart); err != nil {
			fmt.Fprintln(stderr, "neo-tax-agent: could not start the backfill:", err)
			return 1
		}
		fmt.Fprintln(stdout, "Started resending your Claude Code sessions in the background.")
		return 0
	}
	err := claudecode.Backfill(context.Background(), claudecode.BackfillOptions{DataDir: d, AgentVersion: Version, Out: stdout, Restart: *restart})
	if errors.Is(err, claudecode.ErrBackfillRunning) {
		fmt.Fprintln(stdout, "A backfill is already running.")
		return 0
	}
	if err != nil {
		// Login runs the backfill detached, with no terminal to report to.
		reason := "error"
		switch {
		case errors.Is(err, claudecode.ErrCannotSend):
			reason = "device cannot send"
		case errors.Is(err, cursor.ErrBusy):
			reason = "cursor lock busy"
		}
		agent.Logf(d, "backfill failed: %s", reason)
		fmt.Fprintln(stderr, "neo-tax-agent:", err)
		return 1
	}
	return 0
}
