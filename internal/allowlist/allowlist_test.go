package allowlist_test

import (
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/allowlist"
)

func names(fields []allowlist.Field) []string {
	out := []string{}
	for _, f := range fields {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// @spec: field-allowlist#one-allowlist
// @spec: field-allowlist#allowlist-matches-contract
func TestAllowlistMatchesContract(t *testing.T) {
	schema := contract.SchemaFields()
	if got, want := len(allowlist.Kinds()), len(schema); got != want {
		t.Fatalf("allowlist has %d kinds, contract %d", got, want)
	}
	for kind, want := range schema {
		fields, ok := allowlist.For(kind)
		if !ok {
			t.Fatalf("no allowlist entry for %s", kind)
		}
		if got := names(fields); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n allowlist %v\n contract  %v", kind, got, want)
		}
		for _, f := range fields {
			if f.Description == "" {
				t.Errorf("%s.%s has no description", kind, f.Name)
			}
			if (len(f.Children) > 0) != (contract.ObjectFields(f.Name) != nil) {
				t.Errorf("%s.%s: allowlist children %v, schema object fields %v", kind, f.Name, names(f.Children), contract.ObjectFields(f.Name))
			}
			if len(f.Children) > 0 {
				if got, want := names(f.Children), contract.ObjectFields(f.Name); !reflect.DeepEqual(got, want) {
					t.Errorf("%s.%s children:\n allowlist %v\n contract  %v", kind, f.Name, got, want)
				}
				for _, c := range f.Children {
					if c.Description == "" {
						t.Errorf("%s.%s.%s has no description", kind, f.Name, c.Name)
					}
				}
			}
		}
	}
}

// @spec: field-allowlist#cwd-excluded
// @spec: field-allowlist#title-excluded
// @spec: field-allowlist#content-excluded
func TestNoContentBearingFieldIsAllowed(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)cwd|dir|path|title|slug|summary|prompt|content|text|completion|message$|tool|input_|output_|file`)
	for _, kind := range allowlist.Kinds() {
		fields, _ := allowlist.For(kind)
		var walk func(prefix string, fs []allowlist.Field)
		walk = func(prefix string, fs []allowlist.Field) {
			for _, f := range fs {
				if forbidden.MatchString(f.Name) {
					t.Errorf("%s: %s%s looks content-bearing", kind, prefix, f.Name)
				}
				walk(prefix+f.Name+".", f.Children)
			}
		}
		walk("", fields)
	}
}

// @spec: field-allowlist#request-id-included
// @spec: field-allowlist#git-user-email-included
func TestIdentityAndRequestIDPlacement(t *testing.T) {
	for _, kind := range allowlist.Kinds() {
		fields, _ := allowlist.For(kind)
		got := names(fields)
		hasEmail := contains(got, "git_user_email")
		if hasEmail != (kind == contract.KindOpened) {
			t.Errorf("%s: git_user_email allowed=%v", kind, hasEmail)
		}
		if kind == contract.KindTurn && !contains(got, "request_id") {
			t.Errorf("request_id not allowed on %s", kind)
		}
	}
}

// @spec: field-allowlist#unknown-field-dropped-and-counted
func TestApplyDropsUnknownFieldsAndReportsThem(t *testing.T) {
	ev := map[string]any{
		"event_kind": "session.turn", "message_id": "m", "cwd": "/Users/someone/acme",
		"usage": map[string]any{"input": 1, "source": "transcript", "prompt": "secret"},
	}
	out, dropped, err := allowlist.Apply(ev)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["cwd"]; ok {
		t.Error("cwd kept")
	}
	if _, ok := out["usage"].(map[string]any)["prompt"]; ok {
		t.Error("usage.prompt kept")
	}
	if out["message_id"] != "m" || out["usage"].(map[string]any)["input"] != 1 {
		t.Errorf("allowed fields lost: %v", out)
	}
	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{"cwd", "usage.prompt"}) {
		t.Errorf("dropped %v", dropped)
	}
	if _, ok := ev["cwd"]; !ok {
		t.Error("Apply mutated its input")
	}
}

// A null object-valued field (repo outside git) passes through.
func TestApplyKeepsNullNestedObjects(t *testing.T) {
	out, dropped, err := allowlist.Apply(map[string]any{"event_kind": "session.opened", "repo": nil})
	if err != nil || len(dropped) != 0 {
		t.Fatal(err, dropped)
	}
	if v, ok := out["repo"]; !ok || v != nil {
		t.Errorf("repo %v", out)
	}
}

func TestApplyRejectsUnknownKind(t *testing.T) {
	if _, _, err := allowlist.Apply(map[string]any{"event_kind": "session.paused"}); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := allowlist.Apply(map[string]any{}); err == nil {
		t.Fatal("expected error")
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// A value whose shape the allowlist does not describe never passes through:
// an object or array under a leaf field, or a non-object under an object
// field, is dropped and reported.
// @spec: field-allowlist#unknown-field-dropped-and-counted
func TestApplyDropsValuesOfTheWrongShape(t *testing.T) {
	ev := map[string]any{
		"event_kind": "session.turn",
		"model":      map[string]any{"name": "x", "cwd": "/Users/alice/acme"},
		"branch":     []any{"main", "/Users/alice"},
		"usage":      []any{map[string]any{"path": "/x"}},
	}
	out, dropped, _ := allowlist.Apply(ev)
	for _, k := range []string{"model", "branch", "usage"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s kept: %v", k, out[k])
		}
	}
	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{"branch", "model", "usage"}) {
		t.Errorf("dropped %v", dropped)
	}
}

// Dropped names are reported for local diagnostics, so a key that is not a
// plain identifier (a file path used as a map key, say) is never reported
// verbatim.
func TestDroppedNamesNeverCarryContent(t *testing.T) {
	_, dropped, _ := allowlist.Apply(map[string]any{
		"event_kind": "session.closed", "/Users/alice/acme/secret.go": 1, "extra_field": 2,
	})
	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{allowlist.RedactedName, "extra_field"}) {
		t.Errorf("dropped %v", dropped)
	}
}
