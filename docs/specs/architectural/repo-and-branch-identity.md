---
id: repo-and-branch-identity
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-25
version: 1
affects:
  - internal/gitident/*.go
  - internal/allowlist/allowlist.go
---

# Repository and branch identity

Governs how the agent derives the `repo`, `branch`, and `head_sha` fields from the working directory a harness reports, so that session events join to pull requests on the server by the same key the pull-request link stage uses.

## Requirements

- `repo-from-remote-url`(tested): `repo` is parsed from a git remote URL into `host`, `owner`, and `name`, accepting both SSH and HTTPS forms and stripping a trailing `.git`.
- `repo-lowercased`(tested): `host`, `owner`, and `name` are lowercased.
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

`owner` is every path segment between the host and the name, so a nested group such as GitLab's `group/subgroup` stays intact; credentials and ports in the URL are dropped. `cwd-never-sent` is enforced by the field allowlist, which has no working-directory field for any event kind.

`head_sha` is carried as a second join path that survives branch renames and squash merges, since the pull-request link stage already stores a PR's early commits.

## Risks

- **A repository with no remote.** `repo` is null and sessions cannot be joined to a PR. Accepted; the session still counts toward the device and shows as unmatched.
- **Forks whose `origin` is the fork.** `repo` names the fork, not the upstream the PR targets. Accepted for v0.1; the server can match on `head_sha` and the PR's head repository.
