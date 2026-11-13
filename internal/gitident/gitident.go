// Package gitident derives the repo, branch, and head_sha fields of a session
// event from the working directory a harness reports. The working-directory
// path itself never leaves this package.
package gitident

import (
	"bufio"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// git -C "" or a relative path would resolve against the agent's own
	// working directory, not the session's.
	if !filepath.IsAbs(dir) {
		return Identity{}
	}
	// git reports relative paths against its physical working directory, so
	// they are joined onto the resolved dir, not the logical one.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Identity{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	// Output order: git dir, common dir, HEAD's sha, HEAD's symbolic ref. On
	// an unborn branch git prints the two directories (and, depending on the
	// version, an unresolved "HEAD") and exits non-zero; the branch is then
	// read from the HEAD file itself.
	cmd := exec.CommandContext(ctx, GitCommand, "-C", dir, "rev-parse",
		"--git-dir", "--git-common-dir", "HEAD", "--symbolic-full-name", "HEAD")
	// A harness or git hook may export these; they would override -C.
	cmd.Env = withoutEnv(os.Environ(), "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE")
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var gitDir, commonDir, sha, ref string
	switch {
	case err == nil && len(lines) == 4:
		gitDir, commonDir, sha, ref = lines[0], lines[1], lines[2], lines[3]
	case isExitError(err) && ctx.Err() == nil && len(lines) >= 2 && len(lines) <= 3 && lines[0] != "" && lines[1] != "":
		gitDir, commonDir = lines[0], lines[1]
	default:
		return Identity{}
	}
	// Without --path-format=absolute (git 2.31+), rev-parse may print these
	// relative to dir, which is git's working directory under -C.
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	if sha == "" {
		ref = unbornRef(gitDir)
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
	if ref == "refs/heads/.invalid" { // reftable's placeholder; the real HEAD is in the reftable
		return ""
	}
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

// unquote parses a git config value as git does: double-quoted spans are
// taken literally apart from backslash escapes, an unquoted '#' or ';' starts
// a comment, and whitespace outside quotes at either end is dropped. Comment
// text therefore never becomes part of a value.
//
// @spec: repo-and-branch-identity#repo-null-when-unsafe
func unquote(v string) string {
	var b strings.Builder
	quoted := false
	end := 0 // length of b up to the last byte that was quoted or not whitespace
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '"':
			quoted = !quoted
			end = b.Len()
			continue
		case !quoted && (c == '#' || c == ';'):
			return b.String()[:end]
		case c == '\\' && i+1 < len(v):
			i++
			switch v[i] {
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			default:
				c = v[i]
			}
			b.WriteByte(c)
			end = b.Len()
			continue
		}
		if !quoted && (c == ' ' || c == '\t') && b.Len() == 0 {
			continue
		}
		b.WriteByte(c)
		if quoted || (c != ' ' && c != '\t') {
			end = b.Len()
		}
	}
	return b.String()[:end]
}

// ParseRemoteURL parses an SSH, scp-style, HTTPS, or git:// remote URL into a
// lowercased host, owner, and name, stripping credentials, ports, and a
// trailing .git. Owner is every path segment before the name, so nested
// groups are kept. Local paths, file:// URLs, scp-style absolute paths, and
// remote-helper transports (<transport>::<address>) have no repository
// identity and return nil, as does any result that fails validRepo: a remote
// is either reported whole and clean or not at all.
//
// @spec: repo-and-branch-identity#repo-from-remote-url
// @spec: repo-and-branch-identity#repo-lowercased
// @spec: repo-and-branch-identity#host-preserved
// @spec: repo-and-branch-identity#repo-null-when-unsafe
func ParseRemoteURL(raw string) *contract.Repo {
	raw = strings.TrimSpace(raw)
	if remoteHelper.MatchString(raw) {
		return nil
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "file" {
			return nil
		}
		// Hostname excludes userinfo and port. EscapedPath keeps percent
		// escapes, which validRepo rejects, so %2e%2e cannot become "..".
		host, path = u.Hostname(), strings.Trim(u.EscapedPath(), "/")
	} else {
		// scp-style: [userinfo@]host:path. Userinfo ends at the last '@'
		// before the first '/', and may itself contain ':' (user:token@host).
		rest := raw
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			slash = len(rest)
		}
		if at := strings.LastIndexByte(rest[:slash], '@'); at >= 0 {
			rest = rest[at+1:]
		}
		colon := strings.IndexByte(rest, ':')
		if colon < 0 || strings.ContainsAny(rest[:colon], "/\\") || colon == 1 { // colon == 1: a Windows drive letter
			return nil
		}
		host, path = rest[:colon], rest[colon+1:]
		if strings.HasPrefix(path, "/") { // an absolute path on the remote machine
			return nil
		}
		path = strings.TrimSuffix(path, "/")
	}
	// Lowercase first so that .GIT is stripped like .git.
	host, path = strings.ToLower(host), strings.ToLower(path)
	path = strings.TrimSuffix(path, ".git")
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return nil
	}
	repo := &contract.Repo{Host: host, Owner: path[:i], Name: path[i+1:]}
	if !validRepo(repo) {
		return nil
	}
	return repo
}

// remoteHelper matches git's <transport>::<address> syntax.
var remoteHelper = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+.-]*::`)

// segment is one host label run, owner segment, or name: a conservative
// charset with no ':', '@', '%', '/', whitespace, or quotes.
var segment = regexp.MustCompile(`^[a-z0-9._-]+$`)

// validRepo reports whether every component is non-empty, drawn from the
// conservative charset, and free of "." and ".." segments. Owner may be several
// segments joined by '/', for nested groups.
//
// @spec: repo-and-branch-identity#repo-null-when-unsafe
func validRepo(r *contract.Repo) bool {
	if !segment.MatchString(r.Host) || !validSegment(r.Name) {
		return false
	}
	for _, s := range strings.Split(r.Owner, "/") {
		if !validSegment(s) {
			return false
		}
	}
	return true
}

func validSegment(s string) bool {
	return segment.MatchString(s) && s != "." && s != ".."
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
