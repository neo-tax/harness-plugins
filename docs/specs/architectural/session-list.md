---
id: session-list
kind: architectural
owner:
  - Firas Abuzaid
created: 2026-11-21
updated: 2026-11-21
version: 1
affects:
  - internal/adapter/claudecode/sessions.go
  - internal/queue/queue.go
  - cmd/neo-tax-agent/main.go
  - plugins/claude-code/skills/sessions/SKILL.md
  - internal/adapter/claudecode/sessions_test.go
  - cmd/neo-tax-agent/main_test.go
  - e2e/plugin_test.go
---

# Session list

Governs the agent's `sessions` command, which lists an engineer's recent sessions on their own machine, with the titles the harness gave them, so they can match what Neo.Tax shows against sessions they recognize.

## Requirements

- `sessions-command`(tested): The agent exposes a `sessions` command, reachable through the harness's `neo-tax:sessions` entry point, that prints the most recently active sessions, newest first, one per line: when each was last active, its session id, whether the agent has reported it, its branch, and its title. It lists 20 sessions unless `--limit` says otherwise, and `--limit 0` lists every session.
  - status-reason: TestSessionsCommand runs the command and checks its columns, the empty case, and a negative limit; TestSkillsDelegateToAgent checks the skill runs only `sessions`
- `sessions-same-scope-as-backfill`(tested): The command lists the main transcripts `event-queue-and-backfill#backfill-session-id-from-harness` accepts, so every session it shows is one the agent can report, under the session id it shows.
  - status-reason: TestListSessions lists the UUID-named transcripts and skips a file named otherwise; ListSessions enumerates through the same mainTranscripts a backfill uses
- `title-local-only`(tested): The command reads each session's title from its transcript and writes it to standard output only. A title never reaches a session event, a queue file, a cursor, `backfill.json`, or a log line, and the command sends nothing over the network.
  - status-reason: TestListSessionsLeavesDataDirUnchanged checks no file in the data directory changes or holds a title; that the command opens no connection and logs nothing rests on review of sessions.go and main.go, which build no HTTP client and call no logger
- `title-precedence`(tested): A session's title is the last name the engineer gave it (Claude Code's `custom-title` or `agent-name` line), or else the last title the harness generated (`ai-title`); a session with neither shows no title.
  - status-reason: TestListSessions covers a rename after and before regenerated titles, the last of several ai-title lines, a prompt that merely contains the marker, and a session with no title
- `report-status`(tested): Each session's status is `queued` when the queue holds an event for it, `sent` when the agent has a cursor for its transcript and the queue holds none of its events, and `unread` when the agent has no cursor for it.
  - status-reason: TestListSessions covers a queued, a sent, and an unread session
- `sessions-read-only`(tested): The command takes no lock and changes no file in the plugin data directory, so it can run while hooks and a backfill run.
  - status-reason: TestListSessionsLeavesDataDirUnchanged snapshots the data directory before and after listing

## Design Notes

Session titles stay off the wire (`field-allowlist#title-excluded`) because they are a model's summary of the engineer's first prompt, which is conversation content. That leaves the server UI with session ids, timestamps, repositories, and branches, which an engineer can't easily map to the conversations they remember. This command bridges the gap locally: the engineer sees the title next to the same id the server shows, and the title never leaves the machine.

Claude Code regenerates `ai-title` as a session goes on and writes a new line each time, so the last line wins. A name the engineer chose with `/rename` reflects their intent better than a generated one, so it wins over any `ai-title`.

`sent` means the agent read the transcript and no longer holds its events. The queue's cap (`event-queue-and-backfill#queue-cap`) or a dead-lettered event can also leave a session in that state without the server having its events. That's rare and already reported as `dropped_events`, so the status doesn't try to distinguish it. A session with a cursor may also have new lines past the cursor that the next hook will read; it still shows `sent`.

The command reads transcripts in full to find their last title line, but only for the sessions it lists, and it checks each line for a title marker before parsing it.

## Risks

- **Titles in the conversation.** Through the `neo-tax:sessions` skill, the listing passes through the harness conversation and lands in the session's own transcript. That's the engineer's own machine and model provider, where the title came from in the first place, and the parser never copies titles into events (`claude-code-transcript-parser#content-never-read-into-events`).
- **A harness format change.** If Claude Code renames its title lines, sessions show no title. Nothing else depends on them.
