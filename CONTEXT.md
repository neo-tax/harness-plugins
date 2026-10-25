# Harness plugins

The code that runs on an engineer's machine to observe AI coding sessions and report their metadata to Neo.Tax. One shared agent, one wire contract, a thin adapter per coding harness.

## Language

**Harness**: A coding-agent product that runs sessions on an engineer's machine or in the cloud (Claude Code, Gemini CLI, Codex, Cursor, OpenCode). _Avoid_: tool, IDE, client

**Agent**: The single shared binary (`neo-tax-agent`) that every adapter execs; it owns everything a harness does not. _Avoid_: core, daemon, CLI

**Adapter**: The per-harness packaging and mapping that turns one harness's hook payloads and transcripts into normalized session events. _Avoid_: plugin (that word names the installable unit a harness sees), integration

**Plugin**: The installable unit a harness installs, named `neo-tax` on every harness; it bundles the agent, one adapter, and that harness's hook registrations.

**Hook**: A harness-defined lifecycle point at which the harness execs a command and hands it a JSON payload.

**Session**: One conversation an engineer has with a harness, identified by the harness's own session id.

**Session event**: A normalized record the agent emits about a session (opened, turn, PR opened, closed), carrying only the device id and non-personal session metadata. _Avoid_: event, usage record, telemetry

**Turn**: One assistant message within a session, identified by the harness's message id; the unit at which usage is reported. A turn produced by a subagent belongs to the parent session and carries the subagent's id.

**Subagent**: A child agent a harness spawns inside a session, with its own transcript and message ids but no session of its own. _Avoid_: sidechain, child session, sub-session _Avoid_: request, response, step

**Usage**: The token counts and pricing attributes of one turn (input, output, cache write by TTL, cache read, service tier), with a source of payload, transcript, or unknown. _Avoid_: cost, spend (those are dollars, computed server-side)

**Device**: One harness installation on one machine for one engineer, identified by a device id minted at enrollment.

**Device key**: The credential a device holds after enrollment; it carries the engineer's confirmed email and authorizes session ingest only. _Avoid_: API key, token, machine key

**Enrollment**: The one-time exchange in which a device presents an enrollment token, the engineer confirms their email, and the device receives its device key. _Avoid_: login, registration, pairing

**Enrollment token**: A short-lived token an admin mints and circulates so that devices can enroll; it can do nothing but enroll.

**Wire contract**: The JSON Schema definitions of every request and response between the agent and Neo.Tax, owned by this repo and versioned by `contract_version`. _Avoid_: API, protocol, payload format

**Fake ingest**: The shipped stand-in server that accepts the wire contract, validates it, and records what it receives, for tests, dogfooding, and security review.

## Relationships

- A **Plugin** bundles exactly one **Adapter** and the **Agent**.
- An **Adapter** targets exactly one **Harness**.
- A **Hook** belongs to one **Harness** and execs the **Agent** through the **Adapter**'s registration.
- A **Device** holds exactly one **Device key**, obtained through one **Enrollment**.
- A **Device key** identifies exactly one engineer; a **Session event** identifies its **Device**, never the engineer directly.
- A **Session** produces one or more **Session events**.

## Example dialogue

> **Dev:** "Does the Codex plugin need its own queue?" **Domain expert:** "No. The queue is the **Agent**'s. The Codex **Adapter** only maps Codex's **Hook** payloads and transcript into session events; everything after that is shared."

## Flagged ambiguities

- "plugin" was used for both the installable unit and the per-harness code — resolved: the installable unit is a **Plugin**; the per-harness code is an **Adapter**.
