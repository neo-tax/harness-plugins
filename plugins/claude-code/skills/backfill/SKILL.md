---
name: backfill
description: Send metadata for every Claude Code session on this machine to Neo.Tax, continuing a paused backfill where it stopped. Use when the engineer runs /neo-tax:backfill or asks to resend or backfill past sessions.
---

Run this command with the Bash tool:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" backfill --background
```

If the engineer asks to start over and resend everything, add `--restart`; otherwise a paused backfill continues where it stopped.

Report its final message to the engineer.
