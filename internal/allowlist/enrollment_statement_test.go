package allowlist_test

import (
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/allowlist"
)

// schemaPaths returns the dotted path of every property under props, at any
// depth, read straight from the schema rather than from the Go types.
func schemaPaths(prefix string, props map[string]schemaNode) []string {
	var out []string
	for name, n := range props {
		out = append(out, prefix+name)
		out = append(out, schemaPaths(prefix+name+".", n.Properties)...)
	}
	sort.Strings(out)
	return out
}

type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
}

// Every field the enrollment request schema permits, at any depth, has a row
// in the statement's enrollment section, and the section says when it is sent.
//
// @spec: field-allowlist#statement-covers-enrollment
func TestStatementListsEveryEnrollmentRequestField(t *testing.T) {
	b, err := fs.ReadFile(contract.Schemas(), contract.EnrollmentSchema)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]schemaNode `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	paths := schemaPaths("", doc.Defs["request"].Properties)
	if len(paths) == 0 {
		t.Fatal("no enrollment request fields in schema")
	}

	s := allowlist.Statement()
	i := strings.Index(s, "\n## At enrollment\n")
	if i < 0 {
		t.Fatal("statement has no At enrollment section")
	}
	section := s[i:]
	if j := strings.Index(section[1:], "\n## "); j >= 0 {
		section = section[:j+1]
	}
	if !strings.Contains(section, "once per device") {
		t.Error("enrollment section does not say when the request is sent")
	}
	for _, p := range paths {
		if !strings.Contains(section, "| `"+p+"` | ") {
			t.Errorf("enrollment section lacks a row for %s", p)
		}
	}
	if got, want := len(contract.EnrollmentRequestFields()), len(paths); got != want {
		t.Errorf("Go type yields %d enrollment fields, schema %d", got, want)
	}
}
