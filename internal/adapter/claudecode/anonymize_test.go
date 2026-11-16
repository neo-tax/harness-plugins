package claudecode_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/adapter/claudecode"
	"github.com/neo-tax/harness-plugins/internal/cursor"
)

// @spec: test-and-release#fixtures-from-real-transcripts
func TestAnonymizerStripsContentKeepsStructure(t *testing.T) {
	src, _ := os.ReadFile("testdata/fixtures/synthetic-2.1.310/fixture-session.jsonl")
	src = append(src, []byte(`{"type":"user","sessionId":"fixture-session","newField":"someone@example.com wrote this","message":{"role":"user","content":"x"}}`+"\n")...)
	var out bytes.Buffer
	a := claudecode.NewAnonymizer()
	if err := a.Anonymize(bytes.NewReader(src), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, secret := range []string{"Acme", "acme", "secret reasoning", "id_rsa", "PRIVATE KEY", "Done", "/Users/someone", "Neo-Tax", "Harness-Plugins", "someone@example.com", "wrote this"} {
		if strings.Contains(got, secret) {
			t.Errorf("anonymized output contains %q", secret)
		}
	}
	for _, keep := range []string{"msg_A", "req_A", "feature/X", "2026-10-25T10:00:05.000Z", `"ephemeral_1h_input_tokens":200`, `"prNumber":42`, "fixture-session", "claude-opus-5-5", "2.1.311", `"newField"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("anonymized output lost %q", keep)
		}
	}
}

// The anonymized transcript parses to the same event stream as the original,
// except that repository names are placeholders.
func TestAnonymizedTranscriptParsesTheSame(t *testing.T) {
	main, sid := copyFixture(t, "testdata/fixtures/synthetic-2.1.310")
	origR, _ := claudecode.TailEvents(fixtureContext(sid), main, cursor.Open(t.TempDir()))
	orig := origR.Events

	src, _ := os.ReadFile(main)
	var out bytes.Buffer
	claudecode.NewAnonymizer().Anonymize(bytes.NewReader(src), &out)
	os.WriteFile(main, out.Bytes(), 0o644)
	anonR, _ := claudecode.TailEvents(fixtureContext(sid), main, cursor.Open(t.TempDir()))
	anon := anonR.Events

	o, a := toLines(t, orig), toLines(t, anon)
	if len(o) != len(a) {
		t.Fatalf("%d vs %d events", len(o), len(a))
	}
	for i := range o {
		delete(o[i], "repo")
		delete(a[i], "repo")
		if !reflect.DeepEqual(o[i], a[i]) {
			ob, _ := json.Marshal(o[i])
			ab, _ := json.Marshal(a[i])
			t.Errorf("event %d:\n orig %s\n anon %s", i, ob, ab)
		}
	}
}

func TestRepositoryPlaceholdersAreConsistent(t *testing.T) {
	in := `{"type":"pr-link","prNumber":1,"prRepository":"acme/secret","prUrl":"https://github.com/acme/secret/pull/1"}
{"type":"pr-link","prNumber":2,"prRepository":"acme/secret","prUrl":"https://github.com/acme/secret/pull/2"}
{"type":"pr-link","prNumber":3,"prRepository":"acme/other","prUrl":"https://github.com/acme/other/pull/3"}
`
	var out bytes.Buffer
	claudecode.NewAnonymizer().Anonymize(strings.NewReader(in), &out)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	type pr struct{ Repo, URL string }
	parse := func(s string) pr {
		var m map[string]any
		json.Unmarshal([]byte(s), &m)
		return pr{m["prRepository"].(string), m["prUrl"].(string)}
	}
	a, b, c := parse(lines[0]), parse(lines[1]), parse(lines[2])
	if a.Repo != b.Repo || a.Repo == c.Repo {
		t.Fatalf("placeholders %q %q %q", a.Repo, b.Repo, c.Repo)
	}
	for _, p := range []pr{a, b, c} {
		if !strings.HasPrefix(p.URL, "https://github.com/"+p.Repo+"/pull/") {
			t.Errorf("url %q does not match repo %q", p.URL, p.Repo)
		}
	}
}

func TestPrivateGitHostsArePlaceholders(t *testing.T) {
	in := `{"type":"pr-link","prNumber":1,"prRepository":"team/app","prUrl":"https://github.acme-corp.com/team/app/pull/1"}` + "\n"
	var out bytes.Buffer
	claudecode.NewAnonymizer().Anonymize(strings.NewReader(in), &out)
	if strings.Contains(out.String(), "acme") || !strings.Contains(out.String(), "git.example.invalid") {
		t.Fatalf("%s", out.String())
	}
}
