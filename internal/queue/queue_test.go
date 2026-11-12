package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/fakeingest"
	"github.com/neo-tax/harness-plugins/internal/ingest"
	"github.com/neo-tax/harness-plugins/internal/queue"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type env struct {
	t      *testing.T
	q      *queue.Queue
	clock  *clock
	srv    *httptest.Server
	client *ingest.Client
	device contract.EnrollmentResponse
	hits   *atomic.Int64
}

func setup(t *testing.T) *env {
	t.Helper()
	fake := fakeingest.New(fakeingest.Options{StallFor: 2 * time.Second})
	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events" {
			hits.Add(1)
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	device, err := ingest.Enroll(context.Background(), http.DefaultClient, srv.URL, contract.EnrollmentRequest{
		ContractVersion: contract.Version, EnrollmentToken: "et", Email: "e@example.com", EmailConfirmedBy: contract.EmailConfirmedByEngineer,
		Device: contract.DeviceDescription{Hostname: "h", OS: "o", Arch: "a", Harness: "claude-code", AgentVersion: "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	q := queue.New(t.TempDir())
	q.Now = c.now
	client := &ingest.Client{HTTP: &http.Client{Timeout: 10 * time.Second}, BaseURL: device.IngestBaseURL, DeviceKey: device.DeviceKey}
	return &env{t: t, q: q, clock: c, srv: srv, client: client, device: device, hits: hits}
}

func (e *env) event(msgID string) []byte {
	ev := contract.NewTurn(contract.Envelope{
		ContractVersion: contract.Version, DeviceID: e.device.DeviceID, Harness: "claude-code",
		AgentVersion: "0.1.0", SessionID: "s-1", SentAt: e.clock.t,
	}, contract.TurnFields{MessageID: msgID, Timestamp: e.clock.t, Usage: contract.UnknownUsage()})
	b, _ := json.Marshal(ev)
	return b
}

func (e *env) appendN(prefix string, n int) {
	e.t.Helper()
	lines := make([][]byte, n)
	for i := range lines {
		lines[i] = e.event(fmt.Sprintf("%s-%05d", prefix, i))
	}
	if err := e.q.Append(lines); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) flush() queue.FlushResult {
	e.t.Helper()
	res, err := e.q.Flush(context.Background(), e.client)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) fault(mode string, count int) {
	b, _ := json.Marshal(map[string]any{"mode": mode, "count": count})
	http.Post(e.srv.URL+"/_fake/faults", "application/json", strings.NewReader(string(b)))
}

func (e *env) recorded() []map[string]any {
	resp, err := http.Get(e.srv.URL + "/_fake/events")
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (e *env) files(sub, pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(e.q.Dir, sub, pattern))
	return m
}

func lineCount(t *testing.T, paths []string) int {
	n := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		n += strings.Count(string(b), "\n")
	}
	return n
}

// @spec: event-queue-and-backfill#queue-is-jsonl-segments
func TestAppendWritesOneLinePerEvent(t *testing.T) {
	e := setup(t)
	e.appendN("m", 3)
	segs := e.files("queue", "*.jsonl")
	if len(segs) != 1 || lineCount(t, segs) != 3 {
		t.Fatalf("segments %v", segs)
	}
	b, _ := os.ReadFile(segs[0])
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("not JSON: %s", line)
		}
	}
}

func TestAppendRejectsMultilineEvent(t *testing.T) {
	e := setup(t)
	if err := e.q.Append([][]byte{[]byte("{\n}")}); err == nil {
		t.Fatal("expected error")
	}
}

// @spec: event-queue-and-backfill#segment-rotation
func TestSegmentRotatesPastOneMegabyte(t *testing.T) {
	e := setup(t)
	line := e.event("m")
	n := (1<<20)/len(line) + 50
	e.appendN("m", n)
	e.appendN("n", 1)
	segs := e.files("queue", "*.jsonl")
	if len(segs) < 2 {
		t.Fatalf("no rotation: %v", segs)
	}
	for _, s := range segs {
		if fi, _ := os.Stat(s); fi.Size() > (1<<20)+int64(len(line))*int64(n) {
			t.Errorf("%s is %d bytes", s, fi.Size())
		}
	}
	if lineCount(t, segs) != n+1 {
		t.Fatalf("lost events")
	}
}

// @spec: event-queue-and-backfill#flush-batch-size
// @spec: event-queue-and-backfill#segment-advances-on-success
func TestFlushSendsOldestFirstInBatchesOf500(t *testing.T) {
	e := setup(t)
	e.appendN("a", 700)
	e.appendN("b", 300)
	res := e.flush()
	if res.Sent != 1000 || e.hits.Load() != 2 {
		t.Fatalf("sent %d in %d requests", res.Sent, e.hits.Load())
	}
	got := e.recorded()
	if len(got) != 1000 || got[0]["message_id"] != "a-00000" || got[999]["message_id"] != "b-00299" {
		t.Fatalf("order: first %v last %v", got[0]["message_id"], got[len(got)-1]["message_id"])
	}
	if segs := e.files("queue", "*.jsonl"); len(segs) != 0 {
		t.Fatalf("segments left: %v", segs)
	}
}

// Events appended after a flush starts are kept for the next flush.
func TestFlushThenAppendKeepsNewEvents(t *testing.T) {
	e := setup(t)
	e.appendN("a", 2)
	e.flush()
	e.appendN("b", 2)
	e.flush()
	if got := len(e.recorded()); got != 4 {
		t.Fatalf("recorded %d", got)
	}
}

// @spec: event-queue-and-backfill#schema-rejection-dead-letters
func TestSchemaRejectionsAreDeadLettered(t *testing.T) {
	e := setup(t)
	bad, _ := json.Marshal(map[string]any{"event_kind": "session.turn", "device_id": e.device.DeviceID})
	e.q.Append([][]byte{e.event("ok-1"), bad, e.event("ok-2")})
	res := e.flush()
	if res.Sent != 3 || res.DeadLettered != 1 {
		t.Fatalf("%+v", res)
	}
	dl := e.files("deadletter", "*.json")
	if len(dl) != 1 {
		t.Fatalf("dead letters %v", dl)
	}
	var rec struct {
		Reason string          `json:"reason"`
		Event  json.RawMessage `json:"event"`
	}
	b, _ := os.ReadFile(dl[0])
	json.Unmarshal(b, &rec)
	if rec.Reason == "" || !strings.Contains(string(rec.Event), "session.turn") {
		t.Fatalf("dead letter %s", b)
	}
	if len(e.recorded()) != 2 || len(e.files("queue", "*.jsonl")) != 0 {
		t.Fatal("remaining events did not continue")
	}
}

// @spec: event-queue-and-backfill#server-and-network-errors-back-off
// @spec: event-queue-and-backfill#backoff-persisted
// @spec: event-queue-and-backfill#backoff-resets-on-success
func TestServerErrorsBackOffAndPersist(t *testing.T) {
	e := setup(t)
	e.appendN("a", 3)
	e.fault(fakeingest.FaultServerError, 100)

	waits := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute}
	for i, wait := range waits {
		if res := e.flush(); res.Sent != 0 || !res.Failed {
			t.Fatalf("attempt %d: %+v", i, res)
		}
		// A new Queue over the same directory sees the persisted backoff.
		q2 := queue.New(e.q.Dir)
		q2.Now = e.clock.now
		before := e.hits.Load()
		e.clock.advance(wait - time.Second)
		if res, _ := q2.Flush(context.Background(), e.client); res.Attempted || e.hits.Load() != before {
			t.Fatalf("attempt %d: flushed during backoff", i)
		}
		e.clock.advance(time.Second)
	}
	if lineCount(t, e.files("queue", "*.jsonl")) != 3 {
		t.Fatal("batch not left in place")
	}

	e.fault("", 0)
	if res := e.flush(); res.Sent != 3 {
		t.Fatalf("%+v", res)
	}
	e.appendN("b", 1)
	e.fault(fakeingest.FaultServerError, 1)
	e.flush()
	e.clock.advance(30 * time.Second)
	if res := e.flush(); res.Sent != 1 {
		t.Fatalf("backoff did not reset after success: %+v", res)
	}
}

func TestBackoffCeilingIsOneHour(t *testing.T) {
	e := setup(t)
	e.appendN("a", 1)
	e.fault(fakeingest.FaultServerError, 1000)
	for i := 0; i < 12; i++ {
		e.flush()
		e.clock.advance(time.Hour)
	}
	e.flush()
	e.clock.advance(time.Hour - time.Second)
	if res := e.flush(); res.Attempted {
		t.Fatal("flushed before ceiling elapsed")
	}
	e.clock.advance(time.Second)
	if res := e.flush(); !res.Attempted {
		t.Fatal("backoff exceeded one hour")
	}
}

// @spec: event-queue-and-backfill#server-and-network-errors-back-off
func TestNetworkErrorsAndStallsBackOff(t *testing.T) {
	e := setup(t)
	e.appendN("a", 1)
	// Shorter than the fake's 2-second stall, so the request times out.
	e.client.HTTP.Timeout = 500 * time.Millisecond
	e.fault(fakeingest.FaultStall, 1)
	if res := e.flush(); !res.Failed || res.Sent != 0 {
		t.Fatalf("stall: %+v", res)
	}
	e.clock.advance(30 * time.Second)
	e.srv.Close()
	if res := e.flush(); !res.Failed {
		t.Fatalf("network error: %+v", res)
	}
	if lineCount(t, e.files("queue", "*.jsonl")) != 1 {
		t.Fatal("event lost")
	}
}

// @spec: event-queue-and-backfill#auth-rejection-stops-flushing
// @spec: device-enrollment#rejected-key-stops-flushing
func TestAuthRejectionStopsFlushingUntilEnrollment(t *testing.T) {
	for _, mode := range []string{fakeingest.FaultUnauthorized, fakeingest.FaultForbidden} {
		e := setup(t)
		e.appendN("a", 1)
		e.fault(mode, 1)
		if res := e.flush(); !res.NeedsEnrollment {
			t.Fatalf("%s: %+v", mode, res)
		}
		e.appendN("b", 1)
		e.clock.advance(48 * time.Hour)
		before := e.hits.Load()
		if res := e.flush(); res.Attempted || e.hits.Load() != before {
			t.Fatalf("%s: flushed while needing enrollment", mode)
		}
		if !e.q.NeedsEnrollment() {
			t.Fatal("state not persisted")
		}
		if err := e.q.Enrolled(); err != nil {
			t.Fatal(err)
		}
		if res := e.flush(); res.Sent != 2 {
			t.Fatalf("%s after enrollment: %+v", mode, res)
		}
	}
}

// @spec: event-queue-and-backfill#queue-cap
// @spec: event-queue-and-backfill#dropped-events-reported
func TestQueueCapDropsOldestSegmentAndCounts(t *testing.T) {
	e := setup(t)
	e.q.SegmentMax = 1000
	e.q.Cap = 5000
	line := e.event("a-00000")
	perSeg := 1000/len(line) + 1
	for i := 0; i < 20; i++ {
		e.appendN(fmt.Sprintf("s%02d", i), perSeg)
	}
	var total int64
	for _, s := range e.files("queue", "*.jsonl") {
		fi, _ := os.Stat(s)
		total += fi.Size()
	}
	if total > e.q.Cap {
		t.Fatalf("queue is %d bytes, cap %d", total, e.q.Cap)
	}
	dropped := e.q.TakeDropped()
	kept := lineCount(t, e.files("queue", "*.jsonl"))
	if dropped == 0 || int(dropped)+kept != 20*perSeg {
		t.Fatalf("dropped %d kept %d of %d", dropped, kept, 20*perSeg)
	}
	if e.q.TakeDropped() != 0 {
		t.Fatal("dropped count not reset")
	}
	e.flush()
	if got := e.recorded(); got[len(got)-1]["message_id"] != fmt.Sprintf("s19-%05d", perSeg-1) {
		t.Fatal("newest events were dropped instead of oldest")
	}
}

// @spec: event-queue-and-backfill#flush-lock
func TestFlushLockHeldElsewhereSkipsFlush(t *testing.T) {
	e := setup(t)
	e.appendN("a", 1)
	lock := flock.New(filepath.Join(e.q.Dir, "flush.lock"))
	if ok, err := lock.TryLock(); !ok || err != nil {
		t.Fatal("could not take lock", err)
	}
	if res := e.flush(); res.Attempted || e.hits.Load() != 0 {
		t.Fatalf("%+v", res)
	}
	lock.Unlock()
	if res := e.flush(); res.Sent != 1 {
		t.Fatalf("%+v", res)
	}
}

// @spec: event-queue-and-backfill#dead-letter-retention
func TestDeadLettersOlderThan14DaysAreDeleted(t *testing.T) {
	e := setup(t)
	bad, _ := json.Marshal(map[string]any{"event_kind": "session.turn"})
	e.q.Append([][]byte{bad})
	e.flush()
	dl := e.files("deadletter", "*.json")
	if len(dl) != 1 {
		t.Fatal("no dead letter")
	}
	os.Chtimes(dl[0], e.clock.t, e.clock.t)
	e.clock.advance(14*24*time.Hour - time.Minute)
	e.q.PruneDeadLetters()
	if len(e.files("deadletter", "*.json")) != 1 {
		t.Fatal("pruned too early")
	}
	e.clock.advance(2 * time.Minute)
	e.q.PruneDeadLetters()
	if len(e.files("deadletter", "*.json")) != 0 {
		t.Fatal("not pruned")
	}
}

// @spec: event-queue-and-backfill#no-database
func TestCorruptLineIsDeadLetteredNotFatal(t *testing.T) {
	e := setup(t)
	e.appendN("a", 1)
	f, _ := os.OpenFile(e.files("queue", "*.jsonl")[0], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{not json\n")
	f.Close()
	e.appendN("b", 1)
	res := e.flush()
	if res.Sent != 2 || res.DeadLettered != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestNoQueueNoRequest(t *testing.T) {
	e := setup(t)
	if res := e.flush(); res.Attempted || e.hits.Load() != 0 {
		t.Fatalf("%+v", res)
	}
}

// A corrupt line between valid ones must not move the queue past the valid
// lines when the send that carries them fails.
// @spec: event-queue-and-backfill#no-database
// @spec: event-queue-and-backfill#segment-advances-on-success
func TestCorruptLineBeforeFailedSendLosesNothing(t *testing.T) {
	e := setup(t)
	e.appendN("a", 1)
	f, _ := os.OpenFile(e.files("queue", "*.jsonl")[0], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{not json\n")
	f.Close()
	e.appendN("b", 1)
	e.fault(fakeingest.FaultServerError, 1)
	if res := e.flush(); !res.Failed {
		t.Fatalf("%+v", res)
	}
	e.clock.advance(time.Minute)
	res := e.flush()
	if res.Sent != 2 || res.DeadLettered != 1 || len(e.recorded()) != 2 {
		t.Fatalf("%+v recorded %d", res, len(e.recorded()))
	}
	if n := len(e.files("deadletter", "*.json")); n != 1 {
		t.Fatalf("%d dead letters", n)
	}
}

// Losing state.json never overwrites or reorders queued segments.
func TestLostStateKeepsSegmentsAndOrder(t *testing.T) {
	e := setup(t)
	e.fault(fakeingest.FaultServerError, 1)
	e.appendN("a", 1)
	e.flush() // seals seg 1, fails
	os.Remove(filepath.Join(e.q.Dir, "state.json"))
	e.appendN("b", 1)
	e.clock.advance(time.Hour)
	if res := e.flush(); res.Sent != 2 {
		t.Fatalf("%+v", res)
	}
	got := e.recorded()
	if got[0]["message_id"] != "a-00000" || got[1]["message_id"] != "b-00000" {
		t.Fatalf("order %v, %v", got[0]["message_id"], got[1]["message_id"])
	}
}

// A hook killed mid-write leaves a partial line; the next event is still
// its own line.
func TestPartialTrailingLineDoesNotSwallowNextEvent(t *testing.T) {
	e := setup(t)
	os.MkdirAll(filepath.Join(e.q.Dir, "queue"), 0o700)
	os.WriteFile(filepath.Join(e.q.Dir, "queue", "active.jsonl"), []byte(`{"event_kind":"sess`), 0o600)
	e.appendN("a", 1)
	res := e.flush()
	if res.Sent != 1 || res.DeadLettered != 1 || len(e.recorded()) != 1 {
		t.Fatalf("%+v", res)
	}
}

type rejectRequest struct{ calls int }

func (r *rejectRequest) Send(context.Context, []json.RawMessage) (contract.IngestResponse, error) {
	r.calls++
	return contract.IngestResponse{}, &ingest.StatusError{Code: http.StatusUnprocessableEntity, Message: "events: maxItems"}
}

// A request-level 422 is a property of the request, not of its events:
// nothing is dead-lettered and the flush backs off.
func TestRequestLevelSchemaRejectionBacksOff(t *testing.T) {
	e := setup(t)
	e.appendN("a", 3)
	s := &rejectRequest{}
	res, err := e.q.Flush(context.Background(), s)
	if err != nil || !res.Failed || res.DeadLettered != 0 || s.calls != 1 {
		t.Fatalf("%+v %v calls=%d", res, err, s.calls)
	}
	if lineCount(t, e.files("queue", "*.jsonl")) != 3 || len(e.files("deadletter", "*")) != 0 {
		t.Fatal("events moved")
	}
}

type fixedResponse struct{ resp contract.IngestResponse }

func (f *fixedResponse) Send(context.Context, []json.RawMessage) (contract.IngestResponse, error) {
	return f.resp, nil
}

// A response whose counts add up but whose rejection indexes are out of range
// or repeated cannot say which events the server rejected, so it is treated
// as malformed: nothing is dead-lettered or advanced, and the flush backs off.
// @spec: event-queue-and-backfill#schema-rejection-dead-letters
// @spec: event-queue-and-backfill#segment-advances-on-success
func TestMalformedRejectionIndexesBackOff(t *testing.T) {
	cases := map[string]contract.IngestResponse{
		"out of range": {Accepted: 2, Rejected: []contract.Rejection{{Index: 5, Reason: "r"}}},
		"negative":     {Accepted: 2, Rejected: []contract.Rejection{{Index: -1, Reason: "r"}}},
		"duplicate":    {Accepted: 1, Rejected: []contract.Rejection{{Index: 1, Reason: "r"}, {Index: 1, Reason: "r"}}},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.appendN("a", 3)
			res, err := e.q.Flush(context.Background(), &fixedResponse{resp: resp})
			if err != nil || !res.Failed || res.Sent != 0 || res.DeadLettered != 0 {
				t.Fatalf("%+v %v", res, err)
			}
			if lineCount(t, e.files("queue", "*.jsonl")) != 3 || len(e.files("deadletter", "*")) != 0 {
				t.Fatal("events moved")
			}
			if !e.q.State().NextAttemptAt.After(e.clock.t) {
				t.Fatal("no backoff recorded")
			}
		})
	}
}
