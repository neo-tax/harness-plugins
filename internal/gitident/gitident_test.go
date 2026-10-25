package gitident_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/neo-tax/harness-plugins/contract"
	"github.com/neo-tax/harness-plugins/internal/gitident"
)

// @spec: repo-and-branch-identity#repo-from-remote-url
// @spec: repo-and-branch-identity#repo-lowercased
// @spec: repo-and-branch-identity#host-preserved
func TestParseRemoteURL(t *testing.T) {
	gh := &contract.Repo{Host: "github.com", Owner: "neo-tax", Name: "harness-plugins"}
	cases := map[string]*contract.Repo{
		"git@github.com:neo-tax/harness-plugins.git":                           gh,
		"git@github.com:Neo-Tax/Harness-Plugins.git":                           gh,
		"git@github.com:neo-tax/harness-plugins":                               gh,
		"ssh://git@github.com/neo-tax/harness-plugins.git":                     gh,
		"ssh://git@github.com:22/neo-tax/harness-plugins.git":                  gh,
		"https://github.com/neo-tax/harness-plugins.git":                       gh,
		"https://github.com/neo-tax/harness-plugins":                           gh,
		"https://github.com/neo-tax/harness-plugins/":                          gh,
		"https://x-access-token:secret@github.com/neo-tax/harness-plugins.git": gh,
		"http://GitHub.com/neo-tax/harness-plugins.git":                        gh,
		"git://github.com/neo-tax/harness-plugins.git":                         gh,
		"https://gitlab.example.com/Group/Sub/Project.git":                     {Host: "gitlab.example.com", Owner: "group/sub", Name: "project"},
		"git@bitbucket.org:team/repo.git":                                      {Host: "bitbucket.org", Owner: "team", Name: "repo"},
		"https://dev.azure.com/org/project/_git/repo":                          {Host: "dev.azure.com", Owner: "org/project/_git", Name: "repo"},
		"/Users/someone/src/repo.git":                                          nil,
		"file:///Users/someone/src/repo.git":                                   nil,
		`C:\Users\someone\repo`:                                                nil,
		"C:/Users/someone/repo":                                                nil,
		"../sibling":                                                           nil,
		"gh:neo-tax/harness-plugins":                                           nil, // an insteadOf alias, not a host
		"https://github.com/onlyowner":                                         nil,
		"":                                                                     nil,
	}
	for url, want := range cases {
		got := gitident.ParseRemoteURL(url)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%q: got %+v, want %+v", url, got, want)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "Feature/Mixed-Case")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// @spec: repo-and-branch-identity#branch-exact
// @spec: repo-and-branch-identity#head-sha
func TestIdentifyRepository(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "remote", "add", "origin", "git@github.com:Neo-Tax/Harness-Plugins.git")
	id := gitident.Identify(dir)
	if id.Repo == nil || *id.Repo != (contract.Repo{Host: "github.com", Owner: "neo-tax", Name: "harness-plugins"}) {
		t.Errorf("repo %+v", id.Repo)
	}
	if str(id.Branch) != "Feature/Mixed-Case" {
		t.Errorf("branch %s", str(id.Branch))
	}
	if want := git(t, dir, "rev-parse", "HEAD"); str(id.HeadSHA) != want || !sha40.MatchString(want) {
		t.Errorf("head %s, want %s", str(id.HeadSHA), want)
	}
}

// @spec: repo-and-branch-identity#remote-precedence
func TestRemotePrecedence(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "remote", "add", "aaa", "git@github.com:first/repo.git")
	git(t, dir, "remote", "add", "upstream", "git@github.com:upstream/repo.git")
	if got := gitident.Identify(dir).Repo; got == nil || got.Owner != "first" {
		t.Errorf("no origin, no upstream: %+v", got)
	}
	git(t, dir, "config", "branch.Feature/Mixed-Case.remote", "upstream")
	if got := gitident.Identify(dir).Repo; got == nil || got.Owner != "upstream" {
		t.Errorf("upstream: %+v", got)
	}
	git(t, dir, "remote", "add", "origin", "git@github.com:origin/repo.git")
	if got := gitident.Identify(dir).Repo; got == nil || got.Owner != "origin" {
		t.Errorf("origin: %+v", got)
	}
}

func TestNoRemoteHasNullRepoButBranch(t *testing.T) {
	dir := newRepo(t)
	id := gitident.Identify(dir)
	if id.Repo != nil || id.Branch == nil || id.HeadSHA == nil {
		t.Errorf("%+v", id)
	}
}

// @spec: repo-and-branch-identity#branch-exact
func TestDetachedHeadHasNullBranch(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "checkout", "-q", "--detach")
	id := gitident.Identify(dir)
	if id.Branch != nil || id.HeadSHA == nil {
		t.Errorf("branch %s head %s", str(id.Branch), str(id.HeadSHA))
	}
}

// @spec: repo-and-branch-identity#repo-null-outside-git
func TestOutsideGitIsNull(t *testing.T) {
	id := gitident.Identify(t.TempDir())
	if id.Repo != nil || id.Branch != nil || id.HeadSHA != nil {
		t.Errorf("%+v", id)
	}
}

// @spec: repo-and-branch-identity#worktree-resolves-to-main-repo
func TestLinkedWorktreeResolvesToMainRepo(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "remote", "add", "origin", "https://github.com/neo-tax/harness-plugins.git")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-q", "-b", "wt-branch", wt)
	id := gitident.Identify(filepath.Join(wt))
	if id.Repo == nil || id.Repo.Name != "harness-plugins" || str(id.Branch) != "wt-branch" {
		t.Errorf("%+v branch %s", id.Repo, str(id.Branch))
	}
}

func TestSubdirectoryResolves(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "remote", "add", "origin", "https://github.com/neo-tax/harness-plugins.git")
	sub := filepath.Join(dir, "a", "b")
	os.MkdirAll(sub, 0o755)
	if id := gitident.Identify(sub); id.Repo == nil {
		t.Error("no repo from subdirectory")
	}
}

// fakeGit installs a git wrapper that logs each invocation, or one that
// always fails, and returns the log path.
func fakeGit(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	real, _ := exec.LookPath("git")
	body := strings.NewReplacer("$LOG", log, "$REAL", real).Replace(script)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := gitident.GitCommand
	gitident.GitCommand = filepath.Join(bin, "git")
	t.Cleanup(func() { gitident.GitCommand = old })
	return log
}

// @spec: repo-and-branch-identity#single-git-invocation
func TestSingleGitInvocation(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "remote", "add", "origin", "https://github.com/neo-tax/harness-plugins.git")
	log := fakeGit(t, `echo x >> "$LOG"; exec "$REAL" "$@"`)
	if id := gitident.Identify(dir); id.Repo == nil || id.Branch == nil || id.HeadSHA == nil {
		t.Fatalf("%+v", id)
	}
	calls, _ := os.ReadFile(log)
	if n := strings.Count(string(calls), "x"); n != 1 {
		t.Fatalf("%d git invocations", n)
	}
}

// @spec: repo-and-branch-identity#git-failure-is-null
func TestGitFailureIsNull(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "remote", "add", "origin", "https://github.com/neo-tax/harness-plugins.git")
	fakeGit(t, `exit 1`)
	if id := gitident.Identify(dir); id.Repo != nil || id.Branch != nil || id.HeadSHA != nil {
		t.Fatalf("%+v", id)
	}
	gitident.GitCommand = filepath.Join(t.TempDir(), "no-such-git")
	if id := gitident.Identify(dir); id.Repo != nil || id.Branch != nil || id.HeadSHA != nil {
		t.Fatalf("%+v", id)
	}
}

// A repository with no commits yet still has a branch and a remote.
// @spec: repo-and-branch-identity#repo-null-outside-git
// @spec: repo-and-branch-identity#branch-exact
func TestUnbornBranchKeepsRepoAndBranch(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "First")
	git(t, dir, "remote", "add", "origin", "git@github.com:neo-tax/empty.git")
	id := gitident.Identify(dir)
	if id.Repo == nil || id.Repo.Name != "empty" || str(id.Branch) != "First" || id.HeadSHA != nil {
		t.Fatalf("%+v branch %s head %s", id.Repo, str(id.Branch), str(id.HeadSHA))
	}
}
