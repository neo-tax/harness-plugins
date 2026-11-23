// Package queue stores session events on disk between hooks and flushes them
// to the ingest endpoint. It runs only inside a hook or command invocation;
// nothing runs between invocations.
//
// Layout under the plugin data directory:
//
//	queue/active.jsonl       appended to by every hook, one event per line
//	queue/seg-<n>.jsonl      sealed segments, flushed oldest first
//	deadletter/<id>.json     events the server rejected for schema reasons
//	state.json               backoff, re-enrollment flag, dropped count, offsets
//	state.lock, flush.lock   cross-process locks
//
// Hooks append to active.jsonl only. A flush first seals it into a numbered
// segment under the state lock, and sends sealed segments only, so no hook
// ever appends to a segment a flush is about to delete.
//
// @spec: event-queue-and-backfill#queue-is-jsonl-segments
// @spec: event-queue-and-backfill#no-database
// @spec: event-queue-and-backfill#no-daemon
package queue

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/ingest"
)

const (
	initialBackoff = 30 * time.Second
	maxBackoff     = time.Hour
	deadLetterTTL  = 14 * 24 * time.Hour
	activeName     = "active.jsonl"
)

// Sender delivers one batch to the ingest endpoint.
type Sender interface {
	Send(ctx context.Context, events []json.RawMessage) (contract.IngestResponse, error)
}

// Queue is the on-disk event queue rooted at a plugin data directory.
type Queue struct {
	Dir        string
	SegmentMax int64 // a segment is sealed once it exceeds this size
	Cap        int64 // total queue size above which the oldest segment is deleted
	BatchSize  int   // events per ingest request
	Now        func() time.Time
}

// New returns a queue over dir with the spec's defaults.
//
// @spec: event-queue-and-backfill#segment-rotation
// @spec: event-queue-and-backfill#queue-cap
// @spec: event-queue-and-backfill#flush-batch-size
func New(dir string) *Queue {
	return &Queue{Dir: dir, SegmentMax: 1 << 20, Cap: 50 << 20, BatchSize: 500, Now: time.Now}
}

// State is persisted in state.json and shared by every invocation.
type State struct {
	NextSegment     uint64           `json:"next_segment"`
	Offsets         map[string]int   `json:"offsets,omitempty"`
	BackoffSeconds  int64            `json:"backoff_seconds,omitempty"`
	NextAttemptAt   time.Time        `json:"next_attempt_at,omitempty"`
	NeedsEnrollment bool             `json:"needs_enrollment,omitempty"`
	DroppedEvents   int64            `json:"dropped_events,omitempty"`
	DroppedFields   map[string]int64 `json:"dropped_fields,omitempty"`
	InvalidEvents   map[string]int64 `json:"invalid_events,omitempty"`
}

func (q *Queue) queueDir() string { return filepath.Join(q.Dir, "queue") }
func (q *Queue) deadDir() string  { return filepath.Join(q.Dir, "deadletter") }

// withState runs fn with the state lock held, persisting the state if fn
// returns nil.
func (q *Queue) withState(fn func(*State) error) error {
	if err := os.MkdirAll(q.queueDir(), 0o700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(q.Dir, "state.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	st := q.readState()
	if err := fn(&st); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(q.Dir, "state.json"), mustJSON(st))
}

func (q *Queue) readState() State {
	var st State
	if b, err := os.ReadFile(filepath.Join(q.Dir, "state.json")); err == nil {
		json.Unmarshal(b, &st)
	}
	if st.Offsets == nil {
		st.Offsets = map[string]int{}
	}
	if st.DroppedFields == nil {
		st.DroppedFields = map[string]int64{}
	}
	if st.InvalidEvents == nil {
		st.InvalidEvents = map[string]int64{}
	}
	return st
}

// Append queues events, one JSON document per line.
func (q *Queue) Append(events [][]byte) error {
	var buf bytes.Buffer
	for _, ev := range events {
		if bytes.ContainsAny(ev, "\r\n") || !json.Valid(ev) {
			return errors.New("queue: event must be single-line JSON")
		}
		buf.Write(ev)
		buf.WriteByte('\n')
	}
	return q.withState(func(st *State) error {
		active := filepath.Join(q.queueDir(), activeName)
		f, err := os.OpenFile(active, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		out := buf.Bytes()
		// A hook killed mid-write leaves a partial last line; start on a
		// fresh line so it stays one corrupt line instead of swallowing ours.
		if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
			last := make([]byte, 1)
			if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
				out = append([]byte{'\n'}, out...)
			}
		}
		_, werr := f.Write(out)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			return errors.Join(werr, cerr)
		}
		if fi, err := os.Stat(active); err == nil && fi.Size() > q.SegmentMax {
			if err := q.seal(st); err != nil {
				return err
			}
		}
		return q.enforceCap(st)
	})
}

// seal renames the active segment to the next numbered segment.
func (q *Queue) seal(st *State) error {
	active := filepath.Join(q.queueDir(), activeName)
	fi, err := os.Stat(active)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	// Number past every existing segment, so a lost or stale state.json can
	// neither overwrite a segment nor sort new events before old ones.
	for _, seg := range q.sealed() {
		var n uint64
		if _, err := fmt.Sscanf(filepath.Base(seg), "seg-%d.jsonl", &n); err == nil && n > st.NextSegment {
			st.NextSegment = n
		}
	}
	st.NextSegment++
	dst := filepath.Join(q.queueDir(), fmt.Sprintf("seg-%016d.jsonl", st.NextSegment))
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("queue: refusing to overwrite %s", filepath.Base(dst))
	}
	return os.Rename(active, dst)
}

func (q *Queue) sealed() []string {
	m, _ := filepath.Glob(filepath.Join(q.queueDir(), "seg-*.jsonl"))
	sort.Strings(m)
	return m
}

// Size is the queue's total size in bytes.
func (q *Queue) Size() int64 {
	var total int64
	entries, _ := os.ReadDir(q.queueDir())
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".jsonl") {
			total += fi.Size()
		}
	}
	return total
}

// SessionIDs is the set of session ids the queue holds an event for, read
// without a lock: a segment a flush deletes meanwhile is skipped, and its
// events count as sent.
//
// @spec: session-list#report-status
func (q *Queue) SessionIDs() map[string]bool {
	out := map[string]bool{}
	paths := append(q.sealed(), filepath.Join(q.queueDir(), activeName))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		r := bufio.NewReaderSize(f, 64<<10)
		for {
			raw, err := r.ReadBytes('\n')
			var ev struct {
				SessionID string `json:"session_id"`
			}
			if len(raw) > 0 && json.Unmarshal(raw, &ev) == nil && ev.SessionID != "" {
				out[ev.SessionID] = true
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	return out
}

// enforceCap deletes the oldest sealed segments while the queue exceeds its
// cap, counting the unsent events it discards.
//
// @spec: event-queue-and-backfill#queue-cap
// @spec: event-queue-and-backfill#dropped-events-reported
func (q *Queue) enforceCap(st *State) error {
	for q.Size() > q.Cap {
		segs := q.sealed()
		if len(segs) == 0 {
			if err := q.seal(st); err != nil || len(q.sealed()) == 0 {
				return err
			}
			continue
		}
		oldest := segs[0]
		name := filepath.Base(oldest)
		n, _ := countLines(oldest)
		st.DroppedEvents += int64(max(n-st.Offsets[name], 0))
		delete(st.Offsets, name)
		if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ConsumeDropped subtracts n from the dropped count once an event carrying
// n has been queued; drops counted since it was read are kept.
//
// @spec: event-queue-and-backfill#dropped-events-reported
func (q *Queue) ConsumeDropped(n int64) error {
	if n == 0 {
		return nil
	}
	return q.withState(func(st *State) error {
		st.DroppedEvents = max(st.DroppedEvents-n, 0)
		return nil
	})
}

// RecordDroppedFields counts fields the allowlist removed, by name, in the
// agent's local diagnostics.
//
// @spec: field-allowlist#unknown-field-dropped-and-counted
func (q *Queue) RecordDroppedFields(names []string) error {
	if len(names) == 0 {
		return nil
	}
	return q.withState(func(st *State) error {
		for _, n := range names {
			st.DroppedFields[n]++
		}
		return nil
	})
}

// RecordInvalidEvents counts, by event kind, events the agent refused to
// queue because their values do not match the wire contract, in the agent's
// local diagnostics. It is kept apart from DroppedEvents, which counts events
// the cap discarded and is reported on the wire as dropped_events.
//
// @spec: field-allowlist#values-validated-before-queue
func (q *Queue) RecordInvalidEvents(kinds []string) error {
	if len(kinds) == 0 {
		return nil
	}
	return q.withState(func(st *State) error {
		for _, k := range kinds {
			st.InvalidEvents[k]++
		}
		return nil
	})
}

// State returns a snapshot of the persisted state.
func (q *Queue) State() State {
	var out State
	q.withState(func(st *State) error { out = *st; return errors.New("read only") })
	return out
}

// NeedsEnrollment reports whether a 401 or 403 has stopped flushing.
func (q *Queue) NeedsEnrollment() bool { return q.State().NeedsEnrollment }

// Enrolled clears the re-enrollment flag and any backoff so the next hook
// flushes.
func (q *Queue) Enrolled() error {
	return q.withState(func(st *State) error {
		st.NeedsEnrollment = false
		st.BackoffSeconds = 0
		st.NextAttemptAt = time.Time{}
		return nil
	})
}

// FlushResult describes what one Flush did.
type FlushResult struct {
	Attempted       bool // at least one request was sent
	Sent            int  // events the server accounted for
	DeadLettered    int
	Failed          bool // a request failed and backoff was recorded
	NeedsEnrollment bool
}

type pending struct {
	raw  json.RawMessage
	seg  string
	line int // 0-based line index within seg
}

// Flush sends queued events, oldest first, until the queue is empty, a
// request fails, or ctx is done. It does nothing while the device needs
// re-enrollment, before the persisted next-attempt time, or while another
// invocation holds the flush lock.
//
// @spec: event-queue-and-backfill#flush-lock
// @spec: event-queue-and-backfill#backoff-persisted
// @spec: event-queue-and-backfill#auth-rejection-stops-flushing
// @spec: device-enrollment#rejected-key-stops-flushing
func (q *Queue) Flush(ctx context.Context, s Sender) (FlushResult, error) {
	var res FlushResult
	st := q.State()
	if st.NeedsEnrollment || q.Now().Before(st.NextAttemptAt) {
		return res, nil
	}
	if err := os.MkdirAll(q.Dir, 0o700); err != nil {
		return res, err
	}
	lock := flock.New(filepath.Join(q.Dir, "flush.lock"))
	ok, err := lock.TryLock()
	if err != nil || !ok {
		return res, err
	}
	defer lock.Unlock()
	// Another invocation may have recorded a rejection or backoff while we
	// waited for the lock.
	if st := q.State(); st.NeedsEnrollment || q.Now().Before(st.NextAttemptAt) {
		return res, nil
	}

	if err := q.withState(func(st *State) error { return q.seal(st) }); err != nil {
		return res, err
	}
	for ctx.Err() == nil {
		batch, corrupt, ends, err := q.nextBatch()
		if err != nil {
			return res, err
		}
		// Corrupt lines are dead-lettered and the batch's range advanced
		// together, only once the batch reaches a terminal outcome, so a
		// failed send never moves the offset past unsent lines.
		settleCorrupt := func() error {
			for _, p := range corrupt {
				if err := q.deadLetter(p.raw, "corrupt queue line"); err != nil {
					return err
				}
				res.DeadLettered++
			}
			return nil
		}
		if len(batch) == 0 {
			if err := settleCorrupt(); err != nil {
				return res, err
			}
			if err := q.advance(corrupt, ends); err != nil {
				return res, err
			}
			if len(corrupt) == 0 {
				break
			}
			continue
		}
		raws := make([]json.RawMessage, len(batch))
		for i, p := range batch {
			raws[i] = p.raw
		}
		res.Attempted = true
		resp, err := s.Send(ctx, raws)
		var se *ingest.StatusError
		switch {
		case err == nil && accountsFor(resp, len(batch)):
			// @spec: event-queue-and-backfill#schema-rejection-dead-letters
			for _, r := range resp.Rejected {
				if err := q.deadLetter(batch[r.Index].raw, r.Reason); err != nil {
					return res, err
				}
				res.DeadLettered++
			}
			res.Sent += len(batch)
			// @spec: event-queue-and-backfill#backoff-resets-on-success
			if err := q.withState(func(st *State) error {
				st.BackoffSeconds, st.NextAttemptAt = 0, time.Time{}
				return nil
			}); err != nil {
				return res, err
			}
		case errors.As(err, &se) && se.IsAuth():
			res.NeedsEnrollment = true
			return res, q.withState(func(st *State) error { st.NeedsEnrollment = true; return nil })
		default:
			// A request-level 422 is grouped here too: individual events are
			// rejected in the response body, so a whole-request rejection is
			// systemic and would recur for every batch. A 2xx response that
			// does not account for every event exactly once is grouped here
			// too: advancing on it could lose an event the server rejected.
			// @spec: event-queue-and-backfill#server-and-network-errors-back-off
			res.Failed = true
			return res, q.backOff()
		}
		if err := settleCorrupt(); err != nil {
			return res, err
		}
		if err := q.advance(append(batch, corrupt...), ends); err != nil {
			return res, err
		}
	}
	return res, nil
}

// accountsFor reports whether resp accounts for each of n sent events exactly
// once: the counts add up and every rejection names a distinct in-range index.
func accountsFor(resp contract.IngestResponse, n int) bool {
	if resp.Accepted+resp.Duplicates+len(resp.Rejected) != n {
		return false
	}
	seen := make(map[int]bool, len(resp.Rejected))
	for _, r := range resp.Rejected {
		if r.Index < 0 || r.Index >= n || seen[r.Index] {
			return false
		}
		seen[r.Index] = true
	}
	return true
}

func (q *Queue) backOff() error {
	return q.withState(func(st *State) error {
		next := time.Duration(st.BackoffSeconds) * time.Second * 2
		if st.BackoffSeconds == 0 {
			next = initialBackoff
		}
		next = min(next, maxBackoff)
		st.BackoffSeconds = int64(next / time.Second)
		st.NextAttemptAt = q.Now().Add(next)
		return nil
	})
}

// nextBatch reads up to BatchSize unsent events from sealed segments, oldest
// first. Lines that are not valid JSON are returned separately. ends maps a
// segment to its line count when the read reached its end.
func (q *Queue) nextBatch() (batch, corrupt []pending, ends map[string]int, err error) {
	st := q.State()
	ends = map[string]int{}
	for _, path := range q.sealed() {
		name := filepath.Base(path)
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // dropped by the cap since listing
		}
		if err != nil {
			return nil, nil, nil, err
		}
		r := bufio.NewReaderSize(f, 64<<10)
		line, done := 0, st.Offsets[name]
		eof := false
		for len(batch) < q.BatchSize {
			b, rerr := r.ReadBytes('\n')
			if rerr == io.EOF && len(b) == 0 {
				eof = true
				break
			}
			if rerr != nil && rerr != io.EOF {
				f.Close()
				return nil, nil, nil, rerr
			}
			if line >= done {
				b = bytes.TrimRight(b, "\r\n")
				p := pending{raw: append(json.RawMessage{}, b...), seg: name, line: line}
				if json.Valid(b) {
					batch = append(batch, p)
				} else {
					corrupt = append(corrupt, p)
				}
			}
			line++
		}
		if !eof {
			// Peek: a full batch that ends exactly at the segment's end.
			if _, perr := r.Peek(1); perr == io.EOF {
				eof = true
			}
		}
		f.Close()
		if eof {
			ends[name] = line
		}
		if len(batch) >= q.BatchSize {
			break
		}
	}
	return batch, corrupt, ends, nil
}

// advance records handled lines and deletes segments every line of which has
// been accepted, recorded as a duplicate, or dead-lettered.
//
// @spec: event-queue-and-backfill#segment-advances-on-success
func (q *Queue) advance(handled []pending, ends map[string]int) error {
	return q.withState(func(st *State) error {
		for _, p := range handled {
			if _, err := os.Stat(filepath.Join(q.queueDir(), p.seg)); errors.Is(err, os.ErrNotExist) {
				continue // dropped by the cap while in flight
			}
			if p.line+1 > st.Offsets[p.seg] {
				st.Offsets[p.seg] = p.line + 1
			}
		}
		for seg, total := range ends {
			if st.Offsets[seg] >= total {
				if err := os.Remove(filepath.Join(q.queueDir(), seg)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				delete(st.Offsets, seg)
			}
		}
		return nil
	})
}

// deadLetter writes one rejected event and the server's reason.
func (q *Queue) deadLetter(ev json.RawMessage, reason string) error {
	if err := os.MkdirAll(q.deadDir(), 0o700); err != nil {
		return err
	}
	var id [6]byte
	rand.Read(id[:])
	now := q.Now()
	rec := struct {
		Reason string          `json:"reason"`
		At     time.Time       `json:"at"`
		Event  json.RawMessage `json:"event,omitempty"`
		Raw    string          `json:"raw,omitempty"`
	}{Reason: reason, At: now}
	if json.Valid(ev) {
		rec.Event = ev
	} else {
		rec.Raw = string(ev)
	}
	path := filepath.Join(q.deadDir(), fmt.Sprintf("%d-%s.json", now.UnixNano(), hex.EncodeToString(id[:])))
	if err := os.WriteFile(path, mustJSON(rec), 0o600); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}

// PruneDeadLetters deletes dead-letter files older than 14 days.
//
// @spec: event-queue-and-backfill#dead-letter-retention
func (q *Queue) PruneDeadLetters() error {
	entries, err := os.ReadDir(q.deadDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := q.Now().Add(-deadLetterTTL)
	for _, e := range entries {
		fi, err := e.Info()
		if err == nil && fi.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(q.deadDir(), e.Name()))
		}
	}
	return nil
}

func countLines(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return bytes.Count(b, []byte{'\n'}), nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
