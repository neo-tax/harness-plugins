---
name: backfill
description: Resend metadata for every Claude Code session on this machine to Neo.Tax. Use when the engineer runs /neo-tax:backfill or asks to resend or backfill past sessions.
---

Run this command with the Bash tool:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" backfill --background
```

Report its final message to the engineer.
