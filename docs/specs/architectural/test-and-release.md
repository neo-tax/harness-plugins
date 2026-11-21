---
id: test-and-release
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-20
version: 7
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
  - scripts/release.sh
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
- `release-from-main`(built): The release workflow fails before building unless the tagged commit is on main.
  - status-reason: a step in the release workflow's check job; no release has run yet, and no test covers it
- `actions-pinned-by-sha`(tested): Every action a workflow uses is pinned to a full commit SHA.
- `action-pins-updated`(built): Each pin carries the version it stands for in a comment, and Dependabot proposes updates to the pins.
  - status-reason: the comments in each workflow and .github/dependabot.yml; no test checks either
- `workflow-token-least-privilege`(tested): Every workflow's default token permission is read-only.
- `job-scoped-write-and-secrets`(built): A job that writes (publishing the release, opening the pin pull request, posting review comments) grants itself only the permissions it needs, and a secret is passed only to the step that uses it.
  - status-reason: the permissions and env blocks of each workflow; the test checks only the top-level default
- `release-targets`(built): A release builds the agent for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, and windows/amd64.
  - status-reason: the release workflow's build matrix; CI cross-compiles the same five targets on every push
- `release-attaches-binaries`(built): Release binaries, the fake ingest server for the same targets, and a `checksums.txt` of all of them are attached to a GitHub release for the tag; a rerun finds the release already there and leaves it as it is.
  - status-reason: the release workflow's publish job; no release has run yet
- `public-release-downloads`(built): This repository is public, and the plugin's launcher downloads the agent binary for the plugin's version, and the release's `checksums.txt`, from the repository's GitHub release over HTTPS with no credential.
  - status-reason: the launcher fetches both with unauthenticated curl; the repository is still private, so both downloads fail until it goes public, which is a GitHub setting, not code
- `launcher-download-verified`(tested): The launcher downloads the binary named for the plugin's version and the machine's platform, runs it only when its SHA-256 matches the release's `checksums.txt`, and caches it for later runs. When the download or the check fails it runs nothing, caches nothing, and exits 0.
- `fleet-install-before-pin`(built): After publish, the release workflow installs the plugin the way a fleet does: the marketplace's `git-subdir` source fetched from GitHub at the tag, and the launcher downloading and verifying the released binary. It enrolls against the fake ingest server and, when an API key is present, runs one prompt. The pin pull request opens only if this succeeds.
  - status-reason: the release workflow's fleet_install job runs scripts/smoke-test.sh with SMOKE_RELEASE_TAG; no release has run yet
- `release-bumps-version`(tested): The release script moves every released `plugin.json`, and from v1.0 the latest channel marketplace's versions, to the next major, minor, or patch version, commits that bump to main as `Version bump to X.Y.Z`, and pushes it together with an annotated tag of the same message in one atomic push; the stable marketplace is left to the pin.
- `manifest-version-matches-tag`(tested): The release workflow fails if the version in the `plugin.json` of any plugin a marketplace lists, or the latest channel marketplace's version, differs from the tag; a plugin directory no marketplace lists is not checked; the stable marketplace's version is set to the tag when the marketplace is pinned after publish.
- `marketplace-source-is-this-repo`(tested): The marketplace manifest's source for each plugin is this repository at the released tag, so a harness's plugin auto-update follows releases.
- `pin-through-pull-request`(built): The stable marketplace's pin reaches main through a pull request that the release workflow opens after publish, carrying one signed commit, and merging it needs one approval and the `test` and `plugin_validate` checks; the workflow never pushes to main.
  - status-reason: the release workflow's pin_marketplace job; no release has run yet, and no test covers it
- `single-channel-v0-1`(tested): v0.1 publishes one marketplace that tracks the latest release.
- `unsigned-binaries-v0-1`(built): v0.1 binaries are unsigned.
  - status-reason: the signing jobs' `if` skips them for v0 tags; no release has run yet
- `signing-v1-0`(built): From v1.0 the release workflow notarizes macOS binaries and signs Windows binaries.
  - status-reason: the sign_macos and sign_windows jobs are written but have never run: they run only for v1+ tags, and the signing secrets they read do not exist yet, so neither job can succeed until those are added
- `stable-and-latest-channels-v1-0`(tested): From v1.0 a stable marketplace points at the last tagged release and a latest marketplace points at the main branch.
- `fake-ingest-is-released`(built): The fake ingest server ships as its own binary in every release.
  - status-reason: the release workflow builds and attaches fake-ingest for every target; no release has run yet

## Design Notes

The marketplace's plugin source is a `git-subdir` of this repository at the release tag, so the plugin version an engineer gets is exactly the tagged tree. A release bumps each `plugin.json` on main and pushes the tag; the release workflow checks the manifests against the tag, builds, publishes, installs the published release the way a fleet does, and only then opens a pull request that pins the stable marketplace on main to the tag, so the marketplace never points at a tag whose binaries are not attached yet. main's ruleset requires a squash-merged pull request with one approval, signed commits, and the `test` and `plugin_validate` checks, and the workflow cannot bypass it, so the workflow creates the pin commit through the GitHub API (which signs it) on a `release/pin-<tag>` branch rather than pushing; a rerun finds the open pull request and stops. Once the pin merges, the plugin's launcher downloads the agent binary attached to that tag from the public release, with no credential, and checks it against the release's `checksums.txt`. From v1.0 a second marketplace under `channels/latest/` points the same plugin at `main`.

Fixtures are the contract between the adapter and the harness: when a harness ships a new transcript format, the response is a new anonymized fixture and, only if it fails, a parser change. This keeps harness variability as data rather than code branches.

The install smoke test is the only test that exercises the harness's own hook dispatch; everything else stubs the harness by exec'ing the agent directly. On a pull request it installs the working tree and runs an agent built from it, because no release exists for that code yet. The launcher's download and checksum path is covered instead by a test that serves a fake release to the shipped launcher, changing only its release URL. After publish, the same script runs against the real release and the plugin source on GitHub, which is the only check of the whole path an engineer's machine takes. CI and the release install a fixed Claude Code version, so an upstream release can't turn every pull request red; moving it is a deliberate change.

Workflows pin actions to commit SHAs because a tag can be moved to other code, and the release workflow hands signing secrets to a third-party action whose output is published. The ancestry check keeps a release to code that reached main through review, plus the version-only bump commit the release script adds; who may push a `v*` tag at all is the `release-tags` ruleset on `refs/tags/v*`, which blocks creating, moving, and deleting those tags for everyone but organization admins, since the workflow cannot enforce it.

The agent is a static Go binary. It ships to every engineer's laptop, often pushed by a customer's IT through device management, so it must run with no runtime present and be small enough to live in every harness's plugin cache: Go cross-compiles to roughly 10 MB static binaries per target with no toolchain work, signs and notarizes cleanly, and is the conventional choice for fleet-deployed agents, which makes it the easiest story for a customer's security review. Nobody on the team has Go experience, so the binary is kept deliberately small (hook glue, transcript parsing, queue, HTTP client). The choice stays cheap to reverse only while two boundaries hold: the wire contract is specified in JSON Schema and never derived from Go types, and the harness boundary is exec-the-binary-with-the-event-on-argv-and-the-payload-on-stdin. With those held, swapping the language is a rewrite of one binary against the existing fixtures, and the end-to-end test above is what proves the boundary is honored.

### Cutting a release

1. Have an organization admin, the only role the `release-tags` ruleset lets create a `v*` tag and the only one that bypasses main's ruleset, run `scripts/release.sh [major|minor|patch]` from a clean, up-to-date checkout of main; it asks for the release type when none is given. It checks that main is green, sets `version` in every `plugins/*/.claude-plugin/plugin.json` (and, from v1.0, the metadata and plugin versions in `channels/latest/marketplace.json`) with `go run ./cmd/release-check -bump`, leaves `.claude-plugin/marketplace.json` for the pin, commits `Version bump to X.Y.Z`, tags that commit with the same message, pushes main and the tag atomically, and follows the release workflow. `--dry-run` shows the bump and reverts it without committing or pushing.
2. Wait for the release workflow to finish. It fails before building if the tagged commit is not on main or the manifests disagree with the tag, and it opens no pin pull request if the fleet-style install of the published release fails.
3. Approve and squash-merge the `Pin the marketplace to vX.Y.Z` pull request it opens. Until then engineers stay on the previous release. Pull requests opened with the workflow token do not trigger CI, so its required checks do not run on their own (see Risks).

The first release is the exception: `.claude-plugin/marketplace.json` already names `v0.1.0`, so until that tag is released an install resolves a tag that does not exist. v0.1.0 is tagged as soon as the v0.1 stack merges; its pin is then a no-op and no pull request is opened. A pre-release relative source would not help: the plugin's launcher downloads the agent from the GitHub release for the plugin's version, so an install before v0.1.0 exists has no binary either way.

v1.0.0 is the other exception, because it is the release that adds the latest channel and the release script cannot cross it: `release-check -bump major` from 0.x fails because v1 requires `channels/latest/marketplace.json`, and adding that file to main while still on 0.x would make every 0.x release fail the single-channel check. So v1.0.0 is cut by hand:

1. Open one pull request that sets every `plugins/*/.claude-plugin/plugin.json` to `1.0.0` and adds `channels/latest/marketplace.json` with metadata and plugin versions `1.0.0` and each plugin's source a `git-subdir` of this repository at `main`. `go run ./cmd/release-check v1.0.0` passes when it is complete.
2. Once it merges, an organization admin tags main's tip right away: `git tag -a v1.0.0 -m "Version bump to 1.0.0"` and `git push origin refs/tags/v1.0.0`. Until the release is published, an install from the latest channel finds no v1.0.0 binary and runs nothing.
3. Continue from step 2 of the procedure above. Every later release, majors included, goes through `scripts/release.sh`, which bumps the latest channel along with each `plugin.json`.

## Risks

- **The anonymizer misses a content-bearing field in a new transcript format.** Real content lands in the repository. Mitigated by the anonymizer allowlisting fields to keep rather than denylisting fields to strip, so an unknown field is replaced by default.
- **The smoke test depends on the harness being installable in CI.** A harness release that breaks headless install blocks releases. CI pins the Claude Code version, so this bites only when the pin is moved. Accepted; the pull request job can be marked required only once it has been stable for a release cycle.
- **The fleet install needs the repository to be public.** It clones the plugin source and downloads the binary with no credential, as engineers' machines do. While the repository is private both fail, the job fails, and no pin pull request opens. The repository goes public before v0.1.0 is tagged.
- **An organization admin can start a release.** The `release-tags` ruleset leaves creating a `v*` tag to organization admins, who also bypass main's ruleset. The ancestry check limits a release to commits on main, so the worst case short of an admin bypassing review is releasing reviewed code early. The release script's own bump commit reaches main without a pull request; `release-check -bump` changes only version values, and the release workflow checks and tests the tagged tree again.
- **The pin pull request's required checks never start.** main requires `test` and `plugin_validate`, but a pull request opened with the workflow token triggers no workflow, so the pin cannot merge as opened. Until the workflow opens it with a token that triggers CI, someone must start the checks by hand (closing and reopening it does) or an admin must bypass them.
