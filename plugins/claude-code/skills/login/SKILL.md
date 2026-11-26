---
name: login
description: Enroll this machine with Neo.Tax so Claude Code session metadata is reported. Use when the engineer runs /neo-tax:login or asks to connect, enroll, or log in to Neo.Tax.
---

Run this command with the Bash tool, with run_in_background set, because it waits up to five minutes for the engineer to finish in their browser:

```bash
"${CLAUDE_SKILL_DIR}/../../bin/neo-tax-agent" login
```

Read its output once it says the form is opening. Tell the engineer the page where they paste their enrollment token and confirm their email has opened in their browser. If the output says no browser could be opened, give them the address it prints instead. Pass on the command it names to run in a terminal, for a machine whose browser cannot reach the page. Then wait for the command to finish and relay its final lines word for word. Do not reword them: they say what is sent to Neo.Tax and what never is, and a paraphrase such as "your sessions are being sent" reads as sending the conversations themselves.

Never ask the engineer for the enrollment token in this conversation, and never pass a token to the command.
