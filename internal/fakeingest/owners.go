package fakeingest

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

// owners decides which session a turn belongs to when more than one session
// reports it, as a fork does with the turns it copied from its parent. The
// stored copy and the one arriving are compared alone, and the better-ranked
// session's copy is kept: a session that is not a fork, then the earliest
// started_at, then the lowest session_id. A session whose session.opened has
// not arrived ranks after every session whose has. An agent sends a
// session's session.opened before its turns, so a copy dropped earlier lost
// to a session whose rank was already known, and a copy sent again is
// compared again.
//
// @spec: session-event-wire-contract#shared-event-owner
type owners struct {
	// sessions holds each device and session's rank once opened.
	sessions map[string]sessionRank
}

type sessionRank struct {
	fork      bool
	startedAt time.Time
}

func newOwners() owners {
	return owners{sessions: map[string]sessionRank{}}
}

func sessionKey(device, session any) string {
	return fmt.Sprint(device, "\x00", session)
}

// opened records a session's rank.
func (o owners) opened(ev map[string]any) {
	src, _ := ev["source"].(string)
	s, _ := ev["started_at"].(string)
	at, _ := time.Parse(time.RFC3339Nano, s)
	o.sessions[sessionKey(ev["device_id"], ev["session_id"])] = sessionRank{fork: src == "fork", startedAt: at}
}

// replaces reports whether ev, a turn another session already reported,
// belongs to its own session rather than the stored copy's.
func (o owners) replaces(stored json.RawMessage, ev map[string]any) bool {
	if ev["event_kind"] != contract.KindTurn {
		return false
	}
	var cur map[string]any
	json.Unmarshal(stored, &cur)
	if cur["session_id"] == ev["session_id"] {
		return false
	}
	return o.before(ev["device_id"], fmt.Sprint(ev["session_id"]), fmt.Sprint(cur["session_id"]))
}

// before reports whether session a outranks session b on a device.
func (o owners) before(device any, a, b string) bool {
	ra, oa := o.sessions[sessionKey(device, a)]
	rb, ob := o.sessions[sessionKey(device, b)]
	switch {
	case oa != ob:
		return oa
	case ra.fork != rb.fork:
		return !ra.fork
	case !ra.startedAt.Equal(rb.startedAt):
		return ra.startedAt.Before(rb.startedAt)
	}
	return a < b
}
