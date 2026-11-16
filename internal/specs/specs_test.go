// Package specs checks the architectural specs against the code: statuses
// are honest, @spec tags resolve, and affects globs match files.
package specs

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../.."

type requirement struct {
	spec, slug, status string
	reason             bool
}

var (
	reqLine = regexp.MustCompile("^- `([a-z0-9-]+)`\\((draft|approved|built|tested)\\):")
	tagLine = regexp.MustCompile(`@spec: ([a-z0-9-]+)#([a-z0-9-]+)`)
)

func loadSpecs(t *testing.T) (map[string]requirement, map[string][]string) {
	t.Helper()
	reqs := map[string]requirement{}
	affects := map[string][]string{}
	paths, _ := filepath.Glob(filepath.Join(root, "docs/specs/architectural/*.md"))
	if len(paths) == 0 {
		t.Fatal("no specs")
	}
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".md")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		inAffects := false
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "affects:"):
				inAffects = true
				continue
			case inAffects && strings.HasPrefix(l, "  - "):
				affects[id] = append(affects[id], strings.TrimPrefix(l, "  - "))
				continue
			default:
				inAffects = false
			}
			if m := reqLine.FindStringSubmatch(l); m != nil {
				r := requirement{spec: id, slug: m[1], status: m[2]}
				r.reason = i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  - status-reason: ")
				reqs[id+"#"+m[1]] = r
			}
		}
	}
	return reqs, affects
}

// tags maps "spec#slug" to the files tagging it, split by test (Go tests and
// scripts/*-test.sh, which CI runs) and non-test.
func tags(t *testing.T) (tests, code map[string][]string) {
	tests, code = map[string][]string{}, map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata" || d.Name() == "docs") {
			return filepath.SkipDir
		}
		if d.IsDir() || !(strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".yml")) {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			for _, m := range tagLine.FindAllStringSubmatch(sc.Text(), -1) {
				key := m[1] + "#" + m[2]
				if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, "-test.sh") {
					tests[key] = append(tests[key], p)
				} else {
					code[key] = append(code[key], p)
				}
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return tests, code
}

// A requirement marked tested has a test tagged with it, and one marked
// built says why no test covers it.
func TestStatusesAreHonest(t *testing.T) {
	reqs, _ := loadSpecs(t)
	tests, _ := tags(t)
	for key, r := range reqs {
		switch r.status {
		case "tested":
			if len(tests[key]) == 0 {
				t.Errorf("%s is tested but no test carries @spec: %s", key, key)
			}
		case "built":
			if !r.reason {
				t.Errorf("%s is built without a status-reason", key)
			}
		}
	}
}

// Every @spec tag names a requirement that exists.
func TestTagsResolve(t *testing.T) {
	reqs, _ := loadSpecs(t)
	tests, code := tags(t)
	for _, m := range []map[string][]string{tests, code} {
		for key, files := range m {
			if _, ok := reqs[key]; !ok {
				t.Errorf("@spec: %s in %v names no requirement", key, files)
			}
		}
	}
}

// Every affects glob matches at least one file.
func TestAffectsGlobsMatch(t *testing.T) {
	_, affects := loadSpecs(t)
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	for spec, globs := range affects {
		for _, g := range globs {
			re := globRegexp(g)
			found := false
			for _, f := range files {
				if re.MatchString(f) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: affects %q matches no file", spec, g)
			}
		}
	}
}

// For the subsystems test-and-release names, every requirement is either
// covered by a unit test or says why it cannot be.
// @spec: test-and-release#unit-coverage
func TestUnitCoverage(t *testing.T) {
	reqs, _ := loadSpecs(t)
	for _, spec := range []string{"claude-code-transcript-parser", "repo-and-branch-identity", "event-queue-and-backfill", "field-allowlist"} {
		for key, r := range reqs {
			if r.spec != spec {
				continue
			}
			if r.status != "tested" && !(r.status == "built" && r.reason) {
				t.Errorf("%s is %s", key, r.status)
			}
		}
	}
}

func globRegexp(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case g[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
