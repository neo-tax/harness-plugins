// Package gitident derives the repo, branch, and head_sha fields of a session
// event from the working directory a harness reports. The working-directory
// path itself never leaves this package.
//
// @spec: repo-and-branch-identity#cwd-never-sent
package gitident

import (
	"bufio"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/neo-tax/harness-plugins/contract"
)

// GitCommand is the git executable Identify runs.
var GitCommand = "git"

// Timeout bounds the single git invocation.
var Timeout = 5 * time.Second

// Identity is what the agent knows about the repository a session runs in.
// Every field is nil when the directory is not in a git repository or git
// fails.
type Identity struct {
	Repo    *contract.Repo
	Branch  *string
	HeadSHA *string
}

// Identify returns the repository identity of dir with exactly one git
// process: rev-parse reports HEAD, the shared git directory, and the symbolic
// ref; remotes are read from the shared config file directly, so a linked
// worktree resolves to its main repository.
//
// @spec: repo-and-branch-identity#single-git-invocation
// @spec: repo-and-branch-identity#git-failure-is-null
// @spec: repo-and-branch-identity#repo-null-outside-git
// @spec: repo-and-branch-identity#worktree-resolves-to-main-repo
func Identify(dir string) Identity {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	// Output order: git dir, common dir, HEAD's sha, HEAD's symbolic ref. On
	// an unborn branch git prints the two directories (and, depending on the
	// version, an unresolved "HEAD") and exits non-zero; the branch is then
	// read from the HEAD file itself.
	cmd := exec.CommandContext(ctx, GitCommand, "-C", dir, "rev-parse", "--path-format=absolute",
		"--git-dir", "--git-common-dir", "HEAD", "--symbolic-full-name", "HEAD")
	// A harness or git hook may export these; they would override -C.
	cmd.Env = withoutEnv(os.Environ(), "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE")
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var gitDir, commonDir, sha, ref string
	switch {
	case err == nil && len(lines) == 4:
		gitDir, commonDir, sha, ref = lines[0], lines[1], lines[2], lines[3]
	case isExitError(err) && ctx.Err() == nil && len(lines) >= 2 && len(lines) <= 3 && filepath.IsAbs(lines[0]) && filepath.IsAbs(lines[1]):
		gitDir, commonDir = lines[0], lines[1]
		ref = unbornRef(gitDir)
	default:
		return Identity{}
	}

	var id Identity
	// @spec: repo-and-branch-identity#head-sha
	if isHexSHA(sha) {
		id.HeadSHA = &sha
	}
	// @spec: repo-and-branch-identity#branch-exact
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		id.Branch = &b
	}
	cfg := readConfig(filepath.Join(commonDir, "config"))
	id.Repo = ParseRemoteURL(cfg.remoteURL(id.Branch))
	return id
}

func isExitError(err error) bool {
	_, ok := err.(*exec.ExitError)
	return ok
}

// unbornRef reads the ref an unborn HEAD points at, such as refs/heads/main.
func unbornRef(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref, _ := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
	return ref
}

func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

type gitConfig struct {
	remotes      []string          // names in configuration order
	remoteURLs   map[string]string // name -> first url
	branchRemote map[string]string // branch -> remote name
}

// remoteURL picks origin, else the current branch's upstream remote, else the
// first remote in configuration order.
//
// @spec: repo-and-branch-identity#remote-precedence
func (c gitConfig) remoteURL(branch *string) string {
	if u, ok := c.remoteURLs["origin"]; ok {
		return u
	}
	if branch != nil {
		if u, ok := c.remoteURLs[c.branchRemote[*branch]]; ok {
			return u
		}
	}
	if len(c.remotes) > 0 {
		return c.remoteURLs[c.remotes[0]]
	}
	return ""
}

// readConfig reads the remote URLs and branch remotes from a git config file.
// It handles the subset of git's config syntax those keys use.
func readConfig(path string) gitConfig {
	cfg := gitConfig{remoteURLs: map[string]string{}, branchRemote: map[string]string{}}
	f, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer f.Close()
	var section, sub string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.LastIndexByte(line, ']')
			if end < 0 {
				continue
			}
			header := line[1:end]
			section, sub = header, ""
			if i := strings.IndexByte(header, '"'); i >= 0 {
				section = strings.TrimSpace(header[:i])
				sub = strings.TrimSuffix(header[i+1:], `"`)
				sub = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(sub)
			} else if i := strings.IndexByte(header, '.'); i >= 0 {
				section, sub = header[:i], header[i+1:]
			}
			section = strings.ToLower(section)
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		value = unquote(strings.TrimSpace(value))
		switch {
		case section == "remote" && key == "url":
			if _, seen := cfg.remoteURLs[sub]; !seen {
				cfg.remoteURLs[sub] = value
				cfg.remotes = append(cfg.remotes, sub)
			}
		case section == "branch" && key == "remote":
			cfg.branchRemote[sub] = value
		}
	}
	return cfg
}

func unquote(v string) string {
	if i := strings.IndexAny(v, "#;"); i >= 0 && !strings.Contains(v[:i], `"`) {
		v = strings.TrimSpace(v[:i])
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return v
}

// ParseRemoteURL parses an SSH, scp-style, HTTPS, or git:// remote URL into a
// lowercased host, owner, and name, stripping credentials, ports, and a
// trailing .git. Owner is every path segment before the name, so nested
// groups are kept. Local paths and file:// URLs have no repository identity
// and return nil.
//
// @spec: repo-and-branch-identity#repo-from-remote-url
// @spec: repo-and-branch-identity#repo-lowercased
// @spec: repo-and-branch-identity#host-preserved
func ParseRemoteURL(raw string) *contract.Repo {
	raw = strings.TrimSpace(raw)
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "file" {
			return nil
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-style: [user@]host:path
		colon := strings.IndexByte(raw, ':')
		if colon < 0 || strings.ContainsAny(raw[:colon], "/\\") || colon == 1 { // colon == 1: a Windows drive letter
			return nil
		}
		host = raw[:colon]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		path = raw[colon+1:]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segments := strings.Split(path, "/")
	// A host with no dot is an insteadOf alias (gh:owner/repo) or a local
	// name; without git's URL rewriting it would yield a wrong identity, so
	// it yields none.
	if !strings.Contains(host, ".") || len(segments) < 2 {
		return nil
	}
	name := segments[len(segments)-1]
	owner := strings.Join(segments[:len(segments)-1], "/")
	if name == "" || owner == "" {
		return nil
	}
	return &contract.Repo{Host: strings.ToLower(host), Owner: strings.ToLower(owner), Name: strings.ToLower(name)}
}

func withoutEnv(env []string, names ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range names {
			drop = drop || name == n
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
