package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/neo-tax/harness-plugins/internal/cursor"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

// SessionStatus is whether the agent has reported a session.
type SessionStatus string

const (
	StatusQueued SessionStatus = "queued"
	StatusSent   SessionStatus = "sent"
	StatusUnread SessionStatus = "unread"
)

// SessionSummary is one line of the session list. Title is for the
// engineer's terminal only: it never goes into an event, a queue file, a
// cursor, or a log line.
type SessionSummary struct {
	SessionID  string
	LastActive time.Time
	Status     SessionStatus
	Branch     string
	Title      string
}

// ListOptions configures ListSessions.
type ListOptions struct {
	DataDir   string
	ConfigDir string // Claude Code's config directory; ConfigDir() when empty
	Limit     int    // 0 lists every session
}

// ListSessions returns the most recently active sessions, newest first,
// with the title Claude Code gave each and whether the agent has reported
// it. It reads the plugin data directory without locking or writing it.
//
// @spec: session-list#sessions-same-scope-as-backfill
// @spec: session-list#report-status
// @spec: session-list#sessions-read-only
// @spec: session-list#title-local-only
func ListSessions(o ListOptions) []SessionSummary {
	if o.ConfigDir == "" {
		o.ConfigDir = ConfigDir()
	}
	ts := mainTranscripts(filepath.Join(o.ConfigDir, "projects"))
	if o.Limit > 0 && len(ts) > o.Limit {
		ts = ts[:o.Limit]
	}
	cursors := cursor.Open(o.DataDir).All()
	queued := queue.New(o.DataDir).SessionIDs()
	out := make([]SessionSummary, 0, len(ts))
	for _, t := range ts {
		id := sessionIDOf(t.path)
		s := SessionSummary{SessionID: id, LastActive: t.mod, Status: StatusUnread, Title: readTitle(t.path)}
		if start, ok := readSessionStart(t.path); ok {
			s.Branch = start.GitBranch
		}
		switch _, read := cursors[t.path]; {
		case queued[id]:
			s.Status = StatusQueued
		case read:
			s.Status = StatusSent
		}
		out = append(out, s)
	}
	return out
}

// Markers for the lines that carry a title, checked before a line is parsed.
var (
	aiTitleMarker  = []byte(`"ai-title"`)
	nameMarkers    = [][]byte{[]byte(`"custom-title"`), []byte(`"agent-name"`)}
	anyTitleMarker = append([][]byte{aiTitleMarker}, nameMarkers...)
)

// readTitle is the last name the engineer gave the session, or else the
// last title Claude Code generated for it.
//
// @spec: session-list#title-precedence
func readTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var generated, named string
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		raw, err := r.ReadBytes('\n')
		if hasAny(raw, anyTitleMarker) {
			var l struct {
				Type        string `json:"type"`
				AITitle     string `json:"aiTitle"`
				CustomTitle string `json:"customTitle"`
				AgentName   string `json:"agentName"`
			}
			if json.Unmarshal(raw, &l) == nil {
				switch {
				case l.Type == "ai-title" && l.AITitle != "":
					generated = l.AITitle
				case l.Type == "custom-title" && l.CustomTitle != "":
					named = l.CustomTitle
				case l.Type == "agent-name" && l.AgentName != "":
					named = l.AgentName
				}
			}
		}
		if err != nil {
			break
		}
	}
	if named != "" {
		return named
	}
	return generated
}

func hasAny(b []byte, subs [][]byte) bool {
	for _, s := range subs {
		if bytes.Contains(b, s) {
			return true
		}
	}
	return false
}
