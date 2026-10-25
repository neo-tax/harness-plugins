---
id: repo-and-branch-identity
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-24
version: 1
affects: []
---

# Repository and branch identity

Governs how the agent derives the `repo`, `branch`, and `head_sha` fields from the working directory a harness reports, so that session events join to pull requests on the server by the same key the pull-request link stage uses.

## Requirements

- `repo-from-remote-url`(approved): `repo` is parsed from a git remote URL into `host`, `owner`, and `name`, accepting both SSH and HTTPS forms and stripping a trailing `.git`.
- `repo-lowercased`(approved): `host`, `owner`, and `name` are lowercased.
- `remote-precedence`(approved): The remote used is `origin` when it exists, else the current branch's upstream remote, else the first remote in configuration order.
- `repo-null-outside-git`(approved): `repo`, `branch`, and `head_sha` are null when the working directory is not inside a git repository.
- `host-preserved`(approved): A non-GitHub host is carried as its own `host` value; the agent does not drop or rewrite it.
- `branch-exact`(approved): `branch` is the current branch name with case preserved, and is null on a detached HEAD.
- `head-sha`(approved): `head_sha` is the full commit hash HEAD points at.
- `worktree-resolves-to-main-repo`(approved): A working directory inside a linked git worktree resolves to the same `repo` as the main checkout.
- `single-git-invocation`(approved): The agent obtains all three values with one git process invocation per hook.
- `git-failure-is-null`(approved): When git is absent or the invocation fails, all three fields are null and the event is still emitted.
- `cwd-never-sent`(approved): The working-directory path itself is never included in any event.

## Design Notes

Branch case is preserved because git branch names are case-sensitive; the server lowercases for joining if it chooses, but the agent does not destroy information the server might need.

`head_sha` is carried as a second join path that survives branch renames and squash merges, since the pull-request link stage already stores a PR's early commits.

## Risks

- **A repository with no remote.** `repo` is null and sessions cannot be joined to a PR. Accepted; the session still counts toward the device and shows as unmatched.
- **Forks whose `origin` is the fork.** `repo` names the fork, not the upstream the PR targets. Accepted for v0.1; the server can match on `head_sha` and the PR's head repository.
