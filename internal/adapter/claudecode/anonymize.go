package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// Anonymizer turns a real Claude Code transcript into a fixture. It keeps an
// explicit set of fields (ids, usage blocks, timestamps, versions, branches,
// pull-request link numbers, and session totals) and replaces every other value with a
// placeholder, so a field it has never seen is replaced by default.
// Repository names become consistent placeholders.
//
// @spec: test-and-release#fixtures-from-real-transcripts
type Anonymizer struct {
	repos map[string]string
}

// NewAnonymizer returns an anonymizer with an empty repository mapping.
// Reuse one across a session's main and subagent transcripts so their
// placeholders agree.
func NewAnonymizer() *Anonymizer { return &Anonymizer{repos: map[string]string{}} }

const redacted = "[redacted]"

// keepTop are top-level fields whose values are kept as they are.
var keepTop = map[string]bool{
	"type": true, "uuid": true, "parentUuid": true, "logicalParentUuid": true, "leafUuid": true,
	"sessionId": true, "timestamp": true, "version": true, "gitBranch": true, "requestId": true,
	"isSidechain": true, "agentId": true, "entrypoint": true, "userType": true, "isMeta": true,
	"prNumber": true, "subtype": true, "startTime": true,
}

// modelName is the shape of a model name; a modelUsage key of any other
// shape is dropped.
var modelName = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*(\[[a-z0-9]+\])?$`)

// keepMessage are message fields kept as they are. usage is numbers and tier
// names only.
var keepMessage = map[string]bool{
	"id": true, "model": true, "role": true, "type": true, "usage": true, "stop_reason": true,
}

var email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// Anonymize copies r to w line by line. Lines that are not JSON objects are
// dropped.
func (a *Anonymizer) Anonymize(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	bw := bufio.NewWriter(w)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil || m == nil {
			continue
		}
		out := map[string]any{}
		for k, v := range m {
			switch {
			case keepTop[k]:
				out[k] = scrub(v)
			case k == "message":
				out[k] = a.message(v)
			case k == "modelUsage":
				out[k] = modelUsage(v)
			case k == "cwd":
				out[k] = "/anonymized/cwd"
			case k == "prRepository":
				var s string
				json.Unmarshal(v, &s)
				out[k] = a.repo(s)
			case k == "prUrl":
				var s string
				json.Unmarshal(v, &s)
				out[k] = a.prURL(s)
			default:
				out[k] = redacted
			}
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		bw.Write(b)
		bw.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return bw.Flush()
}

func (a *Anonymizer) message(raw json.RawMessage) any {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return redacted
	}
	out := map[string]any{}
	for k, v := range m {
		if keepMessage[k] {
			out[k] = scrub(v)
		} else {
			out[k] = redacted
		}
	}
	return out
}

// modelUsage keeps a cost-state line's per-model numbers: model-name keys,
// and within each, numeric values only.
func modelUsage(raw json.RawMessage) any {
	var m map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return redacted
	}
	out := map[string]map[string]json.Number{}
	for model, counts := range m {
		if !modelName.MatchString(model) {
			continue
		}
		kept := map[string]json.Number{}
		for k, v := range counts {
			var n json.Number
			if json.Unmarshal(v, &n) == nil {
				kept[k] = n
			}
		}
		out[model] = kept
	}
	return out
}

// scrub keeps a JSON value but replaces any email address inside it.
func scrub(raw json.RawMessage) json.RawMessage {
	if !email.Match(raw) {
		return raw
	}
	return email.ReplaceAll(raw, []byte("someone@example.invalid"))
}

func (a *Anonymizer) repo(name string) string {
	key := strings.ToLower(name)
	if p, ok := a.repos[key]; ok {
		return p
	}
	p := fmt.Sprintf("example-org/repo-%d", len(a.repos)+1)
	a.repos[key] = p
	return p
}

func (a *Anonymizer) prURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return redacted
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return redacted
	}
	return fmt.Sprintf("https://%s/%s/pull/%s", publicHost(u.Hostname()), a.repo(parts[0]+"/"+parts[1]), parts[len(parts)-1])
}

// publicHost keeps well-known public git hosts; any other host could name
// an organization and becomes a placeholder.
func publicHost(h string) string {
	switch strings.ToLower(h) {
	case "github.com", "gitlab.com", "bitbucket.org":
		return strings.ToLower(h)
	}
	return "git.example.invalid"
}
