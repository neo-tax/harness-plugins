---
name: login
description: Enroll this machine with Neo.Tax so Claude Code session metadata is reported. Use when the engineer runs /neo-tax:login or asks to connect, enroll, or log in to Neo.Tax.
---

Run this command with the Bash tool, with run_in_background set, because it waits up to five minutes for the engineer to finish in their browser:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" login
```

Read its output once it prints a web address. Tell the engineer the page where they paste their enrollment token and confirm their email has opened in their browser, and give them that address in case it did not open. If the output also names a command to run in a terminal, pass that on too, for a machine whose browser cannot reach the page. Then wait for the command to finish and report its final message.

Never ask the engineer for the enrollment token in this conversation, and never add `--token` to the command.
