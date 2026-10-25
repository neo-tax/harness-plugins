---
name: login
description: Enroll this machine with Neo.Tax so Claude Code session metadata is reported. Use when the engineer runs /neo-tax:login or asks to connect, enroll, or log in to Neo.Tax.
---

Run this command with the Bash tool:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" login
```

Do exactly what its output says. If it says it needs input, ask the engineer for each item it lists, offering the emails it lists as choices, then run the same command again with `--token` and `--email` added. Report its final message to the engineer.
