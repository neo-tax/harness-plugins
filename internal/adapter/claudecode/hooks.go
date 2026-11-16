package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/agent"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/enroll"
	"github.com/neo-tax/harness-plugins/internal/gitident"
)

// Hooks are the Claude Code hooks the plugin registers.
//
// @spec: claude-code-transcript-parser#hook-set
var Hooks = []string{"SessionStart", "Stop", "PreCompact", "SessionEnd"}

// payload is the subset of a hook payload the adapter decodes. Fields that
// carry model output or prompts (last_assistant_message, prompt,
// custom_instructions, ...) have no field here, so they are reduced away
// before anything is written to disk.
//
// @spec: field-allowlist#content-bearing-payloads-reduced
type payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
}

// CursorWait is how long a hook waits for another hook's transcript read.
var CursorWait = 10 * time.Second

// HookOptions configures one hook invocation.
type HookOptions struct {
	DataDir      string
	AgentVersion string
	// Entrypoint is how Claude Code was launched (CLAUDE_CODE_ENTRYPOINT).
	Entrypoint   string
	Now          func() time.Time
	HTTP         *http.Client
	FlushTimeout time.Duration
}

// RunHook handles one Claude Code hook: the hook name from the command line
// and the payload from standard input. It never returns an error for bad
// input or a failed flush, so it never fails the engineer's session; those
// are written to the agent log instead.
//
// @spec: claude-code-transcript-parser#hook-exec-shape
// @spec: event-queue-and-backfill#flush-on-every-hook
// @spec: event-queue-and-backfill#no-daemon
func RunHook(ctx context.Context, name string, stdin io.Reader, o HookOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			agent.Logf(o.DataDir, "hook=%s panic", name)
			err = nil
		}
	}()
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.FlushTimeout == 0 {
		o.FlushTimeout = 20 * time.Second
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if !isHook(name) {
		return nil
	}
	var p payload
	if err := json.NewDecoder(io.LimitReader(stdin, 16<<20)).Decode(&p); err != nil || p.SessionID == "" {
		agent.Logf(o.DataDir, "hook=%s unreadable payload", name)
		return nil
	}
	a := agent.New(o.DataDir)
	a.Queue.Now = o.Now
	if err := emit(a, name, p, o); err != nil {
		agent.Logf(o.DataDir, "hook=%s emit failed: %s", name, errClass(err))
	}
	// @spec: event-queue-and-backfill#dead-letter-retention
	a.Queue.PruneDeadLetters()
	if client, err := enroll.Client(o.DataDir, o.HTTP); err == nil {
		fctx, cancel := context.WithTimeout(ctx, o.FlushTimeout)
		defer cancel()
		res, err := a.Queue.Flush(fctx, client)
		if err != nil || res.Failed || res.NeedsEnrollment {
			agent.Logf(o.DataDir, "hook=%s flush sent=%d failed=%v needs_enrollment=%v", name, res.Sent, res.Failed, res.NeedsEnrollment)
		}
	}
	return nil
}

func isHook(name string) bool {
	for _, h := range Hooks {
		if h == name {
			return true
		}
	}
	return false
}

// errClass names an error without its message, which may carry a path.
func errClass(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return "file " + pe.Op
	}
	return fmt.Sprintf("%T", err)
}

func emit(a *agent.Agent, name string, p payload, o HookOptions) error {
	dev, _ := enroll.Load(o.DataDir) // events queue before enrollment; device_id is stamped at send
	git := gitident.Identify(p.Cwd)
	ctx := Context{DeviceID: dev.DeviceID, AgentVersion: o.AgentVersion, SessionID: p.SessionID, Now: o.Now(), Git: git}

	// Side usage only reads transcripts, and on an exit may wait briefly for
	// Claude Code to write its totals, so it runs before the cursor lock is
	// taken.
	var side []contract.SideUsage
	if name == "SessionEnd" {
		var err error
		// Only an exit writes cost-state; on /clear, logout, or /resume none
		// is coming, so the totals are read once without waiting.
		var wait time.Duration
		if exitReasons[p.Reason] {
			wait = CostStateWait
		}
		if side, err = sideUsage(ctx, p.TranscriptPath, wait); err != nil {
			agent.Logf(o.DataDir, "hook=%s side usage unavailable: %s", name, errClass(err))
		}
	}

	// The cursor lock serializes transcript reads and the dropped count.
	// When another hook holds it too long, opened and closed are still
	// emitted, carrying no count. A skipped Stop or PreCompact tail is read
	// by the session's next hook; SessionEnd has no next hook, so it waits
	// three times as long, and a tail it still skips is recovered only by a
	// backfill run with --restart. The long wait relies on the hook being
	// async: Claude Code cancels a synchronous SessionEnd hook after 1.5
	// seconds, and an async one ran to completion after it exited in our
	// probes, though its docs say claude -p may kill one at teardown.
	store := cursor.Open(o.DataDir)
	wait := CursorWait
	if name == "SessionEnd" {
		wait *= 3
	}
	unlock, lockErr := store.LockWithin(wait)
	locked := lockErr == nil
	switch {
	case locked:
		defer unlock()
	case name == "SessionEnd":
		agent.Logf(o.DataDir, "hook=%s cursor lock busy; final transcript tail skipped until a backfill", name)
	default:
		agent.Logf(o.DataDir, "hook=%s cursor lock busy; transcript tail deferred to the next hook", name)
	}

	var events []any
	commit := func() error { return nil }
	// The dropped count rides on opened and closed; it is consumed only once
	// the event carrying it is queued. Reading and consuming it under the
	// cursor lock, which every hook on this data directory holds here,
	// keeps two hooks from carrying the same count.
	var dropped, carried int64
	if locked {
		dropped = a.Queue.State().DroppedEvents
	}
	switch name {
	case "SessionStart":
		// @spec: claude-code-transcript-parser#opened-on-session-start
		events = append(events, contract.NewOpened(ctx.envelope(lastVersion(p.TranscriptPath, store)), contract.OpenedFields{
			StartedAt:     ctx.Now.UTC(),
			Entrypoint:    nonEmpty(o.Entrypoint),
			Source:        nonEmpty(p.Source),
			Repo:          git.Repo,
			Branch:        git.Branch,
			HeadSHA:       git.HeadSHA,
			DroppedEvents: dropped,
		}))
		carried = dropped
		// A fork's transcript starts with copies of its parent's lines,
		// which the parent's hooks already sent. Recording the start lets
		// this session's reads skip them.
		//
		// @spec: claude-code-transcript-parser#fork-skips-copied-lines
		if locked && p.Source == "fork" {
			if _, ok := store.All()[p.TranscriptPath]; !ok {
				if err := store.Put(p.TranscriptPath, cursor.Cursor{Since: ctx.Now.UTC()}); err != nil {
					agent.Logf(o.DataDir, "hook=%s fork start not recorded: %s", name, errClass(err))
				}
			}
		}
	case "Stop", "PreCompact", "SessionEnd":
		if locked {
			res, err := TailEvents(ctx, p.TranscriptPath, store)
			if err != nil {
				agent.Logf(o.DataDir, "hook=%s transcript read failed: %s", name, errClass(err))
			}
			if res.ParseFailures > 0 {
				agent.Logf(o.DataDir, "hook=%s parse_failures=%d harness_version=%s", name, res.ParseFailures, lastVersion(p.TranscriptPath, store))
			}
			events, commit = res.Events, res.Commit
		}
		if name == "SessionEnd" {
			// @spec: claude-code-transcript-parser#closed-on-session-end
			events = append(events, contract.NewClosed(ctx.envelope(lastVersion(p.TranscriptPath, store)), contract.ClosedFields{
				EndedAt: ctx.Now.UTC(), Reason: nonEmpty(p.Reason), DroppedEvents: dropped, SideUsage: side,
			}))
			carried = dropped
		}
	}
	if err := a.Emit(events...); err != nil {
		return err
	}
	// The cursors move as soon as the events are queued, whatever happens
	// to the dropped-count bookkeeping.
	return errors.Join(commit(), a.Queue.ConsumeDropped(carried))
}

// lastVersion is the last harness version seen in the transcript: from its
// cursor when it has been read, else from its last lines.
func lastVersion(path string, store *cursor.Store) string {
	if v := store.Get(path).Version; v != "" {
		return v
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	start := max(fi.Size()-256<<10, 0)
	b := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(b, start); err != nil && err != io.EOF {
		return ""
	}
	version := ""
	for _, l := range bytes.Split(b, []byte{'\n'}) {
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(l, &v) == nil && v.Version != "" {
			version = v.Version
		}
	}
	return version
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
