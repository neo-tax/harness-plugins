# Contributing

This is the guide for working on the code. If you want to know what the plugin does and what it collects, start with the [README](README.md) and [PRIVACY.md](PRIVACY.md).

## Status

v0.1 is released. The agent (`cmd/neo-tax-agent`), the wire contract (`contract/`), the Claude Code adapter (`internal/adapter/claudecode`), the plugin (`plugins/claude-code`), and the fake ingest server (`cmd/fake-ingest`) are built against the specs; each requirement's status is in its spec.

```
go test ./...                       # unit, contract, fixture, end-to-end, and spec-traceability tests
go run ./cmd/fake-ingest -record -  # see exactly what the plugin sends
go run ./cmd/privacy-statement      # the privacy statement, generated from the field allowlist
```

## What ships

| Piece | What it is |
| --- | --- |
| `neo-tax-agent` | A static Go binary every adapter execs. Owns the field allowlist, the on-disk queue, flushing to Neo.Tax, enrollment, and transcript backfill. |
| Wire contract | JSON Schema for every request and response between the agent and Neo.Tax, versioned by `contract_version`. Neo.Tax's server validates against a pinned copy. |
| Adapters | Per-harness hook registrations and transcript parsing that turn a harness's payloads into normalized session events. Claude Code first; Codex next; Gemini CLI, Cursor, and OpenCode when a customer asks. |
| Plugins | The installable unit each harness sees, named `neo-tax` on every harness, bundling the agent and one adapter. |
| Fake ingest server | A stand-in for Neo.Tax that validates the wire contract and records what it receives. Used by tests, dogfooding, and any security team that wants to see exactly what the plugin sends. |

## How it works

1. An admin mints an enrollment token in Neo.Tax. An engineer runs `/neo-tax:login` in their harness, pastes it, and confirms their email. The device receives its own key, scoped to session ingest only.
2. The harness's lifecycle hooks exec the agent with the hook name on the command line and the harness payload on stdin. The agent reads the session transcript from its last cursor and emits one event per assistant turn: token usage by cache tier, model, repository, branch, and head commit.
3. Events pass through the allowlist, queue to disk, and flush in batches. Neo.Tax joins each session to a pull request by repository and branch, then to a ticket through its existing PR links, and prices each turn.

The published privacy statement's session-event and enrollment sections are generated from the allowlist in the agent and the contract, so they cannot drift; its telemetry section is kept by hand from the endpoint's mappings. With telemetry enabled, Claude Code's own exporter also sends its usage telemetry to Neo.Tax directly, without passing through the agent; platform-new's `docs/specs/architectural/platform/otlp-ingest.spec.md` governs what that path keeps, and `docs/specs/architectural/claude-code-telemetry-export.md` what this repository ships for it.

## Reading this repository

- `CONTEXT.md` is the glossary. Its terms are the vocabulary for every spec, commit, and conversation.
- `docs/specs/architectural/` holds one spec per subsystem: the wire contract, device enrollment, the Claude Code transcript parser, repository and branch identity, the event queue and backfill, the field allowlist, telemetry export, and test and release. Each requirement line carries a status; where code and spec disagree, the code is wrong.
- `AGENTS.md` is the operating guide for anyone, human or agent, making changes.

## Trying it locally

`scripts/dev.sh` runs the plugin from your working tree against the fake ingest server, so nothing reaches Neo.Tax. It needs `go`, `jq`, `curl`, and `claude`.

```sh
scripts/dev.sh up          # build, start the fake server, install the plugin, enroll
scripts/dev.sh claude      # Claude Code wired to the stack; log in to Claude the first time
scripts/dev.sh events -f   # in another terminal: each event as it arrives
scripts/dev.sh down        # stop and uninstall; --clean also deletes .dev/
```

Claude Code runs from a throwaway config directory under `.dev/`, so your own sessions and plugins are untouched. `up --my-config` installs into your real `~/.claude` instead, and enrolling then backfills your past sessions to the fake server. It replaces any neo-tax install there: your regular `claude` sessions then report to the fake server too, and `down` deletes the enrollment and any queued events. It refuses if neo-tax is already installed unless you pass `--force`.

To check token counts against the API's own, start with `up --audit`. Claude Code then talks to the API through a local proxy that logs the usage of every request (ids and counts only, never content), and `scripts/dev.sh audit` reports the requests no `session.turn` accounted for and any counts that differ.

`go test ./...` runs everything else; `scripts/smoke-test.sh` is the scripted install check CI runs.

## Releases

A semantic-version tag builds the agent and the fake ingest server for macOS (arm64, amd64), Linux (amd64, arm64), and Windows (amd64), attaches them to a GitHub release, and then opens a pull request that pins the marketplace to the tag; merging it (one approval) moves every install to the release. The step-by-step procedure is under "Cutting a release" in `docs/specs/architectural/test-and-release.md`.
