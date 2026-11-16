package allowlist_test

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
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
			checkChildren(t, kind, f.Name, f)
		}
	}
}

// checkChildren compares f's list flag and Children with what the contract
// permits at path, then recurses, so nesting at any depth stays in step with
// the schema.
func checkChildren(t *testing.T, kind, path string, f allowlist.Field) {
	t.Helper()
	if list := contract.IsList(kind, path); f.List != list {
		t.Errorf("%s.%s: allowlist list %v, schema array %v", kind, path, f.List, list)
	}
	want := contract.ObjectFields(kind, path)
	if (len(f.Children) > 0) != (want != nil) {
		t.Errorf("%s.%s: allowlist children %v, schema object fields %v", kind, path, names(f.Children), want)
	}
	if len(f.Children) == 0 {
		return
	}
	if got := names(f.Children); !reflect.DeepEqual(got, want) {
		t.Errorf("%s.%s children:\n allowlist %v\n contract  %v", kind, path, got, want)
	}
	for _, c := range f.Children {
		if c.Description == "" {
			t.Errorf("%s.%s.%s has no description", kind, path, c.Name)
		}
		checkChildren(t, kind, path+"."+c.Name, c)
	}
}

// @spec: field-allowlist#cwd-excluded
// @spec: repo-and-branch-identity#cwd-never-sent
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
// @spec: field-allowlist#git-user-email-excluded
func TestIdentityAndRequestIDPlacement(t *testing.T) {
	for _, kind := range allowlist.Kinds() {
		fields, _ := allowlist.For(kind)
		got := names(fields)
		for _, f := range got {
			if strings.Contains(f, "email") {
				t.Errorf("%s: %s allowed; no event names the engineer", kind, f)
			}
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

// @spec: field-allowlist#side-usage-included
// @spec: field-allowlist#unknown-field-dropped-and-counted
func TestApplyFiltersEachEntryOfAList(t *testing.T) {
	ev := map[string]any{
		"event_kind": "session.closed",
		"side_usage": []any{
			map[string]any{"model": "claude-opus-5-5", "input": 1, "title": "Acme invoice bug"},
			"/Users/alice/acme",
			[]any{"nested"},
			map[string]any{"model": "claude-haiku-5-5", "output": 2, "cost_usd": map[string]any{"x": 1}},
		},
	}
	out, dropped, err := allowlist.Apply(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"model": "claude-opus-5-5", "input": 1},
		map[string]any{"model": "claude-haiku-5-5", "output": 2},
	}
	if !reflect.DeepEqual(out["side_usage"], want) {
		t.Errorf("side_usage %v", out["side_usage"])
	}
	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{"side_usage[]", "side_usage[]", "side_usage[].cost_usd", "side_usage[].title"}) {
		t.Errorf("dropped %v", dropped)
	}
}

// A list-valued field holds only lists: an object or a scalar in its place is
// dropped, and a null passes through.
func TestApplyListFieldShape(t *testing.T) {
	for _, v := range []any{map[string]any{"model": "x"}, "x"} {
		out, dropped, _ := allowlist.Apply(map[string]any{"event_kind": "session.closed", "side_usage": v})
		if _, ok := out["side_usage"]; ok || !reflect.DeepEqual(dropped, []string{"side_usage"}) {
			t.Errorf("%v: out %v dropped %v", v, out, dropped)
		}
	}
	out, dropped, _ := allowlist.Apply(map[string]any{"event_kind": "session.closed", "side_usage": nil})
	if v, ok := out["side_usage"]; !ok || v != nil || len(dropped) != 0 {
		t.Errorf("null: out %v dropped %v", out, dropped)
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

// Go values an adapter might build directly are held to the same rule: only
// JSON scalars survive under a leaf field.
func TestApplyDropsNonScalarGoValuesUnderLeafFields(t *testing.T) {
	out, dropped, _ := allowlist.Apply(map[string]any{
		"event_kind": "session.turn",
		"model":      map[string]string{"cwd": "/Users/alice/acme"},
		"branch":     []string{"main"},
		"request_id": "req_1",
		"message_id": json.Number("1"),
	})
	if _, ok := out["model"]; ok {
		t.Error("model kept")
	}
	if _, ok := out["branch"]; ok {
		t.Error("branch kept")
	}
	if out["request_id"] != "req_1" || out["message_id"] != json.Number("1") {
		t.Errorf("scalars lost: %v", out)
	}
	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{"branch", "model"}) {
		t.Errorf("dropped %v", dropped)
	}
}
