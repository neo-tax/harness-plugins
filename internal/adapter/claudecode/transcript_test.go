package claudecode_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/gitident"
)

var update = flag.Bool("update", false, "rewrite expected.jsonl of anonymized fixtures (never synthetic ones)")

func fixtureContext(sessionID string) claudecode.Context {
	branch, head := "hook-branch", "0123456789abcdef0123456789abcdef01234567"
	return claudecode.Context{
		DeviceID: "dev_fixture", AgentVersion: "0.0.0-fixture", SessionID: sessionID,
		Now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
		Git: gitident.Identity{Branch: &branch, HeadSHA: &head},
	}
}

// copyFixture copies a fixture directory into a temp dir, returning the
// main transcript path and the session id.
func copyFixture(t *testing.T, dir string) (string, string) {
	t.Helper()
	dst := t.TempDir()
	var main string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || fi.Name() == "expected.jsonl" {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out := filepath.Join(dst, rel)
		os.MkdirAll(filepath.Dir(out), 0o755)
		b, _ := os.ReadFile(p)
		os.WriteFile(out, b, 0o644)
		if filepath.Dir(rel) == "." && strings.HasSuffix(rel, ".jsonl") {
			main = out
		}
		return nil
	})
	if main == "" {
		t.Fatalf("%s: no main transcript", dir)
	}
	return main, strings.TrimSuffix(filepath.Base(main), ".jsonl")
}

func toLines(t *testing.T, events []any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := contract.ValidateEvent(b); err != nil {
			t.Errorf("event violates contract: %v\n%s", err, b)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}

func readExpected(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// Every fixture's transcript must produce exactly its expected event stream.
// @spec: claude-code-transcript-parser#fixture-driven
// @spec: test-and-release#fixture-pairs-expected-stream
// @spec: claude-code-transcript-parser#dedupe-by-message-id
// @spec: claude-code-transcript-parser#usage-mapping
// @spec: claude-code-transcript-parser#usage-source-transcript
// @spec: claude-code-transcript-parser#usage-unknown-when-absent
// @spec: claude-code-transcript-parser#request-id-from-transcript
// @spec: claude-code-transcript-parser#subagent-transcripts-scanned
// @spec: claude-code-transcript-parser#subagent-usage-counted
// @spec: claude-code-transcript-parser#pr-link-lines
// @spec: claude-code-transcript-parser#branch-per-turn
// @spec: claude-code-transcript-parser#transcript-version-recorded
// @spec: session-event-wire-contract#subagent-turns-belong-to-parent
func TestFixtures(t *testing.T) {
	dirs, _ := filepath.Glob("testdata/fixtures/*")
	if len(dirs) == 0 {
		t.Fatal("no fixtures")
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			main, sid := copyFixture(t, dir)
			events, commit, err := claudecode.TailEvents(fixtureContext(sid), main, cursor.Open(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			commit()
			got := toLines(t, events)
			expPath := filepath.Join(dir, "expected.jsonl")
			if *update && !strings.HasPrefix(filepath.Base(dir), "synthetic") {
				var buf bytes.Buffer
				for _, m := range got {
					b, _ := json.Marshal(m)
					buf.Write(append(b, '\n'))
				}
				os.WriteFile(expPath, buf.Bytes(), 0o644)
			}
			want := readExpected(t, expPath)
			if len(got) != len(want) {
				t.Fatalf("%d events, want %d", len(got), len(want))
			}
			for i := range want {
				if !reflect.DeepEqual(got[i], want[i]) {
					g, _ := json.Marshal(got[i])
					w, _ := json.Marshal(want[i])
					t.Errorf("event %d:\n got  %s\n want %s", i, g, w)
				}
			}
		})
	}
}

// Reading a transcript as it grows, one line per hook, yields the same
// stream as reading it whole: no turn is lost or repeated.
// @spec: claude-code-transcript-parser#turns-from-transcript-tail
// @spec: claude-code-transcript-parser#dedupe-by-message-id
func TestIncrementalReadsMatchWholeRead(t *testing.T) {
	src := "testdata/fixtures/synthetic-2.1.310/fixture-session.jsonl"
	whole, _ := os.ReadFile(src)
	lines := strings.SplitAfter(string(whole), "\n")
	dir := t.TempDir()
	main := filepath.Join(dir, "fixture-session.jsonl")
	store := cursor.Open(t.TempDir())
	var got []any
	for _, line := range lines {
		f, _ := os.OpenFile(main, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		// Write each line in two halves with a hook in between, so a hook
		// sometimes sees a partial last line.
		half := len(line) / 2
		f.WriteString(line[:half])
		evs, commit, err := claudecode.TailEvents(fixtureContext("fixture-session"), main, store)
		if err != nil {
			t.Fatal(err)
		}
		commit()
		got = append(got, evs...)
		f.WriteString(line[half:])
		f.Close()
		evs, commit, _ = claudecode.TailEvents(fixtureContext("fixture-session"), main, store)
		commit()
		got = append(got, evs...)
	}
	want := readExpected(t, "testdata/fixtures/synthetic-2.1.310/expected.jsonl")
	var mainWant []map[string]any
	for _, w := range want {
		if w["subagent_id"] == nil {
			mainWant = append(mainWant, w)
		}
	}
	if g := toLines(t, got); !reflect.DeepEqual(g, mainWant) {
		t.Fatalf("incremental stream differs:\n got %d events\n want %d", len(g), len(mainWant))
	}
}

// Without commit the cursor does not move, so events are re-read until the
// caller has queued them.
func TestCursorAdvancesOnlyOnCommit(t *testing.T) {
	main, sid := copyFixture(t, "testdata/fixtures/synthetic-2.1.310")
	store := cursor.Open(t.TempDir())
	first, _, _ := claudecode.TailEvents(fixtureContext(sid), main, store)
	again, commit, _ := claudecode.TailEvents(fixtureContext(sid), main, store)
	if len(first) == 0 || len(again) != len(first) {
		t.Fatalf("%d then %d", len(first), len(again))
	}
	commit()
	if after, _, _ := claudecode.TailEvents(fixtureContext(sid), main, store); len(after) != 0 {
		t.Fatalf("%d events after commit", len(after))
	}
}

// A transcript rewritten shorter (compaction) is read again from the start.
func TestShrunkTranscriptRereadFromStart(t *testing.T) {
	main, sid := copyFixture(t, "testdata/fixtures/synthetic-2.1.310")
	store := cursor.Open(t.TempDir())
	_, commit, _ := claudecode.TailEvents(fixtureContext(sid), main, store)
	commit()
	b, _ := os.ReadFile(main)
	lines := strings.SplitAfter(string(b), "\n")
	os.WriteFile(main, []byte(lines[len(lines)-2]), 0o644) // only msg_C remains
	evs, _, _ := claudecode.TailEvents(fixtureContext(sid), main, store)
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
}

// Malformed lines are skipped and counted, never fatal.
func TestMalformedLinesSkipped(t *testing.T) {
	main, sid := copyFixture(t, "testdata/fixtures/synthetic-2.1.310")
	f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{not json\n")
	f.WriteString(`{"type":"assistant","timestamp":"2026-10-25T10:00:12Z","message":{"id":"msg_D","usage":"weird"}}` + "\n")
	f.Close()
	evs, _, err := claudecode.TailEvents(fixtureContext(sid), main, cursor.Open(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(evs); n != 6 { // five expected + msg_D with unknown usage
		t.Fatalf("%d events", n)
	}
}

func TestMissingTranscriptIsEmpty(t *testing.T) {
	evs, _, err := claudecode.TailEvents(fixtureContext("s"), filepath.Join(t.TempDir(), "nope.jsonl"), cursor.Open(t.TempDir()))
	if err != nil || len(evs) != 0 {
		t.Fatalf("%v %d", err, len(evs))
	}
}

// @spec: claude-code-transcript-parser#content-never-read-into-events
func TestNoContentInEvents(t *testing.T) {
	main, sid := copyFixture(t, "testdata/fixtures/synthetic-2.1.310")
	evs, _, _ := claudecode.TailEvents(fixtureContext(sid), main, cursor.Open(t.TempDir()))
	b, _ := json.Marshal(evs)
	for _, secret := range []string{"Acme", "acme", "secret reasoning", "id_rsa", "PRIVATE KEY", "Done", "found it", "/Users/someone", "explore the repo"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("event stream contains %q", secret)
		}
	}
}

// @spec: test-and-release#fixture-per-harness-version
func TestEverySupportedVersionHasAFixture(t *testing.T) {
	for _, v := range claudecode.SupportedVersions {
		m, _ := filepath.Glob("testdata/fixtures/*-" + v)
		if len(m) == 0 {
			t.Errorf("no fixture for Claude Code %s", v)
		}
	}
}
