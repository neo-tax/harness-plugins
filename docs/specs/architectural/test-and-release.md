---
id: test-and-release
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-11-11
version: 2
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
  - scripts/smoke-test.sh
  - internal/specs/*.go
  - .claude-plugin/marketplace.json
---

# Test and release

Governs how the agent and plugins are tested, how a release is cut, and what artifacts a release produces.

## Requirements

- `fixtures-from-real-transcripts`(tested): Parser fixtures are real harness transcripts passed through a checked-in anonymizer that replaces emails, paths, repository names, and all message content with placeholders while preserving ids, usage blocks, timestamps, branches, and pull-request link lines.
- `fixture-pairs-expected-stream`(tested): Every fixture transcript is paired with the expected event stream it must produce, and the parser test compares the two exactly.
- `fixture-per-harness-version`(tested): Each supported harness version has at least one fixture.
- `unit-coverage`(tested): The parser, repository normalization, queue, backoff, and allowlist each have unit tests covering every requirement in their spec that can be exercised without a network.
- `contract-test`(tested): A test validates every event the agent can emit against the schema documents in this repository.
- `end-to-end-against-fake-ingest`(tested): A test execs the agent in the exact shape a harness hook does, with the hook name on the command line and a payload on standard input, against the fake ingest server, and asserts the recorded events.
- `fake-ingest-fault-modes`(tested): The fake ingest server can be instructed to return a schema rejection, an authentication rejection, a server error, or to stall, so the queue's failure paths are tested end to end.
- `plugin-validate-in-ci`(built): CI runs the harness's plugin validation command against the marketplace manifest and every plugin directory.
  - status-reason: a CI job (plugin_validate in .github/workflows/ci.yml); it is not a Go test
- `install-smoke-test`(tested): CI installs the plugin from the repository into a throwaway harness configuration directory, runs one non-interactive prompt with hooks pointed at the fake ingest server, and asserts one `session.opened`, one `session.turn`, and one `session.closed` arrived.
- `release-on-tag`(built): Pushing a semantic-version tag triggers the release workflow; nothing else publishes a release.
  - status-reason: the release workflow triggers only on v*.*.* tags; exercised by the first release, not by a test
- `release-targets`(built): A release builds the agent for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, and windows/amd64.
  - status-reason: the release workflow's build matrix; CI cross-compiles the same five targets on every push
- `release-attaches-binaries`(built): Release binaries and the fake ingest server for the same targets are attached to a GitHub release for the tag.
  - status-reason: the release workflow's publish job; exercised by the first release
- `manifest-version-matches-tag`(tested): The release workflow fails if any plugin manifest's version, or the latest channel marketplace's version, differs from the tag; the stable marketplace's version is set to the tag when the marketplace is pinned after publish.
- `marketplace-source-is-this-repo`(tested): The marketplace manifest's source for each plugin is this repository at the released tag, so a harness's plugin auto-update follows releases.
- `single-channel-v0-1`(tested): v0.1 publishes one marketplace that tracks the latest release.
- `unsigned-binaries-v0-1`(built): v0.1 binaries are unsigned.
  - status-reason: v0.x releases skip both signing jobs; structural
- `signing-v1-0`(built): From v1.0 the release workflow notarizes macOS binaries and signs Windows binaries.
  - status-reason: the sign_macos and sign_windows jobs run only for v1+ tags and need signing secrets that do not exist yet
- `stable-and-latest-channels-v1-0`(tested): From v1.0 a stable marketplace points at the last tagged release and a latest marketplace points at the main branch.
- `fake-ingest-is-released`(built): The fake ingest server ships as its own binary in every release.
  - status-reason: the release workflow builds and attaches fake-ingest for every target; exercised by the first release

## Design Notes

The marketplace's plugin source is a `git-subdir` of this repository at the release tag, so the plugin version an engineer gets is exactly the tagged tree. A release bumps each `plugin.json` on main and pushes the tag; the release workflow checks the manifests against the tag, builds, publishes, and only then pins the stable marketplace on main to the tag, so the marketplace never points at a tag whose binaries are not attached yet; the plugin's launcher then downloads the agent binary attached to that tag and checks it against the release's `checksums.txt`. From v1.0 a second marketplace under `channels/latest/` points the same plugin at `main`.

Fixtures are the contract between the adapter and the harness: when a harness ships a new transcript format, the response is a new anonymized fixture and, only if it fails, a parser change. This keeps harness variability as data rather than code branches.

The install smoke test is the only test that exercises the harness's own hook dispatch; everything else stubs the harness by exec'ing the agent directly.

The agent is a static Go binary. It ships to every engineer's laptop, often pushed by a customer's IT through device management, so it must run with no runtime present and be small enough to live in every harness's plugin cache: Go cross-compiles to roughly 10 MB static binaries per target with no toolchain work, signs and notarizes cleanly, and is the conventional choice for fleet-deployed agents, which makes it the easiest story for a customer's security review. Nobody on the team has Go experience, so the binary is kept deliberately small (hook glue, transcript parsing, queue, HTTP client). The choice stays cheap to reverse only while two boundaries hold: the wire contract is specified in JSON Schema and never derived from Go types, and the harness boundary is exec-the-binary-with-the-event-on-argv-and-the-payload-on-stdin. With those held, swapping the language is a rewrite of one binary against the existing fixtures, and the end-to-end test above is what proves the boundary is honored.

## Risks

- **The anonymizer misses a content-bearing field in a new transcript format.** Real content lands in the repository. Mitigated by the anonymizer allowlisting fields to keep rather than denylisting fields to strip, so an unknown field is replaced by default.
- **The smoke test depends on the harness being installable in CI.** A harness release that breaks headless install blocks releases. Accepted; the job can be marked required only once it has been stable for a release cycle.
