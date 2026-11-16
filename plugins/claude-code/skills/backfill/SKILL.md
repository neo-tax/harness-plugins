---
name: backfill
description: Send metadata for the Claude Code sessions on this machine that Neo.Tax has not received, continuing a paused backfill where it stopped. Use when the engineer runs /neo-tax:backfill or asks to resend or backfill past sessions.
---

Run this command with the Bash tool:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" backfill --background
```

It sends the sessions the agent has not already reported, and a paused backfill continues where it stopped. If the engineer asks to start over and resend everything, add `--restart`.

Report its final message to the engineer.
