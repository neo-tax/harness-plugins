package allowlist_test

import (
	"os"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/internal/allowlist"
)

const statementPath = "../../PRIVACY.md"

// @spec: field-allowlist#privacy-statement-generated
func TestCommittedStatementMatchesGenerated(t *testing.T) {
	committed, err := os.ReadFile(statementPath)
	if err != nil {
		t.Fatalf("%v (regenerate with: go run ./cmd/privacy-statement > PRIVACY.md)", err)
	}
	if string(committed) != allowlist.Statement() {
		t.Fatal("PRIVACY.md is stale; regenerate with: go run ./cmd/privacy-statement > PRIVACY.md")
	}
}

// @spec: field-allowlist#statement-per-event-kind
func TestStatementListsEveryFieldPerKind(t *testing.T) {
	s := allowlist.Statement()
	for _, kind := range allowlist.Kinds() {
		i := strings.Index(s, "### `"+kind+"`")
		if i < 0 {
			t.Fatalf("no section for %s", kind)
		}
		section := s[i+1:]
		if j := strings.Index(section, "\n### "); j >= 0 {
			section = section[:j]
		}
		fields, _ := allowlist.For(kind)
		for _, f := range fields {
			row := "| `" + f.Name + "` | " + f.Description + " |"
			if !strings.Contains(section, row) {
				t.Errorf("%s: missing row %q", kind, row)
			}
			for _, c := range f.Children {
				row := "| `" + f.Name + "." + c.Name + "` | " + c.Description + " |"
				if !strings.Contains(section, row) {
					t.Errorf("%s: missing row %q", kind, row)
				}
			}
		}
	}
}

// @spec: field-allowlist#statement-names-identity-line
func TestStatementNamesIdentityLine(t *testing.T) {
	s := allowlist.Statement()
	for _, want := range []string{
		"The only personal identity Neo.Tax receives is the email the engineer confirms at enrollment",
		"repository-level git `user.email`",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("statement lacks %q", want)
		}
	}
}
