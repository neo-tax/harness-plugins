// Package usageaudit checks the agent's token counts against the API's own.
// A recording proxy, set as Claude Code's ANTHROPIC_BASE_URL, logs the usage
// the API returned for every request; Compare matches those requests to the
// session.turn events the agent emitted, by request id, and reports what the
// events miss or count differently.
//
// The proxy never reads request bodies and keeps only ids, the model, and
// token counts from responses, so the log holds no prompt or completion text.
package usageaudit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Usage is one request's token counts in the contract's terms.
type Usage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// Total is every token the request was billed for, across tiers.
func (u Usage) Total() int64 {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h
}

func (u *Usage) add(v Usage) {
	u.Input += v.Input
	u.Output += v.Output
	u.CacheRead += v.CacheRead
	u.CacheWrite5m += v.CacheWrite5m
	u.CacheWrite1h += v.CacheWrite1h
}

// Record is one proxied request.
type Record struct {
	Time      time.Time `json:"time"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	RequestID string    `json:"request_id"`
	MessageID string    `json:"message_id,omitempty"`
	Model     string    `json:"model,omitempty"`
	Usage     Usage     `json:"usage"`
}

// billable reports whether the API charged tokens for the request.
func (r Record) billable() bool {
	return r.Status == http.StatusOK && r.Path == "/v1/messages" && r.Usage.Total() > 0
}

// apiUsage is the API's usage block. Fields are pointers so a message_delta
// overrides only the counts it carries.
type apiUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (a *apiUsage) merge(b *apiUsage) {
	if b == nil {
		return
	}
	for _, f := range []struct{ dst, src **int64 }{
		{&a.InputTokens, &b.InputTokens}, {&a.OutputTokens, &b.OutputTokens},
		{&a.CacheCreationInputTokens, &b.CacheCreationInputTokens}, {&a.CacheReadInputTokens, &b.CacheReadInputTokens},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
	if b.CacheCreation != nil {
		a.CacheCreation = b.CacheCreation
	}
}

// usage maps the API's counts the way the Claude Code adapter maps a
// transcript's: without the per-TTL split, cache writes are five-minute.
func (a apiUsage) usage() Usage {
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	u := Usage{Input: v(a.InputTokens), Output: v(a.OutputTokens), CacheRead: v(a.CacheReadInputTokens)}
	if a.CacheCreation != nil {
		u.CacheWrite5m, u.CacheWrite1h = v(a.CacheCreation.Ephemeral5m), v(a.CacheCreation.Ephemeral1h)
	} else {
		u.CacheWrite5m = v(a.CacheCreationInputTokens)
	}
	return u
}

// message is the subset of a response, or of a message_start event's
// message, that the proxy decodes. Content has no field here.
type message struct {
	ID    string    `json:"id"`
	Model string    `json:"model"`
	Usage *apiUsage `json:"usage"`
}

type streamEvent struct {
	Type    string    `json:"type"`
	Message *message  `json:"message"`
	Usage   *apiUsage `json:"usage"`
}

// Proxy forwards requests to the API and logs one Record per response.
type Proxy struct {
	rp  *httputil.ReverseProxy
	mu  sync.Mutex
	out io.Writer
	wg  sync.WaitGroup
}

// NewProxy returns a proxy to upstream that writes Records to out as JSON
// lines.
func NewProxy(upstream string, out io.Writer) (*Proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	p := &Proxy{out: out}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			// Let the transport negotiate and undo compression, so the
			// response is readable here and plain for the client.
			r.Out.Header.Del("Accept-Encoding")
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			rec := Record{Time: time.Now().UTC(), Path: resp.Request.URL.Path, Status: resp.StatusCode, RequestID: resp.Header.Get("request-id")}
			mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			resp.Body = &tap{ReadCloser: resp.Body, sse: mt == "text/event-stream", done: func(m message, u apiUsage) {
				rec.MessageID, rec.Model, rec.Usage = m.ID, m.Model, u.usage()
				p.write(rec)
			}}
			return nil
		},
	}
	return p, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.wg.Add(1)
	defer p.wg.Done()
	p.rp.ServeHTTP(w, r)
}

// Close waits for in-flight requests to be logged.
func (p *Proxy) Close() { p.wg.Wait() }

func (p *Proxy) write(r Record) {
	b, _ := json.Marshal(r)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.out.Write(append(b, '\n'))
}

// tap passes a response body through unchanged while decoding its ids and
// usage, and reports them once when the body is closed.
type tap struct {
	io.ReadCloser
	sse  bool
	buf  bytes.Buffer
	msg  message
	use  apiUsage
	once sync.Once
	done func(message, apiUsage)
}

func (t *tap) Read(b []byte) (int, error) {
	n, err := t.ReadCloser.Read(b)
	t.buf.Write(b[:n])
	if t.sse {
		t.scanLines()
	}
	return n, err
}

// scanLines decodes each complete "data:" line and drops it from the buffer.
func (t *tap) scanLines() {
	for {
		i := bytes.IndexByte(t.buf.Bytes(), '\n')
		if i < 0 {
			return
		}
		line := t.buf.Next(i + 1)
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok {
			continue
		}
		var ev streamEvent
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				t.msg.ID, t.msg.Model = ev.Message.ID, ev.Message.Model
				t.use.merge(ev.Message.Usage)
			}
		case "message_delta":
			t.use.merge(ev.Usage)
		}
	}
}

func (t *tap) Close() error {
	err := t.ReadCloser.Close()
	t.once.Do(func() {
		if !t.sse {
			var m message
			if json.Unmarshal(t.buf.Bytes(), &m) == nil {
				t.msg.ID, t.msg.Model = m.ID, m.Model
				t.use.merge(m.Usage)
			}
		}
		t.buf.Reset()
		t.done(t.msg, t.use)
	})
	return err
}

// ReadRecords reads a proxy log.
func ReadRecords(r io.Reader) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// Mismatch is a request whose event counts differ from the API's.
type Mismatch struct {
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
	API       Usage  `json:"api"`
	Event     Usage  `json:"event"`
}

// Report is the result of Compare.
type Report struct {
	// Matched is the number of billable requests with a session.turn.
	Matched int `json:"matched"`
	// Missing are billable requests no session.turn reported.
	Missing []Record `json:"missing"`
	// Mismatched are matched requests whose counts differ.
	Mismatched []Mismatch `json:"mismatched"`
	// Unexplained are session.turn request ids the proxy never saw, such as
	// turns from sessions that did not run through it.
	Unexplained []string `json:"unexplained"`
	// API totals every billable request; Events totals the matched turns.
	API    Usage `json:"api"`
	Events Usage `json:"events"`
	// MissingByModel totals the missing requests per model.
	MissingByModel map[string]Usage `json:"missing_by_model"`
}

type turnEvent struct {
	EventKind string `json:"event_kind"`
	RequestID string `json:"request_id"`
	Usage     struct {
		Input        *int64 `json:"input"`
		Output       *int64 `json:"output"`
		CacheRead    *int64 `json:"cache_read"`
		CacheWrite5m *int64 `json:"cache_write_5m"`
		CacheWrite1h *int64 `json:"cache_write_1h"`
	} `json:"usage"`
}

func (e turnEvent) usage() Usage {
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	u := e.Usage
	return Usage{Input: v(u.Input), Output: v(u.Output), CacheRead: v(u.CacheRead), CacheWrite5m: v(u.CacheWrite5m), CacheWrite1h: v(u.CacheWrite1h)}
}

// Compare matches proxied requests to session.turn events by request id.
// Requests that billed nothing (errors, token counting) are ignored.
func Compare(records []Record, events []json.RawMessage) (Report, error) {
	turns := map[string]Usage{}
	var order []string
	for _, raw := range events {
		var e turnEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return Report{}, err
		}
		if e.EventKind != "session.turn" || e.RequestID == "" {
			continue
		}
		if _, dup := turns[e.RequestID]; !dup {
			order = append(order, e.RequestID)
		}
		turns[e.RequestID] = e.usage()
	}
	rep := Report{Missing: []Record{}, Mismatched: []Mismatch{}, Unexplained: []string{}, MissingByModel: map[string]Usage{}}
	seen := map[string]bool{}
	for _, r := range records {
		if !r.billable() {
			continue
		}
		seen[r.RequestID] = true
		rep.API.add(r.Usage)
		ev, ok := turns[r.RequestID]
		if !ok {
			rep.Missing = append(rep.Missing, r)
			m := rep.MissingByModel[r.Model]
			m.add(r.Usage)
			rep.MissingByModel[r.Model] = m
			continue
		}
		rep.Matched++
		rep.Events.add(ev)
		if ev != r.Usage {
			rep.Mismatched = append(rep.Mismatched, Mismatch{RequestID: r.RequestID, Model: r.Model, API: r.Usage, Event: ev})
		}
	}
	for _, id := range order {
		if !seen[id] {
			rep.Unexplained = append(rep.Unexplained, id)
		}
	}
	sort.Strings(rep.Unexplained)
	return rep, nil
}
