// Package cursor records, for each transcript an adapter reads (including
// subagent transcripts), how far it has read and the last message it
// emitted, so each hook reads only the transcript's new tail.
//
// The cursor file lives in the plugin data directory and never leaves the
// machine.
//
// @spec: event-queue-and-backfill#cursor-per-transcript
package cursor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// Cursor is the read position in one transcript.
type Cursor struct {
	Offset    int64  `json:"offset"`
	MessageID string `json:"message_id,omitempty"`
	Version   string `json:"version,omitempty"` // last harness version seen
	// Head identifies the transcript's first line, so a rewrite of the file
	// is detected even when it is no shorter than Offset.
	Head string `json:"head,omitempty"`
	// Since, set when a fork's session starts, is the time before which the
	// transcript's lines were copied from the parent session, so reads skip
	// the turns and pull requests timestamped before it.
	Since time.Time `json:"since,omitzero"`
	// PRLinks, on a session's main transcript, holds a short hash of each
	// pull request the session has already reported a link to, so a link
	// is sent once per session however often the transcript repeats it.
	PRLinks []string `json:"pr_links,omitempty"`
}

// Store is the cursor file of one plugin data directory.
type Store struct{ dir string }

// Open returns the cursor store in dataDir.
func Open(dataDir string) *Store { return &Store{dir: dataDir} }

func (s *Store) file() string { return filepath.Join(s.dir, "cursors.json") }

func (s *Store) load() map[string]Cursor {
	m := map[string]Cursor{}
	if b, err := os.ReadFile(s.file()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// Get returns the cursor for a transcript path, or the zero cursor.
func (s *Store) Get(path string) Cursor { return s.load()[path] }

// Put records the cursor for a transcript path. Callers hold Lock.
func (s *Store) Put(path string, c Cursor) error {
	m := s.load()
	m[path] = c
	return s.write(m)
}

// All returns every stored cursor, keyed by transcript path.
func (s *Store) All() map[string]Cursor { return s.load() }

// Update records several cursors in one write and drops cursors whose
// transcript no longer exists. Callers hold Lock.
func (s *Store) Update(updates map[string]Cursor) error {
	m := s.load()
	for path, c := range updates {
		m[path] = c
	}
	for path, c := range m {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && !pendingFork(c) {
			delete(m, path)
		}
	}
	return s.write(m)
}

// pendingFork reports whether c is a fork's cursor recorded at its start
// whose transcript Claude Code may not have written yet. It is kept for a
// day, after which a fork that never wrote is forgotten.
func pendingFork(c Cursor) bool {
	return c.Offset == 0 && !c.Since.IsZero() && time.Since(c.Since) < 24*time.Hour
}

// Merge records cursors without moving any stored cursor backwards: a
// cursor already further along in the same file is kept. Callers hold Lock.
func (s *Store) Merge(updates map[string]Cursor) error {
	m := s.load()
	for path, c := range updates {
		if old, ok := m[path]; ok && old.Head == c.Head && old.Offset > c.Offset {
			continue
		}
		m[path] = c
	}
	return s.write(m)
}

// Reset forgets every cursor, so the next read starts each transcript from
// the beginning.
func (s *Store) Reset() error { return s.write(map[string]Cursor{}) }

func (s *Store) write(m map[string]Cursor) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

// ErrBusy means another invocation holds the cursor lock.
var ErrBusy = errors.New("cursor: lock held by another invocation")

// Lock waits up to 10 seconds for the cursor lock, which serializes reading
// transcripts and advancing cursors across concurrent hooks.
func (s *Store) Lock() (func(), error) { return s.LockWithin(10 * time.Second) }

// LockWithin waits up to d for the cursor lock.
func (s *Store) LockWithin(d time.Duration) (func(), error) {
	deadline := time.Now().Add(d)
	for {
		unlock, err := s.TryLock()
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			return unlock, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TryLock takes the cursor lock if it is free.
func (s *Store) TryLock() (func(), error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	l := flock.New(filepath.Join(s.dir, "cursors.lock"))
	ok, err := l.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBusy
	}
	return func() { l.Unlock() }, nil
}
