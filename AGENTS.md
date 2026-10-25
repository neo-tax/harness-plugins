# AGENTS.md

Code that runs on an engineer's machine to observe AI coding sessions and report their metadata to Neo.Tax: one shared Go binary (`neo-tax-agent`), one JSON Schema wire contract, and a thin adapter per harness, each packaged as a plugin named `neo-tax`.

## Language

`CONTEXT.md` is the glossary. Use its terms exactly (harness, agent, adapter, plugin, hook, session, turn, device, device key, enrollment) and their listed avoid-words tell you which synonyms to replace. When a change introduces a concept the glossary lacks, add the term there in the same change.

## Specs are the source of truth

`docs/specs/architectural/` holds one spec per subsystem. A spec says how the system should be built; code says how it is. Where they disagree, the code is wrong.

- Before changing behavior, read the spec that governs it and work from its requirement lines.
- Every requirement line carries a status (`draft`, `approved`, `built`, `tested`). When you implement one, move it to `built` with a `status-reason`, or to `tested` when a test covers it. Bump `updated`, and `version` for a material change, and extend `affects` to the files that implement it.
- A requirement that no longer holds is a spec change in the same pull request, never a silent divergence.
- Tag code that implements a requirement with `@spec: <spec-id>#<slug>`.

## Hard rules

- The plugin, marketplace, skill prefix, and binary are all spelled `neo-tax`. Fleet install references the plugin as `neo-tax@neo-tax`.
- The wire contract is the JSON Schema under `contract/`. Go types are written to match it; the contract test proves they do. A schema change is a change here first.
- The field allowlist in the agent is the privacy statement. Every event field an adapter emits passes through it, and the published statement is generated from it.
- Events carry a device id and session metadata only. Prompt text, completion text, tool inputs, file paths, the working-directory path, and session titles never reach an event, a queue file, or a log line.

## Harness boundary

A hook execs the agent with the hook name on argv and the harness payload on stdin. Adapters map that payload and the on-disk transcript into session events; everything after that (allowlist, queue, flush, enrollment) is the agent's and shared.

## Testing

Parser behavior is specified by fixtures: an anonymized real transcript paired with the expected event stream. A new harness version is supported by adding a fixture. End-to-end tests run against the fake ingest server in this repo, never against Neo.Tax.
