#!/bin/sh
# Local dev stack: the neo-tax plugin from this working tree, installed into
# Claude Code and reporting to the fake ingest server instead of Neo.Tax.
#
#   scripts/dev.sh up [--my-config [--force]] [--audit]
#       Build the agent, start the fake ingest server, install the plugin, and
#       enroll. By default Claude Code runs from a throwaway config directory
#       (.dev/claude), so your own sessions and plugins are untouched and you
#       log in to Claude once, the first time. --my-config installs into your
#       real ~/.claude instead, and enrolling backfills your past sessions to
#       the fake server. It replaces any neo-tax install there, and `down`
#       deletes its enrollment and queue, so it refuses an existing one
#       unless --force. --audit also starts a proxy that logs the usage the
#       API returns for every request, for `audit`.
#   scripts/dev.sh claude [args...]   Run Claude Code wired to the stack.
#   scripts/dev.sh events [-f]        Show the events the fake server accepted.
#   scripts/dev.sh audit              Compare the proxy's usage to the events.
#   scripts/dev.sh status
#   scripts/dev.sh down [--clean]     Stop the servers and uninstall the
#       plugin; --clean also deletes .dev (and the throwaway Claude login).
#
# Needs go, jq, and claude. Ports: NEO_TAX_DEV_PORT (8787) and
# NEO_TAX_AUDIT_PORT (8788).
set -eu

repo=$(CDPATH="" cd -- "$(dirname -- "$0")/.." && pwd)
dev="$repo/.dev"
# Ports `up` chose are kept, so later commands need no environment.
port=${NEO_TAX_DEV_PORT:-$(cat "$dev/port" 2>/dev/null || echo 8787)}
audit_port=${NEO_TAX_AUDIT_PORT:-$(cat "$dev/audit-port" 2>/dev/null || echo 8788)}

die() { echo "dev: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

running() { [ -f "$dev/$1.pid" ] && kill -0 "$(cat "$dev/$1.pid")" 2>/dev/null; }

# The config directory `up` chose; empty means the engineer's own.
config_dir() { cat "$dev/config-dir" 2>/dev/null || true; }

with_config() {
  c=$(config_dir)
  if [ -n "$c" ]; then CLAUDE_CONFIG_DIR="$c" "$@"; else "$@"; fi
}

# Whether a stack from `up --my-config` is up: config-dir exists and is empty.
my_config_up() { [ -f "$dev/config-dir" ] && [ ! -s "$dev/config-dir" ] && [ -f "$dev/my-config-dir" ]; }

# After `up --my-config`, every command must act on the directory it checked
# and installed into, never one a different CLAUDE_CONFIG_DIR resolves to.
check_my_config() {
  my_config_up || return 0
  mine=$(cat "$dev/my-config-dir" 2>/dev/null || true)
  if [ -n "$mine" ] && [ "$mine" != "${CLAUDE_CONFIG_DIR:-$HOME/.claude}" ]; then
    die "up --my-config used $mine; run this with the same CLAUDE_CONFIG_DIR"
  fi
}

port_free() {
  if curl -s -o /dev/null "http://127.0.0.1:$1/" 2>/dev/null; then
    die "port $1 is already in use; stop what is using it or set $2"
  fi
}

wait_http() {
  for _ in $(seq 1 50); do
    curl -fs -o /dev/null "$1" 2>/dev/null && return 0
    sleep 0.1
  done
  return 1
}

# Whether the engineer's own config has neo-tax, as Claude Code records it.
neo_tax_installed() {
  p="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/plugins"
  [ -e "$p/data/neo-tax-neo-tax" ] \
    || jq -e '.plugins // {} | has("neo-tax@neo-tax")' "$p/installed_plugins.json" >/dev/null 2>&1 \
    || jq -e 'has("neo-tax")' "$p/known_marketplaces.json" >/dev/null 2>&1
}

up() {
  my_config=0 audit=0 force=0
  for a in "$@"; do
    case "$a" in
      --my-config) my_config=1 ;;
      --force) force=1 ;;
      --audit) audit=1 ;;
      *) die "unknown flag $a" ;;
    esac
  done
  need go; need jq; need claude
  mkdir -p "$dev/bin"
  echo "$port" >"$dev/port"
  echo "$audit_port" >"$dev/audit-port"
  if [ -f "$dev/config-dir" ]; then
    was_mine=0
    [ -s "$dev/config-dir" ] || was_mine=1
    [ "$was_mine" = "$my_config" ] || die "the stack is up in the other mode; run scripts/dev.sh down first"
  fi
  if [ "$my_config" = 1 ]; then
    # A --my-config stack already up (checked to match) means the install
    # there is its own.
    if [ "$force" = 0 ] && ! my_config_up && neo_tax_installed; then
      die "neo-tax is already installed in ${CLAUDE_CONFIG_DIR:-~/.claude}; --my-config would replace it and down would delete its enrollment and queued events. Pass --force to proceed."
    fi
  fi

  echo "dev: building"
  (cd "$repo" && go build -o "$dev/bin/neo-tax-agent" ./cmd/neo-tax-agent \
    && go build -o "$dev/bin/fake-ingest" ./cmd/fake-ingest \
    && go build -o "$dev/bin/usage-audit" ./cmd/usage-audit)

  if ! running fake-ingest; then
    port_free "$port" NEO_TAX_DEV_PORT
    "$dev/bin/fake-ingest" -addr "127.0.0.1:$port" -record "$dev/events.jsonl" >"$dev/fake-ingest.log" 2>&1 &
    echo $! >"$dev/fake-ingest.pid"
    if ! wait_http "http://127.0.0.1:$port/_fake/events" || ! running fake-ingest; then
      die "fake ingest did not start; see $dev/fake-ingest.log"
    fi
  fi
  echo "dev: fake ingest on http://127.0.0.1:$port"

  if [ "$audit" = 1 ] && ! running usage-audit; then
    port_free "$audit_port" NEO_TAX_AUDIT_PORT
    "$dev/bin/usage-audit" proxy -addr "127.0.0.1:$audit_port" -log "$dev/usage.jsonl" >"$dev/usage-audit.log" 2>&1 &
    echo $! >"$dev/usage-audit.pid"
    sleep 0.3
    running usage-audit || die "usage proxy did not start; see $dev/usage-audit.log"
    echo "dev: usage proxy on http://127.0.0.1:$audit_port"
  fi

  # A marketplace whose plugin source is this working tree, not a tag.
  market="$dev/marketplace"
  rm -rf "$market"
  mkdir -p "$market/.claude-plugin"
  cp -R "$repo/plugins" "$market/"
  jq '.plugins[0].source = "./plugins/claude-code"' "$repo/.claude-plugin/marketplace.json" >"$market/.claude-plugin/marketplace.json"
  # Recorded only now, so a run that fails earlier leaves no claim on an
  # install it never made.
  if [ "$my_config" = 1 ]; then
    : >"$dev/config-dir"
    echo "${CLAUDE_CONFIG_DIR:-$HOME/.claude}" >"$dev/my-config-dir"
  else
    rm -f "$dev/my-config-dir"
    mkdir -p "$dev/claude"
    echo "$dev/claude" >"$dev/config-dir"
  fi
  with_config claude plugin marketplace remove neo-tax >/dev/null 2>&1 || true
  with_config claude plugin marketplace add "$market" >/dev/null
  with_config claude plugin install neo-tax@neo-tax >/dev/null
  echo "dev: plugin installed from this working tree"

  email=$(git -C "$repo" config user.email 2>/dev/null || echo dev@example.com)
  with_config env NEO_TAX_AGENT_BIN="$dev/bin/neo-tax-agent" \
    "$market/plugins/claude-code/bin/neo-tax-agent" login \
    --server "http://127.0.0.1:$port" --token dev --email "$email"

  echo
  if [ "$my_config" = 0 ]; then
    echo "Next: scripts/dev.sh claude   (log in to Claude the first time)"
  else
    echo "Next: scripts/dev.sh claude"
  fi
  echo "Then: scripts/dev.sh events -f   (in another terminal)"
}

run_claude() {
  [ -f "$dev/config-dir" ] || die "run scripts/dev.sh up first"
  running fake-ingest || die "the fake ingest server is not running; run scripts/dev.sh up"
  export NEO_TAX_AGENT_BIN="$dev/bin/neo-tax-agent" NEO_TAX_SERVER="http://127.0.0.1:$port"
  c=$(config_dir)
  [ -n "$c" ] && export CLAUDE_CONFIG_DIR="$c"
  if running usage-audit; then
    export ANTHROPIC_BASE_URL="http://127.0.0.1:$audit_port"
  fi
  exec claude "$@"
}

events() {
  f="$dev/events.jsonl"
  [ -f "$f" ] || die "no events yet"
  summary='[.event_kind, .session_id[0:8], (.model // ""), (if .usage then "in=\(.usage.input) out=\(.usage.output) cache_read=\(.usage.cache_read) cache_write=\(.usage.cache_write_5m + .usage.cache_write_1h)" else "" end)] | @tsv'
  if [ "${1:-}" = -f ]; then
    tail -n +1 -f "$f" | jq -r --unbuffered "$summary"
  else
    jq -r "$summary" "$f"
    echo
    jq -s -r 'group_by(.event_kind) | map("\(.[0].event_kind): \(length)") | .[]' "$f"
  fi
}

audit() {
  [ -f "$dev/usage.jsonl" ] || die "no usage logged; run scripts/dev.sh up --audit, then scripts/dev.sh claude"
  "$dev/bin/usage-audit" compare -log "$dev/usage.jsonl" -events "$dev/events.jsonl" "$@"
}

status() {
  for p in fake-ingest usage-audit; do
    if running "$p"; then echo "$p: running (pid $(cat "$dev/$p.pid"))"; else echo "$p: stopped"; fi
  done
  c=$(config_dir)
  echo "config: ${c:-your ~/.claude}"
  [ -f "$dev/events.jsonl" ] && echo "events: $(wc -l <"$dev/events.jsonl" | tr -d ' ')"
  return 0
}

down() {
  for p in fake-ingest usage-audit; do
    if running "$p"; then kill "$(cat "$dev/$p.pid")" 2>/dev/null || true; fi
    rm -f "$dev/$p.pid"
  done
  if [ -f "$dev/config-dir" ]; then
    with_config claude plugin uninstall neo-tax@neo-tax >/dev/null 2>&1 || true
    with_config claude plugin marketplace remove neo-tax >/dev/null 2>&1 || true
    c=$(config_dir)
    rm -rf "${c:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}/plugins/data/neo-tax-neo-tax"
  fi
  if [ "${1:-}" = --clean ]; then
    rm -rf "$dev"
  else
    rm -f "$dev/config-dir" "$dev/my-config-dir"
  fi
  echo "dev: stopped and uninstalled"
}

cmd=${1:-}
[ $# -gt 0 ] && shift
case "$cmd" in
  up | claude | down) check_my_config ;;
esac
case "$cmd" in
  up) up "$@" ;;
  claude) run_claude "$@" ;;
  events) events "$@" ;;
  audit) audit "$@" ;;
  status) status ;;
  down) down "$@" ;;
  *) sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
