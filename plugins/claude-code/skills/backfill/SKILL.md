---
name: backfill
description: Send metadata for the Claude Code sessions on this machine that Neo.Tax has not received, continuing a paused backfill where it stopped. Use when the engineer runs /neo-tax:backfill or asks to resend or backfill past sessions.
---

Run this command with the Bash tool:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" backfill --background
```

It sends the metadata of the sessions the agent has not already reported, and a paused backfill continues where it stopped. If the engineer asks to start over and resend everything, add `--restart`.

Relay its final message to the engineer word for word. Do not reword it: it says what is sent to Neo.Tax and what never is, and a paraphrase such as "your sessions are being sent" reads as sending the conversations themselves.
