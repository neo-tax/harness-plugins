#!/bin/sh
# Install smoke test. Installs the neo-tax plugin from this repository into a
# throwaway Claude Code configuration directory, enrolls it against the fake
# ingest server, runs one non-interactive prompt, and asserts that one
# session.opened, at least one session.turn, and one session.closed arrived.
# It is the only test that goes through Claude Code's own hook dispatch.
#
# Needs go, git, jq, claude, and ANTHROPIC_API_KEY. SMOKE_SKIP_PROMPT=1 stops
# after install and enrollment (no API call).
#
# @spec: test-and-release#install-smoke-test
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
fake_pid=""
cleanup() {
  [ -n "$fake_pid" ] && kill "$fake_pid" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

cd "$repo"
go build -o "$tmp/neo-tax-agent" ./cmd/neo-tax-agent
go build -o "$tmp/fake-ingest" ./cmd/fake-ingest

port=${SMOKE_PORT:-18787}
"$tmp/fake-ingest" -addr "127.0.0.1:$port" -record "$tmp/events.jsonl" 2>/dev/null &
fake_pid=$!
for _ in $(seq 1 50); do
  curl -fs "http://127.0.0.1:$port/_fake/events" >/dev/null 2>&1 && break
  sleep 0.1
done

export CLAUDE_CONFIG_DIR="$tmp/claude"
export NEO_TAX_AGENT_BIN="$tmp/neo-tax-agent"
mkdir -p "$CLAUDE_CONFIG_DIR"

# A copy of the marketplace whose plugin source is this working tree rather
# than the released tag.
market="$tmp/marketplace"
mkdir -p "$market/.claude-plugin"
cp -R "$repo/plugins" "$market/"
jq '.plugins[0].source = "./plugins/claude-code"' "$repo/.claude-plugin/marketplace.json" >"$market/.claude-plugin/marketplace.json"

claude plugin marketplace add "$market"
claude plugin install neo-tax@neo-tax

# Enroll the way the login skill does: through the plugin's launcher with no
# data directory given, so the launcher's fallback must match the directory
# Claude Code hands the hooks.
"$market/plugins/claude-code/bin/neo-tax-agent" login \
  --server "http://127.0.0.1:$port" --token smoke --email smoke@example.com
data="$CLAUDE_CONFIG_DIR/plugins/data/neo-tax-neo-tax"
test -f "$data/device.json" || { echo "smoke: no device.json in $data" >&2; exit 1; }

# Let login's background backfill (of an empty history) finish first.
for _ in $(seq 1 100); do
  jq -e '.finished_at' "$data/backfill.json" >/dev/null 2>&1 && break
  sleep 0.1
done

if [ "${SMOKE_SKIP_PROMPT:-}" = 1 ]; then
  echo "smoke: installed and enrolled; prompt skipped"
  exit 0
fi

work="$tmp/work"
mkdir -p "$work"
git -C "$work" init -q -b main
git -C "$work" -c user.name=smoke -c user.email=smoke@example.com -c commit.gpgsign=false commit -q --allow-empty -m init
(cd "$work" && claude -p "Reply with the single word: ok" --max-turns 1 >/dev/null)

# Hooks run asynchronously; wait for them to deliver.
count() { jq -s --arg k "$1" '[.[] | select(.event_kind == $k)] | length' "$tmp/events.jsonl" 2>/dev/null || echo 0; }
for _ in $(seq 1 120); do
  [ "$(count session.opened)" -ge 1 ] && [ "$(count session.turn)" -ge 1 ] && [ "$(count session.closed)" -ge 1 ] && break
  sleep 0.5
done
opened=$(count session.opened); turns=$(count session.turn); closed=$(count session.closed)
echo "smoke: opened=$opened turn=$turns closed=$closed"
[ "$opened" -eq 1 ] && [ "$turns" -ge 1 ] && [ "$closed" -eq 1 ]
