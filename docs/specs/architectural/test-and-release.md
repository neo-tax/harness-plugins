---
id: test-and-release
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-10-24
updated: 2026-10-24
version: 1
affects: []
---

# Test and release

Governs how the agent and plugins are tested, how a release is cut, and what artifacts a release produces.

## Requirements

- `fixtures-from-real-transcripts`(approved): Parser fixtures are real harness transcripts passed through a checked-in anonymizer that replaces emails, paths, repository names, and all message content with placeholders while preserving ids, usage blocks, timestamps, branches, and pull-request link lines.
- `fixture-pairs-expected-stream`(approved): Every fixture transcript is paired with the expected event stream it must produce, and the parser test compares the two exactly.
- `fixture-per-harness-version`(approved): Each supported harness version has at least one fixture.
- `unit-coverage`(approved): The parser, repository normalization, queue, backoff, and allowlist each have unit tests covering every requirement in their spec that can be exercised without a network.
- `contract-test`(approved): A test validates every event the agent can emit against the schema documents in this repository.
- `end-to-end-against-fake-ingest`(approved): A test execs the agent in the exact shape a harness hook does, with the hook name on the command line and a payload on standard input, against the fake ingest server, and asserts the recorded events.
- `fake-ingest-fault-modes`(approved): The fake ingest server can be instructed to return a schema rejection, an authentication rejection, a server error, or to stall, so the queue's failure paths are tested end to end.
- `plugin-validate-in-ci`(approved): CI runs the harness's plugin validation command against the marketplace manifest and every plugin directory.
- `install-smoke-test`(approved): CI installs the plugin from the repository into a throwaway harness configuration directory, runs one non-interactive prompt with hooks pointed at the fake ingest server, and asserts one `session.opened`, one `session.turn`, and one `session.closed` arrived.
- `release-on-tag`(approved): Pushing a semantic-version tag triggers the release workflow; nothing else publishes a release.
- `release-targets`(approved): A release builds the agent for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, and windows/amd64.
- `release-attaches-binaries`(approved): Release binaries and the fake ingest server for the same targets are attached to a GitHub release for the tag.
- `manifest-version-matches-tag`(approved): The release workflow fails if any plugin or marketplace manifest's version differs from the tag.
- `marketplace-source-is-this-repo`(approved): The marketplace manifest's source for each plugin is this repository at the released tag, so a harness's plugin auto-update follows releases.
- `single-channel-v0-1`(approved): v0.1 publishes one marketplace that tracks the latest release.
- `unsigned-binaries-v0-1`(approved): v0.1 binaries are unsigned.
- `signing-v1-0`(approved): From v1.0 the release workflow notarizes macOS binaries and signs Windows binaries.
- `stable-and-latest-channels-v1-0`(approved): From v1.0 a stable marketplace points at the last tagged release and a latest marketplace points at the main branch.
- `fake-ingest-is-released`(approved): The fake ingest server ships as its own binary in every release.

## Design Notes

Fixtures are the contract between the adapter and the harness: when a harness ships a new transcript format, the response is a new anonymized fixture and, only if it fails, a parser change. This keeps harness variability as data rather than code branches.

The install smoke test is the only test that exercises the harness's own hook dispatch; everything else stubs the harness by exec'ing the agent directly, which is the boundary ADR 0001 names as what keeps the implementation language reversible.

## Risks

- **The anonymizer misses a content-bearing field in a new transcript format.** Real content lands in the repository. Mitigated by the anonymizer allowlisting fields to keep rather than denylisting fields to strip, so an unknown field is replaced by default.
- **The smoke test depends on the harness being installable in CI.** A harness release that breaks headless install blocks releases. Accepted; the job can be marked required only once it has been stable for a release cycle.
