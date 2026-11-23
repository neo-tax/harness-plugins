---
name: sessions
description: List the engineer's recent Claude Code sessions on this machine with their titles and whether Neo.Tax has received them. Use when the engineer runs /neo-tax:sessions or asks which sessions were reported, or how to match a session id in Neo.Tax to a conversation.
---

Run this command with the Bash tool:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" sessions
```

If the engineer asks for more or fewer sessions, add `--limit N` (`--limit 0` lists every session).

Show the engineer the table as printed. Titles are shown only on this machine; Neo.Tax never receives them.
