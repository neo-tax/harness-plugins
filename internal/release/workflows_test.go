package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func workflows(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("../../.github/workflows/*.yml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = string(b)
	}
	return out
}

// A tag can be moved to other code; a commit SHA cannot.
// @spec: test-and-release#actions-pinned-by-sha
func TestActionsPinnedBySHA(t *testing.T) {
	uses := regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*(\S+)`)
	pinned := regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	for name, s := range workflows(t) {
		for _, m := range uses.FindAllStringSubmatch(s, -1) {
			if strings.HasPrefix(m[1], "./") {
				continue
			}
			if !pinned.MatchString(m[1]) {
				t.Errorf("%s: %s is not pinned to a full commit SHA", name, m[1])
			}
		}
	}
}

// Every workflow sets a read-only default for its token, so a job gets write
// access only by asking for it.
// @spec: test-and-release#workflow-token-least-privilege
func TestWorkflowTokenReadOnlyByDefault(t *testing.T) {
	block := regexp.MustCompile(`(?m)^permissions:[ \t]*(.*)\n((?:[ \t]+.*\n|[ \t]*#.*\n|\n)*)`)
	for name, s := range workflows(t) {
		m := block.FindStringSubmatch(s)
		if m == nil {
			t.Errorf("%s: no top-level permissions", name)
			continue
		}
		if strings.Contains(m[1]+m[2], "write") {
			t.Errorf("%s: top-level permissions grant write:\n%s", name, m[0])
		}
	}
}
