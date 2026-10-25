package cursor_test

import (
	"testing"

	"github.com/neo-tax/harness-plugins/internal/cursor"
)

// @spec: event-queue-and-backfill#cursor-per-transcript
func TestCursorPerTranscript(t *testing.T) {
	dir := t.TempDir()
	s := cursor.Open(dir)
	if c := s.Get("/a/main.jsonl"); c != (cursor.Cursor{}) {
		t.Fatalf("fresh cursor %+v", c)
	}
	if err := s.Put("/a/main.jsonl", cursor.Cursor{Offset: 120, MessageID: "msg_1", Version: "2.1.310"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("/a/main/subagents/agent-x.jsonl", cursor.Cursor{Offset: 7, MessageID: "msg_9"}); err != nil {
		t.Fatal(err)
	}
	s2 := cursor.Open(dir)
	if c := s2.Get("/a/main.jsonl"); c.Offset != 120 || c.MessageID != "msg_1" || c.Version != "2.1.310" {
		t.Fatalf("%+v", c)
	}
	if c := s2.Get("/a/main/subagents/agent-x.jsonl"); c.Offset != 7 || c.MessageID != "msg_9" {
		t.Fatalf("%+v", c)
	}
	if err := s2.Reset(); err != nil {
		t.Fatal(err)
	}
	if c := cursor.Open(dir).Get("/a/main.jsonl"); c != (cursor.Cursor{}) {
		t.Fatalf("after reset %+v", c)
	}
}

func TestLockSerializes(t *testing.T) {
	s := cursor.Open(t.TempDir())
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TryLock(); err == nil {
		t.Fatal("second lock acquired while held")
	}
	unlock()
	u2, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	u2()
}

// Merge never moves a cursor backwards in the same file, but follows a
// rewritten file.
func TestMergeKeepsFurtherCursor(t *testing.T) {
	s := cursor.Open(t.TempDir())
	s.Put("/t.jsonl", cursor.Cursor{Offset: 100, Head: "h"})
	s.Merge(map[string]cursor.Cursor{"/t.jsonl": {Offset: 50, Head: "h"}})
	if c := s.Get("/t.jsonl"); c.Offset != 100 {
		t.Fatalf("moved back to %d", c.Offset)
	}
	s.Merge(map[string]cursor.Cursor{"/t.jsonl": {Offset: 150, Head: "h"}})
	if c := s.Get("/t.jsonl"); c.Offset != 150 {
		t.Fatalf("did not advance: %d", c.Offset)
	}
	s.Merge(map[string]cursor.Cursor{"/t.jsonl": {Offset: 10, Head: "other"}})
	if c := s.Get("/t.jsonl"); c.Offset != 10 || c.Head != "other" {
		t.Fatalf("rewritten file not followed: %+v", c)
	}
}
