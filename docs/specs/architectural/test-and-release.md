---
id: test-and-release
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-22
version: 8
affects:
  - internal/fakeingest/*.go
  - .github/workflows/ci.yml
  - internal/adapter/claudecode/anonymize.go
  - internal/adapter/claudecode/testdata/fixtures/**
  - cmd/anonymize-transcript/*.go
  - e2e/*.go
  - .github/workflows/release.yml
  - internal/release/*.go
  - cmd/release-check/*.go
  - scripts/prepare-release.sh
  - scripts/finish-release.sh
  - scripts/smoke-test.sh
  - internal/specs/*.go
  - .claude-plugin/marketplace.json
  - plugins/claude-code/bin/neo-tax-agent
  - .github/workflows/claude_code_review.yml
  - .github/dependabot.yml
---

# Test and release

Governs how the agent and plugins are tested, how a release is cut, and what artifacts a release produces.

## Requirements

- `fixtures-from-real-transcripts`(tested): Parser fixtures are real harness transcripts passed through a checked-in anonymizer that replaces emails, paths, repository names, and all message content with placeholders while preserving ids, usage blocks, timestamps, branches, and pull-request link lines.
- `fixture-pairs-expected-stream`(tested): Every fixture transcript is paired with the expected event stream it must produce, and the parser test compares the two exactly.
- `fixture-per-harness-version`(tested): Each supported harness version has at least one fixture.
- `unit-coverage`(tested): Every requirement in the parser, repository identity, queue and backfill, and allowlist specs is `tested` or is `built` with a status-reason, and a spec check fails otherwise.
- `contract-test`(tested): A test validates every event the agent can emit against the schema documents in this repository.
- `end-to-end-against-fake-ingest`(tested): A test execs the agent in the exact shape a harness hook does, with the hook name on the command line and a payload on standard input, against the fake ingest server, and asserts the recorded events.
- `fake-ingest-fault-modes`(tested): The fake ingest server can be instructed to return a schema rejection, an authentication rejection, a server error, or to stall, so the queue's failure paths are tested end to end.
- `spec-traceability-check`(built): A Go test checks the specs against the code: a requirement marked `tested` has a test carrying its `@spec` tag, one marked `built` has a status-reason, every `@spec` tag names a requirement that exists, and every `affects` glob matches a file. The check is textual. A `tested` line passes when any Go test file or `scripts/*-test.sh` contains the tag anywhere, comments included, so whether that test exercises the requirement is left to review; and a requirement line whose status is misspelled is not recognized as a requirement, so it is skipped rather than failed.
  - status-reason: TestStatusesAreHonest, TestTagsResolve, and TestAffectsGlobsMatch in internal/specs/specs_test.go; they carry no tag for this line
- `plugin-validate-in-ci`(built): CI runs the harness's plugin validation command against the marketplace manifest and every plugin directory, and main's ruleset requires that job to pass.
  - status-reason: a CI job (plugin_validate in .github/workflows/ci.yml) and a repository ruleset; neither is a Go test
- `install-smoke-test`(tested): CI installs the plugin from the repository into a throwaway harness configuration directory, runs one non-interactive prompt with hooks pointed at the fake ingest server, and asserts one `session.opened`, at least one `session.turn`, and one `session.closed` arrived. The CI job does not block a merge and is skipped where the API key is absent.
- `release-on-tag`(built): Pushing a semantic-version tag triggers the release workflow; nothing else publishes a release.
  - status-reason: the release workflow triggers only on v*.*.* tags; no release has run yet, and no test covers it
- `release-from-main`(built): The release workflow fails before building unless the tagged commit has a single parent, that parent is on main, and the commit changes nothing but plugin manifests and marketplaces.
  - status-reason: a step in the release workflow's check job; no release has run this way yet, and no test covers it
- `actions-pinned-by-sha`(tested): Every action a workflow uses is pinned to a full commit SHA.
- `action-pins-updated`(built): Each pin carries the version it stands for in a comment, and Dependabot proposes updates to the pins.
  - status-reason: the comments in each workflow and .github/dependabot.yml; no test checks either
- `workflow-token-least-privilege`(tested): Every workflow's default token permission is read-only.
- `job-scoped-write-and-secrets`(built): A job that writes (publishing the release, opening the release pull request, posting review comments) grants itself only the permissions it needs, and a secret is passed only to the step that uses it.
  - status-reason: the permissions and env blocks of each workflow; the test checks only the top-level default
- `release-targets`(built): A release builds the agent for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, and windows/amd64.
  - status-reason: the release workflow's build matrix; CI cross-compiles the same five targets on every push
- `release-attaches-binaries`(built): Release binaries, the fake ingest server for the same targets, and a `checksums.txt` of all of them are attached to a GitHub release for the tag; a rerun finds the release already there and leaves it as it is.
  - status-reason: the release workflow's publish job; no release has run yet
- `public-release-downloads`(built): This repository is public, and the plugin's launcher downloads the agent binary for the plugin's version, and the release's `checksums.txt`, from the repository's GitHub release over HTTPS with no credential.
  - status-reason: the launcher fetches both with unauthenticated curl; the repository is still private, so both downloads fail until it goes public, which is a GitHub setting, not code
- `launcher-download-verified`(tested): The launcher downloads the binary named for the plugin's version and the machine's platform, runs it only when its SHA-256 matches the release's `checksums.txt`, and caches it for later runs. When the download or the check fails it runs nothing, caches nothing, and exits 0.
- `fleet-install-before-rollout`(built): After publish, the release workflow installs the plugin the way a fleet does: the tagged commit's marketplace, its `git-subdir` source fetched from GitHub at the tag, and the launcher downloading and verifying the released binary. It enrolls against the fake ingest server and, when an API key is present, runs one prompt. main is fast-forwarded to the release only if this succeeds.
  - status-reason: the release workflow's fleet_install job runs scripts/smoke-test.sh with SMOKE_RELEASE_TAG, and scripts/finish-release.sh refuses to fast-forward unless that workflow run succeeded; no release has run this way yet
- `release-bumps-version`(tested): The prepare script moves every released `plugin.json`, the stable marketplace's versions and source refs, and from v1.0 the latest channel marketplace's versions, to the next major, minor, or patch version in one commit, `Version bump to X.Y.Z`, on a `release/vX.Y.Z` branch off main's tip. The next version follows the newer of the manifests' version and the newest release tag, so a release that was never rolled out is not cut again. It pushes the branch together with an annotated tag of the same message in one atomic push.
- `superseded-release-closed`(built): The prepare script refuses to start while an open release pull request's commit sits on main's tip, since that release can still be finished. It closes the pull request and deletes the branch of every other open release, which can no longer be fast-forwarded, once its own release is pushed.
  - status-reason: scripts/prepare-release.sh; no test covers it
- `release-pull-request-by-workflow`(built): Once the tagged commit passes the release workflow's checks, the workflow opens the `Release vX.Y.Z` pull request from `release/vX.Y.Z` to main with its own token; a rerun finds it and opens no other. CI runs on pushes to `release/**`, so the pull request carries the `test` and `plugin_validate` checks even though a pull request opened with the workflow token starts no workflow.
  - status-reason: the release workflow's open_pull_request job and the push trigger in .github/workflows/ci.yml; no release has run this way yet, and no test covers it
- `manifest-version-matches-tag`(tested): The release workflow fails if the version in the `plugin.json` of any plugin a marketplace lists, the stable marketplace's versions or source refs, or the latest channel marketplace's versions differ from the tag; a plugin directory no marketplace lists is not checked.
- `marketplace-source-is-this-repo`(tested): The marketplace manifest's source for each plugin is this repository at the released tag, so a harness's plugin auto-update follows releases.
- `rollout-by-fast-forward`(built): A release reaches main only when an organization admin fast-forwards main to the tagged commit, after the release workflow for the tag succeeded and CI on the commit is green, so the commit on main is the tagged commit. A release whose parent is no longer main's tip is never rolled out, and the release pull request is never merged with the merge button.
  - status-reason: scripts/finish-release.sh; no release has run this way yet, and no test covers it
- `single-channel-v0-1`(tested): v0.1 publishes one marketplace that tracks the latest release.
- `unsigned-binaries-v0-1`(built): v0.1 binaries are unsigned.
  - status-reason: the signing jobs' `if` skips them for v0 tags; no release has run yet
- `signing-v1-0`(built): From v1.0 the release workflow notarizes macOS binaries and signs Windows binaries.
  - status-reason: the sign_macos and sign_windows jobs are written but have never run: they run only for v1+ tags, and the signing secrets they read do not exist yet, so neither job can succeed until those are added
- `stable-and-latest-channels-v1-0`(tested): From v1.0 a stable marketplace points at the last tagged release and a latest marketplace points at the main branch.
- `fake-ingest-is-released`(built): The fake ingest server ships as its own binary in every release.
  - status-reason: the release workflow builds and attaches fake-ingest for every target; no release has run yet

## Design Notes

The marketplace's plugin source is a `git-subdir` of this repository at the release tag, so the plugin version an engineer gets is exactly the tagged tree. Engineers read the marketplace from main, so whatever main's stable marketplace names is what every install and auto-update gets. A release is one commit on a `release/vX.Y.Z` branch off main's tip that bumps each `plugin.json` and pins the stable marketplace to the tag, and the tag points at that commit. The release workflow checks the commit and its manifests, builds, publishes, and installs the published release the way a fleet does, all before main changes. Only then does an admin fast-forward main to the tagged commit, so the marketplace never points at a tag whose binaries are not attached yet, and the tag is on main. This mirrors platform-new and prediction-service, whose release scripts tag a release candidate and later fast-forward production to it, with one difference: here the release workflow, not the script, opens the release pull request, once the tagged commit has passed its checks.

A fast-forward is the only way the tagged commit itself lands on main: GitHub's merge button always writes a new commit, and main's ruleset allows only squash merges, so the admin's bypass of that ruleset is what makes the push possible. The release pull request exists for review and CI; GitHub marks it merged when its head lands on main. If main moves between prepare and finish, the release cannot be fast-forwarded: it stays published but unpinned, and the next release supersedes it, taking the version after it and closing its pull request. Once main carries the pin, the plugin's launcher downloads the agent binary attached to that tag from the public release, with no credential, and checks it against the release's `checksums.txt`. From v1.0 a second marketplace under `channels/latest/` points the same plugin at `main`.

Moving the prepare and finish steps into workflows, with a dedicated GitHub App as the ruleset bypass actor whose key only an admin-approved environment can use, is the planned next step, shared with platform-new and prediction-service.

Fixtures are the contract between the adapter and the harness: when a harness ships a new transcript format, the response is a new anonymized fixture and, only if it fails, a parser change. This keeps harness variability as data rather than code branches.

The install smoke test is the only test that exercises the harness's own hook dispatch; everything else stubs the harness by exec'ing the agent directly. On a pull request it installs the working tree and runs an agent built from it, because no release exists for that code yet. The launcher's download and checksum path is covered instead by a test that serves a fake release to the shipped launcher, changing only its release URL. After publish, the same script runs against the real release and the plugin source on GitHub, which is the only check of the whole path an engineer's machine takes. CI and the release install a fixed Claude Code version, so an upstream release can't turn every pull request red; moving it is a deliberate change.

Workflows pin actions to commit SHAs because a tag can be moved to other code, and the release workflow hands signing secrets to a third-party action whose output is published. The parent and diff checks keep a release to code that reached main through review, plus the manifest-only bump commit the prepare script adds; who may push a `v*` tag at all is the `release-tags` ruleset on `refs/tags/v*`, which blocks creating, moving, and deleting those tags for everyone but organization admins, since the workflow cannot enforce it.

The agent is a static Go binary. It ships to every engineer's laptop, often pushed by a customer's IT through device management, so it must run with no runtime present and be small enough to live in every harness's plugin cache: Go cross-compiles to roughly 10 MB static binaries per target with no toolchain work, signs and notarizes cleanly, and is the conventional choice for fleet-deployed agents, which makes it the easiest story for a customer's security review. Nobody on the team has Go experience, so the binary is kept deliberately small (hook glue, transcript parsing, queue, HTTP client). The choice stays cheap to reverse only while two boundaries hold: the wire contract is specified in JSON Schema and never derived from Go types, and the harness boundary is exec-the-binary-with-the-event-on-argv-and-the-payload-on-stdin. With those held, swapping the language is a rewrite of one binary against the existing fixtures, and the end-to-end test above is what proves the boundary is honored.

### Cutting a release

1. Have an organization admin, the only role the `release-tags` ruleset lets create a `v*` tag and the only one that bypasses main's ruleset, run `scripts/prepare-release.sh [major|minor|patch]` from a clean, up-to-date checkout of main; it asks for the release type when none is given. It checks that main is green, and with `go run ./cmd/release-check -bump` sets `version` in every `plugins/*/.claude-plugin/plugin.json`, the versions and source refs in `.claude-plugin/marketplace.json`, and, from v1.0, the metadata and plugin versions in `channels/latest/marketplace.json`. It commits that as `Version bump to X.Y.Z` on `release/vX.Y.Z`, tags the commit with the same message, pushes the branch and the tag atomically, and follows the release workflow, which opens the `Release vX.Y.Z` pull request. `--dry-run` shows the bump and reverts it without committing or pushing.
2. Wait for the release workflow to finish. It fails before building if the tagged commit is not a manifest-only commit on top of main or the manifests disagree with the tag, and it fails if the fleet-style install of the published release fails. Engineers stay on the previous release throughout.
3. Once CI on the pull request is green, have an organization admin run `scripts/finish-release.sh vX.Y.Z`. It checks that main has not moved, that the release workflow succeeded, and that CI is green, then fast-forwards main to the tagged commit, which rolls the release out and marks the pull request merged. Never merge the release pull request with the merge button.

v1.0.0 is the exception, because it is the release that adds the latest channel and the prepare script cannot cross it: `release-check -bump major` from 0.x fails because v1 requires `channels/latest/marketplace.json`, and adding that file to main while still on 0.x would make every 0.x release fail the single-channel check. So an organization admin prepares v1.0.0 by hand, doing what the script does:

1. From a clean checkout of main's tip, create `release/v1.0.0`. Set every `plugins/*/.claude-plugin/plugin.json` to `1.0.0`, set the versions in `.claude-plugin/marketplace.json` to `1.0.0` and its source refs to `v1.0.0`, and add `channels/latest/marketplace.json` with metadata and plugin versions `1.0.0` and each plugin's source a `git-subdir` of this repository at `main`. `go run ./cmd/release-check v1.0.0` passes when it is complete.
2. Commit it as `Version bump to 1.0.0`, tag it with `git tag -a v1.0.0 -m "Version bump to 1.0.0"`, and push both with `git push --atomic origin refs/heads/release/v1.0.0 refs/tags/v1.0.0`. The release workflow opens the pull request.
3. Continue from step 2 of the procedure above. Every later release, majors included, goes through `scripts/prepare-release.sh`, which bumps the latest channel along with each `plugin.json`.

## Risks

- **The anonymizer misses a content-bearing field in a new transcript format.** Real content lands in the repository. Mitigated by the anonymizer allowlisting fields to keep rather than denylisting fields to strip, so an unknown field is replaced by default.
- **The smoke test depends on the harness being installable in CI.** A harness release that breaks headless install blocks releases. CI pins the Claude Code version, so this bites only when the pin is moved. Accepted; the pull request job can be marked required only once it has been stable for a release cycle.
- **The fleet install needs the repository to be public.** It clones the plugin source and downloads the binary with no credential, as engineers' machines do. While the repository is private both fail, the job fails, and the release cannot be finished. The repository goes public before v0.1.0 is tagged.
- **An organization admin can start a release.** The `release-tags` ruleset leaves creating a `v*` tag to organization admins, who also bypass main's ruleset. The ancestry check limits a release to commits on main, so the worst case short of an admin bypassing review is releasing reviewed code early. The release commit reaches main by an admin's push rather than through the merge button; `release-check -bump` changes only version and ref values, the release workflow refuses a tagged commit that changes anything else, CI runs on the release commit, and the finish script refuses to push until it is green.
- **main moves between prepare and finish.** The release can no longer be fast-forwarded, so it stays published but never reaches engineers, and its tag is never on main. The finish script refuses rather than rebasing, which would leave the tag off main. Mitigated by finishing as soon as the release workflow and CI pass, usually within minutes; otherwise the next release supersedes it, takes the version after it, and closes its pull request and branch. Its tag and GitHub release stay, unreferenced.
