# Neo.Tax plugin for AI coding tools

The `neo-tax` plugin reports **metadata** about AI coding sessions (token counts, model, repository, and branch) to Neo.Tax, so a company can see how much AI spend went into each pull request, ticket, and project. Nobody has to tag anything by hand.

It never sends your prompts, Claude's responses, your code, tool inputs or outputs, file paths, or session titles.

Today it supports [Claude Code](https://claude.com/claude-code), with [Codex](https://openai.com/codex) support coming soon.

This page is written for engineers and IT or security admins evaluating the plugin. If you want to work on the code, see [CONTRIBUTING.md](CONTRIBUTING.md).

## What it collects

| Sent to Neo.Tax | Never leaves the machine |
| --- | --- |
| Token counts per model response, split by cache tier | Prompt text |
| Model name and pricing tier (e.g. fast mode) | Response text |
| Repository host, owner, and name, from the git remote | Source code |
| Branch name and current commit hash | Tool inputs and outputs (commands, file contents) |
| Pull request numbers the session touched | File paths and the working directory |
| Session id, timestamps, and Claude Code version | Session titles |
| A random device id | |

To match a session id in Neo.Tax to a conversation you remember, run `/neo-tax:sessions`. It lists your recent sessions with their titles and whether each was sent. The titles are read and shown on your machine only.

At login, the engineer confirms their email, and the plugin sends it once along with the machine's hostname, OS, and CPU architecture. That email is stored with the device's key on Neo.Tax's side. It is not attached to each event.

**[PRIVACY.md](PRIVACY.md) is the complete list**, field by field. It's generated from the allowlist built into the plugin: any field not on it is removed on the engineer's machine before it's written to disk or sent. A test fails if the published list and the code ever disagree.

A few fields can identify a person even though they aren't meant to: branch names are free text, a repository owner can be a personal GitHub username, and hostnames often contain a name. PRIVACY.md calls these out.

## How it works

1. An admin creates an enrollment token in Neo.Tax and shares it with their team.
2. Each engineer installs the plugin and runs `/neo-tax:login`. A browser page opens where they paste the token and confirm their email. The machine gets its own key, which can only submit session metadata.
3. From then on, Claude Code runs the plugin at the start and end of each session and after each response. The plugin reads Claude Code's local session log, keeps only the allowlisted fields, queues them on disk, and sends them in batches.
4. On login, the plugin also backfills metadata for past Claude Code sessions on the machine, so Neo.Tax can see earlier usage.

The plugin is designed to stay out of the way. It runs in the background, never blocks Claude Code, and if it can't download, enroll, or reach Neo.Tax, it fails quietly and retries later.

### Network access

| Destination | When |
| --- | --- |
| `github.com` (this repository's releases) | Once per plugin version, to download the agent binary. The download is checked against the release's SHA-256 checksums. |
| `api.neo.tax/harness` | At login, and when sending session metadata. |
| `api.neo.tax/otlp` | Only if your organization turns on Claude Code's own telemetry (see below). |

### Files on disk

Everything lives in Claude Code's plugin data directory, `~/.claude/plugins/data/neo-tax-neo-tax-plugins/`: the downloaded agent binary, the device key, the outgoing queue, a record of how far each session log has been read, and a log file. The log never contains prompt text, response text, tool inputs, or file paths.

## See exactly what it sends

You don't have to take our word for it. This repository includes a fake Neo.Tax server that accepts the same requests and prints everything it receives:

```sh
go run ./cmd/fake-ingest -record -
```

[CONTRIBUTING.md](CONTRIBUTING.md#trying-it-locally) shows how to point a local Claude Code install at it with `scripts/dev.sh`, so you can watch each event arrive while you work. The agent is a single Go binary with no network calls beyond the ones listed above, and the source is all here.

## Installing

### One engineer

From inside Claude Code:

```
/plugin marketplace add neo-tax/harness-plugins
/plugin install neo-tax@neo-tax-plugins
/neo-tax:login
```

### Across your organization

Add this to your Claude Code [managed settings](https://docs.claude.com/en/docs/claude-code/settings) to install the plugin for everyone:

```json
{
  "extraKnownMarketplaces": {
    "neo-tax-plugins": {
      "autoUpdate": true,
      "source": { "source": "github", "repo": "neo-tax/harness-plugins" }
    }
  },
  "enabledPlugins": { "neo-tax@neo-tax-plugins": true }
}
```

Each engineer still runs `/neo-tax:login` once per machine. Nothing is sent from a machine that hasn't enrolled.

[`deploy/claude-code/README.md`](deploy/claude-code/README.md) covers where to put managed settings (the Claude admin console or device management), what engineers see, and what to check before rolling out.

### Optional: Claude Code's own telemetry

Claude Code can export its own usage telemetry over OpenTelemetry. If you want Neo.Tax to receive it, [`deploy/claude-code/managed-settings.json`](deploy/claude-code/managed-settings.json) has the settings to add, with your organization's ingest key from Neo.Tax.

This is separate from the plugin and goes straight from Claude Code to Neo.Tax. Two things to know:
- Claude Code's prompt and tool-detail logging stays off, and Neo.Tax's server discards everything except the attributes listed in [PRIVACY.md](PRIVACY.md#claude-codes-own-telemetry) before storing anything.
- Unlike the plugin's events, this telemetry carries the email of the engineer's Claude account on every record.

## Uninstalling

From inside Claude Code:

```
/plugin uninstall neo-tax@neo-tax-plugins
```

Then delete `~/.claude/plugins/data/neo-tax-neo-tax-plugins/` to remove the device key and any unsent events. For a fleet install, remove the `enabledPlugins` entry from managed settings.

## Releases

Each release is a tagged GitHub release with binaries for macOS (Apple silicon and Intel), Linux (x86-64 and ARM64), and Windows (x86-64), plus SHA-256 checksums. Installs only move to a new version when the marketplace in this repository is updated to point at it.

## Coming soon

- **Codex support.** The same kind of session metadata, reported from OpenAI's Codex.

## Questions

For questions about the plugin, email [support@neo.tax](mailto:support@neo.tax).

To report a security issue, email [security@neo.tax](mailto:security@neo.tax) rather than opening a public issue. See [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
