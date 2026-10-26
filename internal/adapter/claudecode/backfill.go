package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gofrs/flock"
	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/agent"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/enroll"
	"github.com/neo-tax/harness-plugins/internal/gitident"
)

var (
	// ErrBackfillRunning means another backfill holds the backfill lock.
	ErrBackfillRunning = errors.New("a backfill is already running")
	// ErrCannotSend means the device is not enrolled or needs re-enrollment,
	// so a backfill would only fill the queue.
	ErrCannotSend = errors.New("this device cannot send yet; run neo-tax:login first")
)

// BackfillOptions configures a backfill.
type BackfillOptions struct {
	DataDir      string
	ConfigDir    string // Claude Code's config directory; ConfigDir() when empty
	AgentVersion string
	Now          func() time.Time
	HTTP         *http.Client
	Out          io.Writer
	// QueueLimit is how many bytes the queue may hold before the backfill
	// stops reading transcripts and waits for the queue to drain. Defaults
	// to half the queue cap, so the cap never evicts the newest sessions a
	// backfill queued first.
	QueueLimit int64
	// WaitBudget bounds the total time a backfill waits out ingest backoff
	// before it pauses. Defaults to 6 hours.
	WaitBudget time.Duration
	// Sleep waits for d or until ctx is done. Defaults to a timer; tests
	// replace it to advance a fake clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// Restart ignores a paused backfill's resume point: every cursor is
	// reset and every transcript is read again, newest first.
	Restart bool
}

// DefaultWaitBudget is how long a backfill waits out ingest backoff, in
// total, before it pauses.
const DefaultWaitBudget = 6 * time.Hour

// minRetry is the shortest wait between flushes while the queue is over its
// limit: when no backoff is recorded, a hook holds the flush lock.
const minRetry = 5 * time.Second

// ConfigDir is Claude Code's configuration directory: $CLAUDE_CONFIG_DIR, or
// ~/.claude.
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// waitResult is how a wait for the queue to drain ended.
type waitResult int

const (
	drained waitResult = iota
	waitNeedsEnrollment
	waitBudgetSpent
	waitCanceled
)

// Backfill emits events for every Claude Code transcript on disk, newest
// first, from the beginning, flushing as it goes. A full run resets every
// cursor first; a run that follows a paused one resumes after the
// transcripts the paused run queued, unless o.Restart is set. It runs as its
// own command, never under a hook.
//
// @spec: event-queue-and-backfill#backfill-scope
// @spec: event-queue-and-backfill#backfill-lock
// @spec: event-queue-and-backfill#backfill-command
// @spec: event-queue-and-backfill#backfill-idempotent-server-side
// @spec: event-queue-and-backfill#backfill-waits-out-backoff
// @spec: event-queue-and-backfill#backfill-resumes-after-pause
func Backfill(ctx context.Context, o BackfillOptions) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ConfigDir == "" {
		o.ConfigDir = ConfigDir()
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.WaitBudget == 0 {
		o.WaitBudget = DefaultWaitBudget
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if err := CheckBackfill(o.DataDir); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(o.DataDir, "backfill.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return ErrBackfillRunning
	}
	defer lock.Unlock()

	a := agent.New(o.DataDir)
	a.Queue.Now = o.Now
	dev, _ := enroll.Load(o.DataDir)
	if _, err := enroll.Client(o.DataDir, o.HTTP); err != nil {
		return ErrCannotSend
	}
	if o.QueueLimit == 0 {
		o.QueueLimit = a.Queue.Cap / 2
	}
	// The client is rebuilt for each flush, so a login during the backfill
	// is picked up instead of the replaced key being used (and rejected).
	lastSize := a.Queue.Size()
	flush := func() {
		defer func() { lastSize = a.Queue.Size() }()
		client, err := enroll.Client(o.DataDir, o.HTTP)
		if err != nil {
			return
		}
		fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		a.Queue.Flush(fctx, client)
	}
	// waitBelow flushes until the queue holds at most limit bytes, sleeping
	// until each recorded next-attempt time, for at most the wait budget
	// across the whole run.
	var waited time.Duration
	waitBelow := func(limit int64) waitResult {
		for {
			if a.Queue.NeedsEnrollment() {
				return waitNeedsEnrollment
			}
			if a.Queue.Size() <= limit {
				return drained
			}
			if ctx.Err() != nil {
				return waitCanceled
			}
			d := max(a.Queue.State().NextAttemptAt.Sub(o.Now()), minRetry)
			if waited+d > o.WaitBudget {
				return waitBudgetSpent
			}
			agent.Logf(o.DataDir, "backfill waiting %ds for the ingest backoff", int(d/time.Second))
			if o.Sleep(ctx, d) != nil {
				return waitCanceled
			}
			waited += d
			flush()
		}
	}

	prev, _ := ReadBackfillStatus(o.DataDir)
	// A resume point is left by a paused run, or by one that was killed
	// after queuing some transcripts; a finished run clears it.
	resume := prev.Resume
	if o.Restart {
		resume = nil
	}
	status := BackfillStatus{StartedAt: o.Now().UTC(), Resume: resume}
	store := cursor.Open(o.DataDir)
	if resume == nil {
		if err := withCursorLock(store, store.Reset); err != nil {
			return err
		}
	} else {
		// Cursors are left alone: the transcripts already queued keep the
		// cursors the paused run recorded, and the ones after the resume
		// point have had none since the paused run reset them, unless a
		// hook has read them since, which also moved them past the resume
		// point.
		status.Sessions, status.Events = prev.Sessions, prev.Events
		fmt.Fprintln(o.Out, "Resuming the paused backfill.")
	}
	writeBackfillStatus(o.DataDir, status)

	transcripts := mainTranscripts(filepath.Join(o.ConfigDir, "projects"))
	if resume != nil {
		transcripts = after(transcripts, *resume)
	}
	sessions, events := status.Sessions, status.Events
	// Cursors were reset, or the transcripts left have none, so transcripts
	// are read from the start; the moves are recorded once per flushed
	// batch rather than per session.
	pending := map[string]cursor.Cursor{}
	commit := func() {
		if len(pending) == 0 {
			return
		}
		if err := withCursorLock(store, func() error { return store.Merge(pending) }); err != nil {
			agent.Logf(o.DataDir, "backfill cursor update failed: %s", errClass(err))
		}
		pending = map[string]cursor.Cursor{}
	}
	// done is the resume point after the transcripts queued so far.
	done := resume
	finish := func(paused bool) {
		commit()
		status.Sessions, status.Events, status.Paused, status.FinishedAt = sessions, events, paused, o.Now().UTC()
		status.Resume = nil
		if paused {
			status.Resume = done
		}
		writeBackfillStatus(o.DataDir, status)
	}
	pause := func(result waitResult) error {
		finish(true)
		switch result {
		case waitNeedsEnrollment:
			return ErrCannotSend
		case waitCanceled:
			return ctx.Err()
		}
		fmt.Fprintf(o.Out, "Backfilled %d sessions (%d events), then paused after waiting %s in total for Neo.Tax to accept events. Run neo-tax:backfill again later to continue where this one stopped.\n", sessions, events, waited)
		return nil
	}
	// A queue already over its limit, such as the one a paused run left,
	// drains before more is read.
	if a.Queue.Size() > o.QueueLimit {
		flush()
		if r := waitBelow(o.QueueLimit); r != drained {
			return pause(r)
		}
	}
	for _, t := range transcripts {
		if ctx.Err() != nil {
			return pause(waitCanceled)
		}
		n, moves, err := backfillOne(a, t.path, dev.DeviceID, o)
		if err != nil {
			agent.Logf(o.DataDir, "backfill transcript failed: %s", errClass(err))
		}
		for p, c := range moves {
			pending[p] = c
		}
		if n > 0 {
			sessions++
			events += n
		}
		if done == nil || !t.mod.Equal(done.Boundary) {
			done = &ResumePoint{Boundary: t.mod}
		}
		done.AtBoundary++
		// Flush about a batch at a time: once a batch has been queued since
		// the last flush, whether or not that flush drained the queue.
		size := a.Queue.Size()
		lastSize = min(lastSize, size) // a hook may have drained the queue since
		if size-lastSize < min(flushEvery, o.QueueLimit) {
			continue
		}
		flush()
		commit()
		// Record progress, so a run killed from here on resumes rather
		// than starts over.
		status.Sessions, status.Events, status.Resume = sessions, events, done
		writeBackfillStatus(o.DataDir, status)
		// Reading on while the queue is over its limit would only push it
		// to the cap, so wait out the backoff first.
		if r := waitBelow(o.QueueLimit); r != drained {
			return pause(r)
		}
	}
	flush()
	switch r := waitBelow(0); r {
	case drained:
		finish(false)
		fmt.Fprintf(o.Out, "Backfilled %d sessions (%d events).\n", sessions, events)
	case waitBudgetSpent:
		// Every transcript is queued; hooks send the rest.
		finish(false)
		fmt.Fprintf(o.Out, "Backfilled %d sessions (%d events). Neo.Tax is not accepting events right now, so some are still queued; they are sent as you keep using Claude Code.\n", sessions, events)
	default:
		return pause(r)
	}
	return nil
}

// after returns the transcripts a paused backfill had not yet queued:
// those modified before the resume point's boundary, and those modified at
// it beyond the first AtBoundary. Transcripts modified after the boundary
// were queued by the paused run, or have been written since it reset their
// cursors, in which case the hook that wrote them read them from the start.
func after(ts []transcript, r ResumePoint) []transcript {
	var out []transcript
	at := 0
	for _, t := range ts {
		switch {
		case t.mod.After(r.Boundary):
			continue
		case t.mod.Equal(r.Boundary):
			at++
			if at <= r.AtBoundary {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// flushEvery is roughly one ingest batch of queued events.
const flushEvery = 256 << 10

// BackfillStatus is the last backfill's progress, kept in backfill.json.
// Sessions and Events count from the start of the full run a resumed run
// continues.
type BackfillStatus struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Sessions   int       `json:"sessions"`
	Events     int       `json:"events"`
	Paused     bool      `json:"paused,omitempty"`
	// Resume is where the next backfill continues this one: set while a
	// run is in progress and when it pauses, nil once it finishes or when
	// it had queued nothing, so the next run starts over.
	Resume *ResumePoint `json:"resume,omitempty"`
}

// ResumePoint marks how far a paused backfill got through the transcripts,
// newest first, without naming any of them: every transcript modified after
// Boundary was queued, and so were the first AtBoundary of those modified
// at it, in path order.
type ResumePoint struct {
	Boundary   time.Time `json:"boundary"`
	AtBoundary int       `json:"at_boundary"`
}

func writeBackfillStatus(dataDir string, s BackfillStatus) {
	b, _ := json.Marshal(s)
	tmp := filepath.Join(dataDir, "backfill.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, filepath.Join(dataDir, "backfill.json"))
	}
}

// ReadBackfillStatus returns the last backfill's status.
func ReadBackfillStatus(dataDir string) (BackfillStatus, error) {
	var s BackfillStatus
	b, err := os.ReadFile(filepath.Join(dataDir, "backfill.json"))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// CheckBackfill reports whether a backfill can run now: the device is
// enrolled and accepted, and no other backfill holds the lock.
func CheckBackfill(dataDir string) error {
	if _, err := enroll.Load(dataDir); err != nil {
		return ErrCannotSend
	}
	if agent.New(dataDir).Queue.NeedsEnrollment() {
		return ErrCannotSend
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	l := flock.New(filepath.Join(dataDir, "backfill.lock"))
	ok, err := l.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return ErrBackfillRunning
	}
	return l.Unlock()
}

func withCursorLock(store *cursor.Store, fn func() error) error {
	unlock, err := store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// mainTranscripts lists every session's main transcript under projects/,
// newest first by modification time, then by path.
//
// Older Claude Code builds wrote subagent transcripts beside the main ones
// as agent-<id>.jsonl; newer builds put them under <session>/subagents/,
// which tailWith reads. The legacy files are skipped rather than attached to
// their parent through the sessionId on their lines: the hook path never
// reads them, no fixture covers that format, and backfilling one on its own
// would open a session named agent-<id> and count its turns as main-thread
// turns.
//
// A transcript's file name becomes its session id, so only files named as
// Claude Code names them, a UUID, are read. Anything else under projects/
// (a hand-made copy, a renamed backup) is skipped rather than sending its
// file name in an event. The legacy agent-<id>.jsonl files fail the same
// check.
//
// @spec: session-event-wire-contract#subagent-turns-belong-to-parent
// @spec: event-queue-and-backfill#backfill-session-id-from-harness
func mainTranscripts(projectsDir string) []transcript {
	all, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	var ts []transcript
	for _, p := range all {
		if !isSessionID(sessionIDOf(p)) {
			continue
		}
		t := transcript{path: p}
		if fi, err := os.Stat(p); err == nil {
			t.mod = fi.ModTime().UTC()
		}
		ts = append(ts, t)
	}
	// Ties are broken by path so that a resume point's count of transcripts
	// at its boundary names the same ones on the next run.
	sort.SliceStable(ts, func(i, j int) bool {
		if !ts[i].mod.Equal(ts[j].mod) {
			return ts[i].mod.After(ts[j].mod)
		}
		return ts[i].path < ts[j].path
	})
	return ts
}

// transcript is a main transcript and its modification time.
type transcript struct {
	path string
	mod  time.Time
}

// sessionStart is what a backfill knows about how a past session began. Cwd
// is read only to find the repository; it never reaches an event.
type sessionStart struct {
	Timestamp  time.Time `json:"timestamp"`
	Cwd        string    `json:"cwd"`
	GitBranch  string    `json:"gitBranch"`
	Entrypoint string    `json:"entrypoint"`
	Version    string    `json:"version"`
}

func readSessionStart(path string) (sessionStart, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionStart{}, false
	}
	defer f.Close()
	var out sessionStart
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	// These fields appear in a session's first lines; look a bounded way
	// past the first timestamped line rather than parse the whole file.
	lookahead := 50
	for sc.Scan() {
		if found {
			if lookahead == 0 {
				break
			}
			lookahead--
		}
		var s sessionStart
		if json.Unmarshal(sc.Bytes(), &s) != nil || s.Timestamp.IsZero() {
			continue
		}
		if !found {
			out, found = s, true
		}
		if out.Cwd == "" {
			out.Cwd = s.Cwd
		}
		if out.GitBranch == "" {
			out.GitBranch = s.GitBranch
		}
		if out.Entrypoint == "" {
			out.Entrypoint = s.Entrypoint
		}
		if out.Version == "" {
			out.Version = s.Version
		}
		if out.Cwd != "" && out.GitBranch != "" && out.Entrypoint != "" && out.Version != "" {
			break
		}
	}
	return out, found
}

// backfillOne queues a past session's session.opened and every turn and
// pull-request link in its transcripts. The session's repository comes from
// its working directory as it is today; branches come from the transcript,
// and head_sha is null because the commit at the time is unknown.
func backfillOne(a *agent.Agent, path, deviceID string, o BackfillOptions) (int, map[string]cursor.Cursor, error) {
	start, ok := readSessionStart(path)
	if !ok {
		return 0, nil, nil
	}
	git := gitident.Identify(start.Cwd)
	sessionID := sessionIDOf(path)
	ctx := Context{DeviceID: deviceID, AgentVersion: o.AgentVersion, SessionID: sessionID, Now: o.Now()}

	res, moves, err := tailWith(ctx, path, nil)
	if err != nil {
		// Keep what was read; moves holds only the transcripts read
		// successfully.
		agent.Logf(o.DataDir, "backfill transcript read failed: %s", errClass(err))
	}
	opened := contract.NewOpened(ctx.envelope(start.Version), contract.OpenedFields{
		StartedAt:    start.Timestamp.UTC(),
		Entrypoint:   nonEmpty(start.Entrypoint),
		Repo:         git.Repo,
		Branch:       nonEmpty(start.GitBranch),
		GitUserEmail: git.UserEmail,
	})
	events := append([]any{opened}, res.Events...)
	// The close carries the version the session ended on, as SessionEnd's
	// does; the read's cursor holds the last one the transcript recorded.
	endVersion := start.Version
	if v := moves[path].Version; v != "" {
		endVersion = v
	}
	if c, ok := backfillClose(ctx, path, endVersion); ok {
		events = append(events, c)
	}
	if err := a.Emit(events...); err != nil {
		return 0, nil, err
	}
	return len(events), moves, nil
}

// backfillClose is a past session's session.closed, when its transcript
// ends with current session totals: a clean exit. It carries the side usage
// no turn reports, and is timestamped from the transcript so a re-run sends
// the same event.
//
// @spec: event-queue-and-backfill#backfill-closes-ended-sessions
func backfillClose(ctx Context, path, version string) (contract.Closed, bool) {
	side, err := sideUsage(ctx, path, 0)
	if err != nil || side == nil {
		return contract.Closed{}, false
	}
	ended, err := latestTimestamp(path)
	if err != nil || ended.IsZero() {
		return contract.Closed{}, false
	}
	return contract.NewClosed(ctx.envelope(version), contract.ClosedFields{EndedAt: ended.UTC(), SideUsage: side}), true
}

func sessionIDOf(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

// isSessionID reports whether s has the shape of a Claude Code session id:
// a UUID in its canonical lowercase hyphenated form.
func isSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}
