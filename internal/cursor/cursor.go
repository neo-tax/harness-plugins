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
func (s *Store) Lock() (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
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
