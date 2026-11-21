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
#   scripts/dev.sh otlp [-f]          Show the telemetry Claude Code exported
#       to the fake server's OTLP endpoints (NEO_TAX_DEV_OTLP=0 turns it off).
#   scripts/dev.sh capture            Run a new session, a resume, a fork, and
#       an interactive /clear, and check the plugin and telemetry agree on
#       each session and request id. Needs expect; costs a few cheap prompts.
#   scripts/dev.sh audit              Compare the proxy's usage to the events.
#   scripts/dev.sh status
#   scripts/dev.sh down [--clean]     Stop the servers and uninstall the
#       plugin; --clean also deletes .dev (and the throwaway Claude login).
#
# Needs go, jq, curl, and claude (and expect, for capture). Ports: NEO_TAX_DEV_PORT (8787) and
# NEO_TAX_AUDIT_PORT (8788).
set -eu

# Everything written under .dev is private: recorded telemetry carries the
# engineer's email and account ids. Claude Code itself runs with the
# engineer's own umask, so files it writes in their work tree are unaffected.
# @spec: claude-code-telemetry-export#dev-stack-private
user_umask=$(umask)
umask 077

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
  [ -e "$p/data/neo-tax-neo-tax-plugins" ] \
    || jq -e '.plugins // {} | has("neo-tax@neo-tax-plugins")' "$p/installed_plugins.json" >/dev/null 2>&1 \
    || jq -e 'has("neo-tax-plugins")' "$p/known_marketplaces.json" >/dev/null 2>&1
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
  need go; need jq; need curl; need claude
  mkdir -p "$dev/bin"
  chmod 700 "$dev"
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
    "$dev/bin/fake-ingest" -addr "127.0.0.1:$port" -record "$dev/events.jsonl" -record-duplicates "$dev/duplicates.jsonl" -record-otlp "$dev/otlp.jsonl" -ingest-key dev-ingest >"$dev/fake-ingest.log" 2>&1 &
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
  with_config claude plugin marketplace remove neo-tax-plugins >/dev/null 2>&1 || true
  with_config claude plugin marketplace add "$market" >/dev/null
  with_config claude plugin install neo-tax@neo-tax-plugins >/dev/null
  echo "dev: plugin installed from this working tree"

  email=$(git -C "$repo" config user.email 2>/dev/null || echo dev@example.com)
  echo dev | with_config env NEO_TAX_AGENT_BIN="$dev/bin/neo-tax-agent" \
    "$market/plugins/claude-code/bin/neo-tax-agent" login \
    --server "http://127.0.0.1:$port" --token-stdin --email "$email"

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
  # Run from inside another Claude Code session, Claude Code would inherit
  # that session's identity and stop saving transcripts, so the plugin would
  # see no turns.
  unset CLAUDE_CODE_CHILD_SESSION CLAUDE_CODE_SESSION_ID CLAUDE_CODE_SESSION_ATTENDED \
    CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_EXECPATH
  if running usage-audit; then
    export ANTHROPIC_BASE_URL="http://127.0.0.1:$audit_port"
  fi
  # Claude Code's own telemetry, to the fake server's OTLP endpoints. Any
  # telemetry settings inherited from the shell are cleared first: a
  # per-signal endpoint would send this session's telemetry to a real
  # collector, and a content switch such as OTEL_LOG_USER_PROMPTS would have
  # the fake write prompts to .dev/otlp.jsonl.
  # @spec: claude-code-telemetry-export#dev-stack-exports-telemetry
  # @spec: claude-code-telemetry-export#dev-stack-clears-inherited-telemetry
  for v in $(env | sed -n 's/^\(OTEL_[A-Za-z0-9_]*\)=.*/\1/p; s/^\(CLAUDE_CODE_OTEL_[A-Za-z0-9_]*\)=.*/\1/p'); do
    unset "$v"
  done
  unset CLAUDE_CODE_ENABLE_TELEMETRY
  for m in "/Library/Application Support/ClaudeCode/managed-settings.json" /etc/claude-code/managed-settings.json; do
    if [ -f "$m" ] && grep -q 'OTEL_\|ENABLE_TELEMETRY' "$m"; then
      echo "dev: warning: $m sets telemetry, which overrides this session's; its telemetry may go to that collector" >&2
    fi
  done
  if [ "${NEO_TAX_DEV_OTLP:-1}" != 0 ]; then
    export CLAUDE_CODE_ENABLE_TELEMETRY=1 OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=otlp \
      OTEL_EXPORTER_OTLP_PROTOCOL=http/json OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:$port/otlp" \
      OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta OTEL_METRICS_INCLUDE_REPOSITORY=true \
      OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer dev-ingest" \
      OTEL_LOGS_EXPORT_INTERVAL=1000 OTEL_METRIC_EXPORT_INTERVAL=5000
  fi
  umask "$user_umask"
  exec claude "$@"
}

otlp() {
  f="$dev/otlp.jsonl"
  [ -f "$f" ] || die "no telemetry yet"
  summary='[.signal, (.event_name // .name), (.attributes["session.id"] // "-"), (if .signal == "metric" then (.value | tostring) else (.attributes.request_id // "") end)] | @tsv'
  if [ "${1:-}" = -f ]; then
    tail -n +1 -f "$f" | jq -r --unbuffered "$summary"
  else
    jq -r "$summary" "$f"
  fi
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

# capture runs Claude Code through the dev stack in the ways that change a
# session's id, then checks both paths against what it saw.
# @spec: claude-code-telemetry-export#dev-stack-capture
capture() {
  need expect
  running fake-ingest || die "the fake ingest server is not running; run scripts/dev.sh up"
  [ "${NEO_TAX_DEV_OTLP:-1}" != 0 ] || die "capture needs telemetry; unset NEO_TAX_DEV_OTLP"
  conf=$(config_dir)
  [ -n "$conf" ] || die "capture runs only against the throwaway config, not --my-config"
  [ -f "$dev/duplicates.jsonl" ] || die "the running fake ingest does not record duplicates; run scripts/dev.sh down, then up"
  chmod 700 "$dev"
  work="$dev/capture"
  mkdir -p "$work"
  [ -d "$work/.git" ] || git -C "$work" init -q
  work=$(cd "$work" && pwd -P)
  # Trust the capture folder up front: answering the trust prompt with keys
  # is unreliable while the interface is still probing the terminal.
  [ -f "$conf/.claude.json" ] || echo '{}' >"$conf/.claude.json"
  jq --arg w "$work" '.projects[$w].hasTrustDialogAccepted = true' "$conf/.claude.json" >"$conf/.claude.json.tmp" \
    && mv "$conf/.claude.json.tmp" "$conf/.claude.json"
  touch "$dev/events.jsonl" "$dev/otlp.jsonl"
  ev_from=$(($(wc -l <"$dev/events.jsonl") + 1))
  dup_from=$(($(wc -l <"$dev/duplicates.jsonl") + 1))
  ot_from=$(($(wc -l <"$dev/otlp.jsonl") + 1))
  self="$repo/scripts/dev.sh"

  sid() { jq -r .session_id; }
  echo "capture: new session"
  a=$(cd "$work" && "$self" claude -p "Reply with just the word one." --output-format json </dev/null | sid)
  echo "capture: resume"
  r=$(cd "$work" && "$self" claude -p "Reply with just the word two." --resume "$a" --output-format json </dev/null | sid)
  echo "capture: fork"
  f=$(cd "$work" && "$self" claude -p "Reply with just the word three." --resume "$a" --fork-session --output-format json </dev/null | sid)

  echo "capture: interactive /clear"
  cat >"$dev/capture.exp" <<EXP
set timeout 60
log_file -noappend {$dev/capture.log}
cd {$work}
spawn sh {$self} claude
# The interface draws spaces as cursor moves, so patterns match single words.
sleep 8
proc ask {text} {
  sleep 3
  send -- \$text
  sleep 1
  send "\\r"
  expect -re {done} {} timeout {}
}
ask "Reply with just the word four."
ask "/clear"
ask "Reply with just the word five."
sleep 3
send -- "/exit"
sleep 1
send "\\r"
expect eof
EXP
  expect "$dev/capture.exp" >/dev/null 2>&1 || die "the interactive session failed; see $dev/capture.log"

  echo "capture: waiting for hooks and telemetry to flush"
  sleep 10
  tail -n +"$ev_from" "$dev/events.jsonl" >"$work/events.jsonl"
  tail -n +"$dup_from" "$dev/duplicates.jsonl" >"$work/duplicates.jsonl"
  tail -n +"$ot_from" "$dev/otlp.jsonl" >"$work/otlp.jsonl"
  jq -n -r --arg a "$a" --arg r "$r" --arg f "$f" --arg work "$work" \
    --slurpfile ev "$work/events.jsonl" --slurpfile dup "$work/duplicates.jsonl" --slurpfile ot "$work/otlp.jsonl" '
    def p(s): $ev | map(select(.session_id == s));
    def calls(s): $ot | map(select(.attributes["event.name"] == "api_request" and .attributes["session.id"] == s));
    def turnreqs(s): p(s) | map(select(.event_kind == "session.turn") | .request_id);
    # A fork re-sends the parent turns under its own id; the server drops
    # them as duplicates, so they land here and not in events.jsonl.
    def dupreqs(s): $dup | map(select(.session_id == s and .event_kind == "session.turn") | .request_id);
    ($ev | map(select(.event_kind == "session.closed" and .reason == "clear")) | .[0].session_id) as $c
    | ($ev | map(select(.event_kind == "session.opened" and .source == "clear")) | .[0].session_id) as $d
    | ($ot | map(.attributes.request_id // empty)) as $otreqs
    | ($ev | map(select(.event_kind == "session.turn") | .request_id)) as $turns
    | [
        ["new session reaches both paths", ($a != "" and (p($a) | length) > 0 and (calls($a) | length) > 0)],
        ["resume keeps the session id", ($r == $a)],
        ["fork gets a new id on both paths", ($f != $a and (p($f) | length) > 0 and (calls($f) | length) > 0)],
        ["fork stores none of its parent turns", ((turnreqs($f) - turnreqs($a)) == turnreqs($f))],
        ["fork re-sends its parent turns as duplicates", ((dupreqs($f) | length) > 0 and (turnreqs($a) - dupreqs($f)) == [])],
        ["/clear closes one session and opens another", ($c != null and $d != null and $c != $d)],
        ["telemetry switches at /clear too", ($c != null and $d != null and (calls($c) | length) > 0 and (calls($d) | length) > 0)],
        ["every plugin turn has a telemetry call", (($turns | length) > 0 and ($turns - $otreqs) == [])]
      ] as $checks
    | ($checks[] | "\(if .[1] then "PASS" else "FAIL" end)  \(.[0])"),
      "",
      "sessions: new \($a[0:8])  fork \($f[0:8])  before /clear \(($c // "-")[0:8])  after /clear \(($d // "-")[0:8])",
      "plugin turns: \($turns | length)  fork duplicates: \(dupreqs($f) | length)  telemetry calls: \($ot | map(select(.attributes["event.name"] == "api_request")) | length)",
      (if all($checks[]; .[1]) then "capture: ok" else "capture: FAILED; inputs in \($work)" end)
    ' | tee "$work/report.txt"
  grep -q '^capture: ok' "$work/report.txt"
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
    with_config claude plugin uninstall neo-tax@neo-tax-plugins >/dev/null 2>&1 || true
    with_config claude plugin marketplace remove neo-tax-plugins >/dev/null 2>&1 || true
    c=$(config_dir)
    rm -rf "${c:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}/plugins/data/neo-tax-neo-tax-plugins"
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
  otlp) otlp "$@" ;;
  audit) audit "$@" ;;
  capture) capture ;;
  status) status ;;
  down) down "$@" ;;
  *) sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
