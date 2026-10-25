---
id: repo-and-branch-identity
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-13
version: 2
affects:
  - internal/gitident/*.go
  - internal/allowlist/allowlist.go
  - internal/adapter/claudecode/hooks.go
---

# Repository and branch identity

Governs how the agent derives the `repo`, `branch`, and `head_sha` fields from the working directory a harness reports, so that session events join to pull requests on the server by the same key the pull-request link stage uses.

## Requirements

- `repo-from-remote-url`(tested): `repo` is parsed from a git remote URL into `host`, `owner`, and `name`, accepting both SSH and HTTPS forms and stripping a trailing `.git`.
- `repo-lowercased`(tested): `host`, `owner`, and `name` are lowercased.
- `repo-null-when-unsafe`(tested): `repo` is null when the remote is a local path, a `file://` URL, an scp-style absolute path, or a remote-helper transport (`<transport>::<address>`), or when any of `host`, `owner`, or `name` fails validation; credentials in a remote and comment text in the config never reach any field.
- `remote-precedence`(tested): The remote used is `origin` when it exists, else the current branch's upstream remote, else the first remote in configuration order.
- `repo-null-outside-git`(tested): `repo`, `branch`, and `head_sha` are null when the working directory is not inside a git repository.
- `host-preserved`(tested): A non-GitHub host is carried as its own `host` value; the agent does not drop or rewrite it.
- `branch-exact`(tested): `branch` is the current branch name with case preserved, and is null on a detached HEAD.
- `head-sha`(tested): `head_sha` is the full commit hash HEAD points at.
- `worktree-resolves-to-main-repo`(tested): A working directory inside a linked git worktree resolves to the same `repo` as the main checkout.
- `single-git-invocation`(tested): The agent obtains all three values with one git process invocation per hook.
- `git-failure-is-null`(tested): When git is absent or the invocation fails, all three fields are null and the event is still emitted.
- `cwd-never-sent`(tested): The working-directory path itself is never included in any event.

## Design Notes

Branch case is preserved because git branch names are case-sensitive; the server lowercases for joining if it chooses, but the agent does not destroy information the server might need.

`owner` is every path segment between the host and the name, so a nested group such as GitLab's `group/subgroup` stays intact; credentials and ports in the URL are dropped. Validation is an allowlist rather than a list of bad inputs: each of `host`, `name`, and every `owner` segment must be non-empty, drawn from lowercase letters, digits, `.`, `-`, and `_`, and not `.` or `..`. A remote that fails is null as a whole, never a partial `repo`, so a path, token, or percent-encoded escape that slips past the parser still cannot leave the machine. Config values are read as git reads them, so an inline `#` or `;` comment is dropped and a quoted value is taken from inside its quotes. `cwd-never-sent` is enforced by the field allowlist, which has no working-directory field for any event kind.

`head_sha` is carried as a second join path that survives branch renames and squash merges, since the pull-request link stage already stores a PR's early commits.

## Risks

- **A repository with no remote.** `repo` is null and sessions cannot be joined to a PR. Accepted; the session still counts toward the device and shows as unmatched.
- **Remotes outside the conservative charset.** A repository whose host or path uses characters beyond letters, digits, `.`, `-`, and `_` (an IPv6 literal host, a percent-encoded or non-ASCII name) gets a null `repo`. Accepted; the major forges restrict names to that charset, and a missed join is cheaper than a leaked path.
- **Host aliases.** A remote written with an SSH `Host` alias (`git@github-work:owner/repo`) or a `url.<base>.insteadOf` prefix (`gh:owner/repo`) is parsed as written: the alias is carried as `host` because the agent does not apply SSH or git rewriting. Accepted; `owner` and `name` are still right, and the server can match on them.
- **Remotes defined only in an included config file.** The agent reads the repository's own config and does not follow `include` or `includeIf`, so such a remote is not seen and `repo` falls back to a lower-precedence remote or null. Accepted for v0.1; repository remotes are rarely defined through includes.
- **A reftable repository with no commits yet.** `branch` is null rather than the branch name, because an unborn HEAD is read from the HEAD file and reftable keeps only a placeholder there. Accepted; the first commit fixes it.
- **Forks whose `origin` is the fork.** `repo` names the fork, not the upstream the PR targets. Accepted for v0.1; the server can match on `head_sha` and the PR's head repository.
