---
name: login
description: Enroll this machine with Neo.Tax so Claude Code session metadata is reported. Use when the engineer runs /neo-tax:login or asks to connect, enroll, or log in to Neo.Tax.
---

Run this command with the Bash tool, with a timeout of 600000 milliseconds, because it waits up to five minutes for the engineer to finish in their browser:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" login
```

Before it finishes, tell the engineer it has opened a page in their browser where they paste the enrollment token and confirm their email, and pass on the address it printed in case the page did not open.

Never ask the engineer for the enrollment token in this conversation, and never add `--token` to the command. If the output says to run a command in a terminal, pass that command on to the engineer. Report the command's final message to the engineer.
