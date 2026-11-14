package fakeingest

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

// owners decides which session a turn or pull request belongs to when more
// than one session reports it, as a fork does with the turns it copied from
// its parent. The owner is fixed by the sessions that reported the event,
// not by the order they arrived in: a session that is not a fork, then the
// earliest started_at, then the lowest session_id. A session whose
// session.opened has not arrived ranks after every session whose has, and
// the event moves when it does.
//
// @spec: session-event-wire-contract#shared-event-owner
type owners struct {
	// reports holds, per idempotency key, each reporting session's copy.
	reports map[string]map[string]json.RawMessage
	// keys lists, per device and session, the keys the session reported.
	keys map[string][]string
	// sessions holds each device and session's rank once opened.
	sessions map[string]sessionRank
}

type sessionRank struct {
	fork      bool
	startedAt time.Time
}

func newOwners() owners {
	return owners{reports: map[string]map[string]json.RawMessage{}, keys: map[string][]string{}, sessions: map[string]sessionRank{}}
}

func shared(ev map[string]any) bool {
	return ev["event_kind"] == contract.KindTurn || ev["event_kind"] == contract.KindPROpened
}

func sessionKey(ev map[string]any) string {
	return fmt.Sprint(ev["device_id"], "\x00", ev["session_id"])
}

// report records a session's copy of a shared event and reports whether it
// is a session not seen for the key before.
func (o owners) report(k string, ev map[string]any, raw json.RawMessage) bool {
	if !shared(ev) {
		return false
	}
	sid := fmt.Sprint(ev["session_id"])
	if o.reports[k] == nil {
		o.reports[k] = map[string]json.RawMessage{}
	}
	if _, ok := o.reports[k][sid]; ok {
		return false
	}
	o.reports[k][sid] = raw
	sk := sessionKey(ev)
	o.keys[sk] = append(o.keys[sk], k)
	return true
}

// opened records a session's rank and returns the keys it reported, whose
// owner may now change.
func (o owners) opened(ev map[string]any) []string {
	src, _ := ev["source"].(string)
	s, _ := ev["started_at"].(string)
	at, _ := time.Parse(time.RFC3339Nano, s)
	sk := sessionKey(ev)
	o.sessions[sk] = sessionRank{fork: src == "fork", startedAt: at}
	return o.keys[sk]
}

// best is the owning session's copy of the event under key k.
func (o owners) best(k string) json.RawMessage {
	var device string
	sids := make([]string, 0, len(o.reports[k]))
	for sid, raw := range o.reports[k] {
		sids = append(sids, sid)
		if device == "" {
			var ev map[string]any
			json.Unmarshal(raw, &ev)
			device = fmt.Sprint(ev["device_id"])
		}
	}
	rank := func(sid string) (bool, sessionRank) {
		r, ok := o.sessions[device+"\x00"+sid]
		return ok, r
	}
	sort.Slice(sids, func(i, j int) bool {
		oi, ri := rank(sids[i])
		oj, rj := rank(sids[j])
		switch {
		case oi != oj:
			return oi
		case ri.fork != rj.fork:
			return !ri.fork
		case !ri.startedAt.Equal(rj.startedAt):
			return ri.startedAt.Before(rj.startedAt)
		}
		return sids[i] < sids[j]
	})
	return o.reports[k][sids[0]]
}
