#!/bin/sh
# Install smoke test. Installs the neo-tax plugin into a throwaway Claude Code
# configuration directory, enrolls it against the fake ingest server, runs one
# non-interactive prompt, and asserts that one session.opened, at least one
# session.turn, and one session.closed arrived. It is the only test that goes
# through Claude Code's own hook dispatch.
#
# Two modes:
#   default              the plugin comes from this working tree and the hooks
#                        run an agent built from it (pull requests).
#   SMOKE_RELEASE_TAG=vX.Y.Z
#                        the plugin comes from GitHub at that tag through the
#                        tagged commit's marketplace, already pinned to the
#                        tag, and its own git-subdir source, and the launcher
#                        downloads and verifies the binary attached to the
#                        release, the way a fleet install does (release
#                        workflow, after publish).
#
# Needs go, git, jq, curl, claude, and ANTHROPIC_API_KEY. SMOKE_SKIP_PROMPT=1
# stops after install and enrollment (no API call).
#
# @spec: test-and-release#install-smoke-test
# @spec: test-and-release#fleet-install-before-rollout
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
fake_pid=""
cleanup() {
  [ -n "$fake_pid" ] && kill "$fake_pid" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT
fail() {
  echo "smoke: $*" >&2
  [ -s "$tmp/fake-ingest.log" ] && sed 's/^/fake-ingest: /' "$tmp/fake-ingest.log" >&2
  exit 1
}

tag=${SMOKE_RELEASE_TAG:-}

cd "$repo"
go build -o "$tmp/fake-ingest" ./cmd/fake-ingest

port=${SMOKE_PORT:-18787}
"$tmp/fake-ingest" -addr "127.0.0.1:$port" -record "$tmp/events.jsonl" 2>"$tmp/fake-ingest.log" &
fake_pid=$!
ready=""
for _ in $(seq 1 50); do
  curl -fs "http://127.0.0.1:$port/_fake/events" >/dev/null 2>&1 && { ready=1; break; }
  sleep 0.1
done
[ -n "$ready" ] || fail "fake ingest did not answer on port $port"

export CLAUDE_CONFIG_DIR="$tmp/claude"
mkdir -p "$CLAUDE_CONFIG_DIR"
unset NEO_TAX_AGENT_BIN

market="$tmp/marketplace"
mkdir -p "$market/.claude-plugin"
if [ -n "$tag" ]; then
  # The marketplace exactly as main will carry it once the release is
  # rolled out: the tagged commit's own, pinned to this tag.
  [ "$(jq -r '.plugins[0].source.ref' "$repo/.claude-plugin/marketplace.json")" = "$tag" ] ||
    fail "the checked-out marketplace is not pinned to $tag"
  cp "$repo/.claude-plugin/marketplace.json" "$market/.claude-plugin/marketplace.json"
else
  # The plugin source is this working tree, and the hooks run an agent built
  # from it.
  go build -o "$tmp/neo-tax-agent" ./cmd/neo-tax-agent
  export NEO_TAX_AGENT_BIN="$tmp/neo-tax-agent"
  cp -R "$repo/plugins" "$market/"
  jq '.plugins[0].source = "./plugins/claude-code"' "$repo/.claude-plugin/marketplace.json" >"$market/.claude-plugin/marketplace.json"
fi

claude plugin marketplace add "$market"
claude plugin install neo-tax@neo-tax-plugins

# Enroll the way the login skill does: through the installed plugin's launcher
# with no data directory given, so the launcher's fallback must match the
# directory Claude Code hands the hooks.
launcher=$(find "$CLAUDE_CONFIG_DIR/plugins" -path '*/bin/neo-tax-agent' -type f 2>/dev/null | head -n 1)
if [ -z "$launcher" ] && [ -z "$tag" ]; then
  launcher="$market/plugins/claude-code/bin/neo-tax-agent"
fi
[ -n "$launcher" ] || fail "the installed plugin has no bin/neo-tax-agent"
echo smoke | "$launcher" login --server "http://127.0.0.1:$port" --token-stdin --email smoke@example.com
data="$CLAUDE_CONFIG_DIR/plugins/data/neo-tax-neo-tax-plugins"
if [ -n "$tag" ]; then
  # The launcher exits 0 when a download fails, so a missing binary would
  # otherwise only show up as a missing device.json.
  test -x "$data/bin/neo-tax-agent-$tag" || fail "the launcher did not download and verify the $tag binary"
fi
test -f "$data/device.json" || fail "no device.json in $data"

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
[ "$opened" -eq 1 ] && [ "$turns" -ge 1 ] && [ "$closed" -eq 1 ] || fail "expected one opened, at least one turn, and one closed"
