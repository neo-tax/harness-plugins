package claudecode

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/gitident"
)

// Context is what a hook knows besides the transcript.
type Context struct {
	DeviceID     string
	AgentVersion string
	SessionID    string
	Now          time.Time
	// Git is the working directory's identity at hook time; its branch is
	// the fallback for turns whose line records none.
	Git gitident.Identity
}

func (c Context) envelope(version string) contract.Envelope {
	var v *string
	if version != "" {
		v = &version
	}
	return contract.Envelope{
		ContractVersion: contract.Version, DeviceID: c.DeviceID, Harness: Harness, HarnessVersion: v,
		AgentVersion: c.AgentVersion, SessionID: c.SessionID, SentAt: c.Now.UTC(),
	}
}

// line is the subset of a transcript line the parser decodes. Message
// content, tool inputs, tool results, titles, and the working directory have
// no field here, so they are never decoded into memory as values.
//
// @spec: claude-code-transcript-parser#content-never-read-into-events
type line struct {
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
	Version      string    `json:"version"`
	GitBranch    string    `json:"gitBranch"`
	RequestID    string    `json:"requestId"`
	AgentID      string    `json:"agentId"`
	Entrypoint   string    `json:"entrypoint"`
	Message      *message  `json:"message"`
	PRNumber     int64     `json:"prNumber"`
	PRRepository string    `json:"prRepository"`
	PRURL        string    `json:"prUrl"`
}

type message struct {
	ID    string          `json:"id"`
	Model string          `json:"model"`
	Usage json.RawMessage `json:"usage"`
}

type transcriptUsage struct {
	InputTokens              *int64  `json:"input_tokens"`
	OutputTokens             *int64  `json:"output_tokens"`
	CacheCreationInputTokens *int64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64  `json:"cache_read_input_tokens"`
	ServiceTier              *string `json:"service_tier"`
	CacheCreation            *struct {
		Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// mapUsage maps a transcript usage block to the contract's usage. When the
// transcript reports cache creation without the per-TTL split (older
// versions), the whole amount is the five-minute tier, the only TTL those
// versions wrote.
//
// @spec: claude-code-transcript-parser#usage-mapping
// @spec: claude-code-transcript-parser#usage-source-transcript
// @spec: claude-code-transcript-parser#usage-unknown-when-absent
func mapUsage(raw json.RawMessage) contract.Usage {
	var u transcriptUsage
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &u) != nil {
		return contract.UnknownUsage()
	}
	if u.InputTokens == nil && u.OutputTokens == nil && u.CacheReadInputTokens == nil && u.CacheCreationInputTokens == nil {
		return contract.UnknownUsage()
	}
	zero := func(p *int64) *int64 {
		if p == nil {
			var z int64
			return &z
		}
		return p
	}
	out := contract.Usage{
		Input: zero(u.InputTokens), Output: zero(u.OutputTokens), CacheRead: zero(u.CacheReadInputTokens),
		ServiceTier: u.ServiceTier, Source: contract.UsageSourceTranscript,
	}
	if u.CacheCreation != nil {
		out.CacheWrite5m, out.CacheWrite1h = zero(u.CacheCreation.Ephemeral5m), zero(u.CacheCreation.Ephemeral1h)
	} else {
		out.CacheWrite5m, out.CacheWrite1h = zero(u.CacheCreationInputTokens), zero(nil)
	}
	return out
}

// tail is what one read of one transcript produced.
type tail struct {
	events   []any
	cursor   cursor.Cursor
	failures int
}

// readTail reads a transcript from its cursor to the last complete line and
// returns one session.turn per assistant message not yet emitted and one
// session.pr_opened per pull-request link line.
//
// @spec: claude-code-transcript-parser#turns-from-transcript-tail
// @spec: claude-code-transcript-parser#dedupe-by-message-id
func readTail(ctx Context, path string, c cursor.Cursor, subagentID *string) (tail, error) {
	out := tail{cursor: c}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return out, err
	}
	// A transcript shorter than the cursor, or whose first line changed,
	// was rewritten (compaction); read it again from the start and let the
	// server's idempotency key discard what it has already seen.
	head := firstLineID(f)
	if fi.Size() < c.Offset || (c.Offset > 0 && c.Head != head) {
		c = cursor.Cursor{Version: c.Version}
	}
	c.Head = head
	out.cursor = c
	if _, err := f.Seek(c.Offset, io.SeekStart); err != nil {
		return out, err
	}
	r := bufio.NewReaderSize(f, 256<<10)
	offset := c.Offset
	seen := map[string]bool{}
	if c.MessageID != "" {
		seen[c.MessageID] = true
	}
	for {
		raw, err := r.ReadBytes('\n')
		if err == io.EOF {
			break // a partial last line is read again by the next hook
		}
		if err != nil {
			return out, err
		}
		offset += int64(len(raw))
		out.cursor.Offset = offset

		var l line
		if json.Unmarshal(raw, &l) != nil {
			out.failures++
			continue
		}
		if l.Version != "" {
			out.cursor.Version = l.Version
		}
		switch {
		case l.Type == "assistant" && l.Message != nil && l.Message.ID != "":
			id := l.Message.ID
			out.cursor.MessageID = id
			if seen[id] {
				continue
			}
			seen[id] = true
			out.events = append(out.events, ctx.turn(l, subagentID, out.cursor.Version))
		case l.Type == "pr-link" && l.PRNumber > 0:
			// @spec: claude-code-transcript-parser#pr-link-lines
			out.events = append(out.events, contract.NewPROpened(ctx.envelope(out.cursor.Version), contract.PROpenedFields{
				Repo: prRepo(l.PRRepository, l.PRURL), Number: l.PRNumber, Timestamp: l.Timestamp.UTC(),
			}))
		}
	}
	return out, nil
}

// firstLineID is a short hash of a file's first line, capped at its first
// 4 KB, or "" when the file has neither a complete first line nor 4 KB.
func firstLineID(f *os.File) string {
	buf := make([]byte, 4<<10)
	n, _ := io.ReadFull(io.NewSectionReader(f, 0, int64(len(buf))), buf)
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i+1]
	} else if n < cap(buf) {
		return ""
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:8])
}

// turn builds a session.turn from the first line of an assistant message.
//
// @spec: claude-code-transcript-parser#request-id-from-transcript
// @spec: claude-code-transcript-parser#branch-per-turn
// @spec: claude-code-transcript-parser#transcript-version-recorded
// @spec: claude-code-transcript-parser#subagent-usage-counted
func (ctx Context) turn(l line, subagentID *string, version string) contract.Turn {
	if l.Version != "" {
		version = l.Version
	}
	f := contract.TurnFields{
		MessageID:  l.Message.ID,
		SubagentID: subagentID,
		Timestamp:  l.Timestamp.UTC(),
		HeadSHA:    ctx.Git.HeadSHA,
		Branch:     ctx.Git.Branch,
		Usage:      mapUsage(l.Message.Usage),
	}
	if l.RequestID != "" {
		f.RequestID = &l.RequestID
	}
	if l.Message.Model != "" {
		f.Model = &l.Message.Model
	}
	if l.GitBranch != "" {
		f.Branch = &l.GitBranch
	}
	return contract.NewTurn(ctx.envelope(version), f)
}

// prRepo parses the recorded "owner/name" with the host from the recorded
// URL, defaulting to github.com.
func prRepo(repository, prURL string) *contract.Repo {
	host := "github.com"
	if u, err := url.Parse(prURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil
	}
	return &contract.Repo{Host: strings.ToLower(host), Owner: strings.ToLower(owner), Name: strings.ToLower(name)}
}

// subagentTranscripts lists the session's subagent transcripts, sorted.
func subagentTranscripts(mainPath string) []string {
	dir := filepath.Join(strings.TrimSuffix(mainPath, ".jsonl"), "subagents")
	m, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(m)
	return m
}

// subagentID is the id in a subagent transcript's file name, agent-<id>.jsonl.
func subagentID(path string) string {
	return strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), ".jsonl"), "agent-")
}

// Result is what one read of a session's transcripts produced.
type Result struct {
	Events []any
	// ParseFailures counts lines that were not valid JSON, so a harness
	// format change shows up in diagnostics.
	ParseFailures int
	// Commit advances the cursors past what was read. Call it only after
	// the events are queued.
	Commit func() error
}

// TailEvents reads the new tail of a session's main transcript and of each
// of its subagent transcripts, from their cursors, and returns their events.
// Subagent turns carry the parent session's id and the subagent's id.
//
// @spec: claude-code-transcript-parser#subagent-transcripts-scanned
// @spec: session-event-wire-contract#subagent-turns-belong-to-parent
func TailEvents(ctx Context, mainPath string, store *cursor.Store) (Result, error) {
	cursors := store.All()
	moves := map[string]cursor.Cursor{}
	var res Result
	var errs []error
	read := func(path string, sub *string) {
		t, err := readTail(ctx, path, cursors[path], sub)
		if err != nil {
			errs = append(errs, err)
			return
		}
		res.Events = append(res.Events, t.events...)
		res.ParseFailures += t.failures
		moves[path] = t.cursor
	}
	read(mainPath, nil)
	for _, sub := range subagentTranscripts(mainPath) {
		id := subagentID(sub)
		read(sub, &id)
	}
	res.Commit = func() error { return store.Update(moves) }
	return res, errors.Join(errs...)
}

// SupportedVersions are the Claude Code versions with a parser fixture under
// testdata/fixtures. Supporting a new version means adding a fixture, not a
// code branch.
//
// @spec: test-and-release#fixture-per-harness-version
var SupportedVersions = []string{"2.1.310"}
