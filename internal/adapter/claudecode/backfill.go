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
	"strings"
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
	// QueueLimit pauses the backfill once the queue holds this many bytes
	// it could not send. Defaults to half the queue cap, so the cap never
	// evicts the newest sessions a backfill queued first.
	QueueLimit int64
}

// ConfigDir is Claude Code's configuration directory: $CLAUDE_CONFIG_DIR, or
// ~/.claude.
func ConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// Backfill resets every cursor and emits events for every Claude Code
// transcript on disk, newest first, from the beginning, flushing as it
// goes. It runs as its own command, never under a hook.
//
// @spec: event-queue-and-backfill#backfill-scope
// @spec: event-queue-and-backfill#backfill-lock
// @spec: event-queue-and-backfill#backfill-command
// @spec: event-queue-and-backfill#backfill-idempotent-server-side
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

	store := cursor.Open(o.DataDir)
	if err := withCursorLock(store, store.Reset); err != nil {
		return err
	}
	status := BackfillStatus{StartedAt: o.Now().UTC()}
	writeBackfillStatus(o.DataDir, status)

	transcripts := mainTranscripts(filepath.Join(o.ConfigDir, "projects"))
	var sessions, events int
	// Cursors were just reset, so transcripts are read from the start; the
	// moves are recorded once per flushed batch rather than per session.
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
	finish := func(paused bool) {
		commit()
		status.Sessions, status.Events, status.Paused, status.FinishedAt = sessions, events, paused, o.Now().UTC()
		writeBackfillStatus(o.DataDir, status)
	}
	for _, path := range transcripts {
		if ctx.Err() != nil {
			break
		}
		n, moves, err := backfillOne(a, path, dev.DeviceID, o)
		if err != nil {
			agent.Logf(o.DataDir, "backfill transcript failed: %s", errClass(err))
			continue
		}
		for p, c := range moves {
			pending[p] = c
		}
		if n > 0 {
			sessions++
			events += n
		}
		// Flush about a batch at a time: once a batch has been queued since
		// the last flush, whether or not that flush drained the queue.
		size := a.Queue.Size()
		lastSize = min(lastSize, size) // a hook may have drained the queue since
		if size-lastSize < min(flushEvery, o.QueueLimit) {
			continue
		}
		flush()
		commit()
		if a.Queue.NeedsEnrollment() {
			finish(true)
			return ErrCannotSend
		}
		if a.Queue.Size() > o.QueueLimit {
			finish(true)
			fmt.Fprintf(o.Out, "Backfilled %d sessions (%d events), then paused: Neo.Tax is not accepting events right now. Run neo-tax:backfill again later.\n", sessions, events)
			return nil
		}
	}
	flush()
	if a.Queue.NeedsEnrollment() {
		finish(true)
		return ErrCannotSend
	}
	finish(false)
	fmt.Fprintf(o.Out, "Backfilled %d sessions (%d events).\n", sessions, events)
	return nil
}

// flushEvery is roughly one ingest batch of queued events.
const flushEvery = 256 << 10

// BackfillStatus is the last backfill's progress, kept in backfill.json.
type BackfillStatus struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Sessions   int       `json:"sessions"`
	Events     int       `json:"events"`
	Paused     bool      `json:"paused,omitempty"`
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
// newest first by modification time.
//
// Older Claude Code builds wrote subagent transcripts beside the main ones
// as agent-<id>.jsonl; newer builds put them under <session>/subagents/,
// which tailWith reads. The legacy files are skipped rather than attached to
// their parent through the sessionId on their lines: the hook path never
// reads them, no fixture covers that format, and backfilling one on its own
// would open a session named agent-<id> and count its turns as main-thread
// turns.
//
// @spec: session-event-wire-contract#subagent-turns-belong-to-parent
func mainTranscripts(projectsDir string) []string {
	all, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	var paths []string
	for _, p := range all {
		if !strings.HasPrefix(filepath.Base(p), "agent-") {
			paths = append(paths, p)
		}
	}
	mod := map[string]time.Time{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			mod[p] = fi.ModTime()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return mod[paths[i]].After(mod[paths[j]]) })
	return paths
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
	if err := a.Emit(events...); err != nil {
		return 0, nil, err
	}
	return len(events), moves, nil
}

func sessionIDOf(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}
