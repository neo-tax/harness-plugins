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
		for _, row := range missingRows(section, "", fields) {
			t.Errorf("%s: missing row %q", kind, row)
		}
	}
}

// missingRows returns the row for every field in fields, at any depth, that
// section lacks. Each row names the field by its full dotted path.
func missingRows(section, prefix string, fields []allowlist.Field) []string {
	var out []string
	for _, f := range fields {
		row := "| `" + prefix + f.Name + "` | " + f.Description + " |"
		if !strings.Contains(section, row) {
			out = append(out, row)
		}
		out = append(out, missingRows(section, prefix+f.Name+".", f.Children)...)
	}
	return out
}

// A field nested two levels deep is still let through by Apply, so the
// statement must list it under its full dotted path, and the per-kind check
// must notice when it does not.
//
// @spec: field-allowlist#statement-per-event-kind
func TestStatementListsNestedFieldsAtAnyDepth(t *testing.T) {
	fields := []allowlist.Field{
		{Name: "usage", Description: "Token counts.", Children: []allowlist.Field{
			{Name: "input_tokens", Description: "Input tokens."},
			{Name: "cache", Description: "Cache counts.", Children: []allowlist.Field{
				{Name: "read_tokens", Description: "Tokens read from cache."},
			}},
		}},
	}
	rows := allowlist.Rows(fields)
	want := "| `usage.cache.read_tokens` | Tokens read from cache. |"
	if !strings.Contains(rows, want) {
		t.Errorf("rows lack %q:\n%s", want, rows)
	}
	if missing := missingRows(rows, "", fields); len(missing) > 0 {
		t.Errorf("missing rows %v", missing)
	}

	oneLevel := "| `usage` | Token counts. |\n| `usage.input_tokens` | Input tokens. |\n| `usage.cache` | Cache counts. |\n"
	if missing := missingRows(oneLevel, "", fields); len(missing) != 1 || missing[0] != want {
		t.Errorf("one-level rendering: missing %v, want only %q", missing, want)
	}
}

// @spec: field-allowlist#statement-names-identity-line
// @spec: field-allowlist#statement-names-incidental-identity
func TestStatementNamesIdentityLine(t *testing.T) {
	s := allowlist.Statement()
	for _, want := range []string{
		"Neo.Tax receives two values meant to identify the engineer: the email the engineer confirms at enrollment",
		"repository-level git `user.email`",
		"`branch` is free text the engineer or their tools choose",
		"`repo.owner` is the account that owns the repository, which is a personal username",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("statement lacks %q", want)
		}
	}
}
