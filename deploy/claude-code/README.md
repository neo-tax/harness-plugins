# Deploying to your organization with managed settings

Claude Code's managed settings can install the `neo-tax` plugin for every engineer. They can also turn on Claude Code's own telemetry export to Neo.Tax. This page covers what to put in them, where to put it, and what engineers will see.

## What to deploy

The plugin and the telemetry are independent. Deploy either or both.

```json
{
  "extraKnownMarketplaces": {
    "neo-tax-plugins": {
      "autoUpdate": true,
      "source": { "source": "github", "repo": "neo-tax/harness-plugins" }
    }
  },
  "enabledPlugins": { "neo-tax@neo-tax-plugins": true },
  "cleanupPeriodDays": 365,
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "https://api.neo.tax/otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json",
    "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
    "OTEL_METRICS_INCLUDE_REPOSITORY": "true",
    "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer REPLACE_WITH_INGEST_KEY"
  }
}
```

- **The plugin** is the `extraKnownMarketplaces` and `enabledPlugins` keys. Keep `autoUpdate: true`: Claude Code doesn't update third-party marketplaces by default, so without it each machine stays on the version it first installed. Updates move only when Neo.Tax publishes a release, since the marketplace pins each one to a tagged version.
- **`cleanupPeriodDays` is optional.** Claude Code deletes local session transcripts older than this many days (30 by default). The plugin's backfill reads those transcripts to report sessions from before a machine enrolled, so a longer window gives it more history. It doesn't affect sessions reported after enrollment, and it can't bring back transcripts already deleted. Transcripts hold prompts and code in plain text on each engineer's machine, so keeping them for a year is a security decision for your team. Leave the line out to keep Claude Code's default.
- **Telemetry** is the `env` block, which is also in [`managed-settings.json`](managed-settings.json). Replace `REPLACE_WITH_INGEST_KEY` with the ingest key Neo.Tax gives your organization. Keep the other values exactly as shown: Neo.Tax's endpoint expects OTLP/JSON and delta metrics.
- **Content logging stays off.** Leave out `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT`, and `OTEL_LOG_RAW_API_BODIES`. Claude Code then redacts prompts, responses, and tool details before anything leaves the machine. [PRIVACY.md](../../PRIVACY.md#claude-codes-own-telemetry) lists what Neo.Tax keeps.

Managed settings are strict JSON, so they can't carry comments. If your organization already has managed settings, merge these keys into them rather than replacing them.

## Where to put it

### Option 1: the Claude admin console (Team and Enterprise)

In claude.ai, go to **Organization settings > Claude Code > Managed settings** ([claude.ai/admin-settings/claude-code](https://claude.ai/admin-settings/claude-code)). Only an Owner or Primary Owner can edit them.

Claude Code fetches these server-managed settings at startup and then hourly, on laptops and in cloud sessions. No device management is needed.

Server-managed settings are skipped for an engineer who:
- uses Bedrock, Vertex, or Foundry;
- sets a non-default `ANTHROPIC_BASE_URL`, for example to route through a gateway like OpenRouter; or
- authenticates with an API key from another organization or an `apiKeyHelper`.

Those engineers need Option 2. `/status` and `claude doctor` show whether remote managed settings applied, and why not if they didn't.

### Option 2: device management (MDM) or a file on each machine

Deploy the same JSON to each machine as one of:

| Platform | Location |
|----------|----------|
| macOS | `/Library/Application Support/ClaudeCode/managed-settings.json`, or the `com.anthropic.claudecode` managed preferences domain via MDM |
| Linux and WSL | `/etc/claude-code/managed-settings.json` |
| Windows | `C:\Program Files\ClaudeCode\managed-settings.json`, or the `Settings` value (JSON string) under `HKLM\SOFTWARE\Policies\ClaudeCode` |

You can also put these keys in their own file in a `managed-settings.d/` directory next to `managed-settings.json`. That keeps them separate from your other policy.

Engineers can't override settings delivered this way. Server-managed settings are a client-side control that an engineer on an unmanaged machine can get around, so MDM is the stronger guarantee if that matters to you.

### If you use both

By default, Claude Code applies only the highest-ranked admin source that sets any policy key. The admin console ranks above MDM, which ranks above `managed-settings.json`. The sources aren't merged. So if the console has any settings, `enabledPlugins` in a file on disk is ignored. The `env` block is the exception: it merges variable by variable across sources.

Put the plugin keys in the source that already wins for your organization. Alternatively, set `"managedSourcesBehavior": "merge"` in that source to apply all of them.

## Cowork

The settings above cover Claude Code only. Cowork exports its own telemetry, which you configure separately in the Claude admin console. Go to **Monitoring**, under Data and privacy in admin settings, and fill in the Cowork form (it covers Cowork, Cowork in the cloud, and unified chat and Cowork):

| Field | Value |
|-------|-------|
| OTLP endpoint | `https://api.neo.tax/otlp` |
| OTLP protocol | `http/json` |
| OTLP headers | `Authorization=Bearer REPLACE_WITH_INGEST_KEY` |
| Resource attributes | leave empty |

Cowork uses Claude Code's telemetry schema, so it uses the same ingest key and endpoint. The console adds the endpoint's domain to Cowork's network egress allowlist automatically. Export needs Claude desktop 1.1.4173 or later.

Like Claude Code's telemetry, Cowork's carries the email of the engineer's Claude account. The plugin and `/neo-tax:login` aren't involved: nothing needs to be installed or enrolled on engineers' machines for it.

Leave the **Office Agents** form empty for now. Neo.Tax doesn't accept its telemetry yet.

## What engineers see

**A one-time approval prompt.** Because the telemetry settings set `OTEL_EXPORTER_OTLP_ENDPOINT`, each engineer sees a security dialog in their next interactive session explaining what's being configured. They must approve it to continue. Rejecting it exits Claude Code. The prompt appears again whenever those settings change.

**Telemetry starts at the next launch.** Most managed settings reach running sessions within the hour, but exporter changes apply only when Claude Code next starts.

**Enrollment is still manual.** The plugin installs everywhere, but each engineer runs `/neo-tax:login` once per machine with the enrollment token you circulate. Nothing is sent from a machine that hasn't enrolled.

## Things to check before deploying

- **`strictKnownMarketplaces`.** If you already restrict marketplaces with this key, add `{ "source": "github", "repo": "neo-tax/harness-plugins" }` to your list. If you don't use it, don't start: a list containing only our entry blocks every other marketplace, including Anthropic's.
- **An existing telemetry exporter.** Claude Code exports to one endpoint. If you already send its telemetry somewhere (Datadog or your own collector), this `env` block would redirect it to Neo.Tax. Leave the block out and have your OpenTelemetry Collector forward a copy to Neo.Tax instead. Contact [support@neo.tax](mailto:support@neo.tax) to set that up.
- **The ingest key lives on every machine.** Server-managed settings are cached on disk in `~/.claude/remote-settings.json`. The ingest key can only write telemetry to your organization in Neo.Tax and can be revoked, but treat it as visible to every engineer. A future version replaces it with a credential issued per device.

## Removing it

Delete the `enabledPlugins` entry to uninstall the plugin, and the `env` block to stop telemetry. Engineers can then delete `~/.claude/plugins/data/neo-tax-neo-tax-plugins/` to remove the device key and any unsent events.

Questions: [support@neo.tax](mailto:support@neo.tax).
