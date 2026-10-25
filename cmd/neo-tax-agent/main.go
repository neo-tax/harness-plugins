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
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/enroll"
)

// Version is set at release time with -ldflags "-X main.Version=v1.2.3".
var Version = "dev"

const usage = `usage: neo-tax-agent <command> [flags]

commands:
  hook <name>  handle a harness hook; the payload is read from standard input
  login        enroll this device with an enrollment token
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
	// The HTTP client bounds the request itself.
	err := enroll.Login(context.Background(), enroll.Options{
		DataDir: d, Server: *server, Token: *token, Email: *email,
		Harness: claudecode.Harness, AgentVersion: Version,
		AccountEmail: claudecode.AccountEmail,
		Interactive:  interactive, In: stdin, Out: stdout, ReadSecret: readSecret,
		HTTP: &http.Client{Timeout: 30 * time.Second},
	})
	switch {
	case errors.Is(err, enroll.ErrNeedInput):
		return 3
	case err != nil:
		fmt.Fprintln(stderr, "neo-tax-agent:", err)
		return 1
	}
	return 0
}
