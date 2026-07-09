#!/bin/bash
# start.sh — llmRx gateway lifecycle
#
# Usage:
#   ./start.sh                  # foreground (default; current behavior)
#   ./start.sh start            # same as above
#   ./start.sh start -d         # daemon: nohup + pidfile + log file
#   ./start.sh stop             # SIGTERM via pidfile (SIGKILL after 5s)
#   ./start.sh restart          # stop + start -d
#   ./start.sh status           # pid + /health probe
#   ./start.sh logs             # tail -F data/llmRx.log
#   ./start.sh wipe-keys        # clear encrypted keys after master-key mismatch
#   ./start.sh help
#
# Master key resolution (in order):
#   1. $LLMRX_KEY_MASTER (env)
#   2. data/llmrx.key (persisted; mode 0600)
#   3. freshly generated + saved to data/llmrx.key (dev only)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

BIN="$ROOT/llmRx"
PIDFILE="$ROOT/data/llmRx.pid"
LOGFILE="$ROOT/data/llmRx.log"
KEY_FILE="$ROOT/data/llmrx.key"
CONFIG="${LLMRX_CONFIG:-$ROOT/config.yml}"

resolve_master_key() {
  if [ -n "${LLMRX_KEY_MASTER:-}" ]; then
    return 0
  fi
  if [ -f "$KEY_FILE" ]; then
    export LLMRX_KEY_MASTER="$(cat "$KEY_FILE")"
    echo "[start] LLMRX_KEY_MASTER loaded from $KEY_FILE"
    return 0
  fi
  export LLMRX_KEY_MASTER="$(openssl rand -hex 32)"
  mkdir -p "$(dirname "$KEY_FILE")"
  printf '%s' "$LLMRX_KEY_MASTER" > "$KEY_FILE"
  chmod 600 "$KEY_FILE"
  echo "[start] LLMRX_KEY_MASTER not set — generated and saved to $KEY_FILE"
}

ensure_binary() {
  if [ ! -x "$BIN" ] || [ -n "${REBUILD:-}" ]; then
    echo "[start] building $BIN..."
    (cd "$ROOT" && go build -o "$BIN" ./cmd/gateway)
  fi
}

# Echo the live PID (or empty) without printing anything else.
live_pid() {
  if [ -f "$PIDFILE" ]; then
    local pid; pid="$(cat "$PIDFILE" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      echo "$pid"
      return 0
    fi
    rm -f "$PIDFILE"
  fi
  return 1
}

cmd_start() {
  local daemon=0
  for arg in "$@"; do
    case "$arg" in
      -d|--daemon) daemon=1 ;;
    esac
  done
  resolve_master_key
  ensure_binary
  if live_pid >/dev/null 2>&1; then
    echo "[start] already running (pid $(cat "$PIDFILE"))"
    return 0
  fi
  if [ "$daemon" = "1" ]; then
    mkdir -p "$(dirname "$LOGFILE")" "$(dirname "$PIDFILE")"
    nohup "$BIN" -config "$CONFIG" >"$LOGFILE" 2>&1 &
    echo $! > "$PIDFILE"
    echo "[start] daemonized — pid $(cat "$PIDFILE")  log $LOGFILE"
    echo "[start] admin → http://localhost:8787/admin/  (default: admin / admin)"
  else
    echo "[start] listening on :8787  admin → http://localhost:8787/admin/"
    exec "$BIN" -config "$CONFIG"
  fi
}

cmd_stop() {
  local pid; pid="$(live_pid || true)"
  if [ -z "$pid" ]; then
    echo "[stop] not running"
    return 0
  fi
  echo "[stop] sending SIGTERM to $pid..."
  kill -TERM "$pid" 2>/dev/null || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    kill -0 "$pid" 2>/dev/null || { rm -f "$PIDFILE"; echo "[stop] stopped"; return 0; }
    sleep 0.5
  done
  echo "[stop] still alive; sending SIGKILL"
  kill -KILL "$pid" 2>/dev/null || true
  rm -f "$PIDFILE"
}

cmd_status() {
  local pid; pid="$(live_pid || true)"
  if [ -z "$pid" ]; then
    echo "[status] not running"
    return 3
  fi
  echo "[status] running (pid $pid)"
  if command -v curl >/dev/null 2>&1; then
    if curl -fsS --max-time 2 http://127.0.0.1:8787/health >/dev/null 2>&1; then
      echo "[status] /health OK"
    else
      echo "[status] /health probe failed (port open but unhealthy, or wrong port)"
    fi
  else
    echo "[status] (install curl to probe /health)"
  fi
}

cmd_logs() {
  if [ ! -f "$LOGFILE" ]; then
    echo "[logs] $LOGFILE not found — start with './start.sh start -d' to enable log file"
    return 1
  fi
  tail -F "$LOGFILE"
}

cmd_wipe_keys() {
  ensure_binary
  resolve_master_key
  "$BIN" -wipe-keys -config "$CONFIG"
}

cmd_help() {
  sed -n '2,20p' "$0"
}

case "${1:-start}" in
  start)      shift; cmd_start "$@" ;;
  stop)       cmd_stop ;;
  restart)    cmd_stop; cmd_start -d ;;
  status)     cmd_status ;;
  logs)       cmd_logs ;;
  wipe-keys)  cmd_wipe_keys ;;
  help|-h|--help) cmd_help ;;
  *)
    echo "unknown command: $1" >&2
    cmd_help
    exit 2
    ;;
esac
