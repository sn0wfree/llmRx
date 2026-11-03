#!/usr/bin/env bash
# E2E: real Go server + real mock upstream + the Python edge client.
# Exercises enroll -> heartbeat -> signed bundle -> chat proxy -> usage upload.
#
# Usage:  bash clients/python/tests/e2e.sh
# Requires: Go (on PATH), python3 with PyYAML, curl. Runs everything on
# ephemeral localhost ports and cleans up its processes on exit.
set -u
cd "$(cd "$(dirname "$0")/../../.." && pwd)" || exit 1

WORK=$(mktemp -d /tmp/llmrx-e2e.XXXXXX)
LOGS="$WORK/logs"; mkdir -p "$LOGS"
SRV_PORT=18901
MOCK_PORT=18902
CLI_PORT=18903
SRV=http://127.0.0.1:$SRV_PORT

cleanup() {
  [ -n "${CLI_PID:-}" ] && kill "$CLI_PID" 2>/dev/null
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null
}
trap cleanup EXIT

pass=0; fail=0
check() { # check <label> <expected-substr> <actual>
  if printf '%s' "$3" | grep -qF -- "$2"; then
    echo "  PASS  $1"; pass=$((pass+1)); return 0
  fi
  echo "  FAIL  $1"
  echo "        wanted: $2"
  echo "        got:    $(printf '%s' "$3" | head -c 300)"
  fail=$((fail+1)); return 1
}

echo "=== build server + client binaries ==="
export PATH=/home/ll/.local/go/bin:$PATH
go build -o "$WORK/llmRx" ./cmd/gateway 2>"$LOGS/build.log" || { echo "server build failed"; cat "$LOGS/build.log"; exit 1; }
go build -o "$WORK/mockupstream" ./scripts/mockupstream 2>>"$LOGS/build.log" || { echo "mock build failed"; exit 1; }
echo "  built"

echo
echo "=== python client via the repo checkout (no install needed) ==="
REPO_ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
export PYTHONPATH="$REPO_ROOT/clients/python/src"
CLI() { python3 -u -m llmrx_client "$@"; }
check "client -version" "edge" "$(CLI -version)"

echo
echo "=== start mock upstream + server ==="
"$WORK/mockupstream" -addr 127.0.0.1:$MOCK_PORT > "$LOGS/mock.log" 2>&1 &
MOCK_PID=$!
sleep 1

cat > "$WORK/config.yml" <<EOF
server:
  host: 127.0.0.1
  port: $SRV_PORT
  allow_default_admin_password: true
database:
  driver: sqlite
  dsn: $WORK/llmrx.db
edge_clients:
  - name: unused-static
    token: "ecs_static_should_not_be_used"
EOF

# The server persists its master key under /data by default; hand it one
# via env so the E2E can run entirely from /tmp. Must be exported BEFORE
# the launch below.
export LLMRX_KEY_MASTER=0000000000000000000000000000000000000000000000000000000000000000

"$WORK/llmRx" -config "$WORK/config.yml" > "$LOGS/server.log" 2>&1 &
SRV_PID=$!
for i in $(seq 1 30); do
  curl -sS -o /dev/null "$SRV/health" 2>/dev/null && break
  sleep 0.5
done
check "server health" '"status"' "$(curl -sS "$SRV/health")"

echo
echo "=== admin: login, channel, token, enroll code ==="
JAR="$WORK/admin.jar"
check "admin login" 'session_token' "$(curl -sS -c "$JAR" -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' "$SRV/admin/api/v1/login")"

CHANNEL=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d "{\"name\":\"e2e-mock\",\"provider\":\"openai\",\"base_url\":\"http://127.0.0.1:$MOCK_PORT/v1\",\"protocol\":\"openai\",\"models\":[\"gpt-4o-mini\"]}" \
  "$SRV/admin/api/v1/channels")
check "channel created" '"id"' "$CHANNEL"
CH_ID=$(printf '%s' "$CHANNEL" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' 2>/dev/null)

# add an upstream key to the channel
curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d '{"key":"sk-mock-e2e"}' "$SRV/admin/api/v1/channels/$CH_ID/keys" > "$LOGS/key.json"

# The admin CreateChannel reloads the pool but not the static L1 index;
# force the full reload an operator would trigger after provisioning.
check "reload" '"ok":true' "$(curl -sS -b "$JAR" -X POST "$SRV/admin/api/v1/reload")"

TOKEN=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-user"}' "$SRV/admin/api/v1/tokens")
check "token created" '"key"' "$TOKEN"
USER_KEY=$(printf '%s' "$TOKEN" | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"])' 2>/dev/null)

ENROLL=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d '{"name":"py-edge-e2e"}' "$SRV/admin/api/v1/edge-clients/enroll-tokens")
check "enroll code issued" '"token"' "$ENROLL"
CODE=$(printf '%s' "$ENROLL" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])' 2>/dev/null)

echo
echo "=== python client: enroll ==="
check "enroll accepted" 'enrolled as' "$(CLI -enroll "$CODE" -server "$SRV" -config "$WORK/client.yml" 2>&1)"
check "config file created" 'client_token' "$(cat "$WORK/client.yml")"
check "config is 0600" '600' "$(stat -c %a "$WORK/client.yml")"
# Point the local listener where the E2E expects it (enroll writes the
# default listen when the config file did not exist yet — same as Go),
# and shorten the upload tick so the E2E does not wait a minute. Both
# must happen BEFORE the client starts: it reads the config once.
sed -i "s|listen: .*|listen: 127.0.0.1:$CLI_PORT|" "$WORK/client.yml"
sed -i "s|usage_upload_sec: .*|usage_upload_sec: 2|" "$WORK/client.yml"

echo
echo "=== python client: serve ==="
CLI -config "$WORK/client.yml" > "$LOGS/client.log" 2>&1 &
CLI_PID=$!
for i in $(seq 1 30); do
  curl -sS -o /dev/null "http://127.0.0.1:$CLI_PORT/healthz" 2>/dev/null && break
  sleep 0.5
done

sleep 2
HEALTH=$(curl -sS "http://127.0.0.1:$CLI_PORT/healthz")
check "client healthz" '"status":"ok"' "$HEALTH"
# The first heartbeat + bundle fetch happens right after startup; give it
# a moment, then the health snapshot carries the applied config version.
sleep 3
HEALTH=$(curl -sS "http://127.0.0.1:$CLI_PORT/healthz")
check "control plane wired" 'config_version' "$HEALTH"

echo
echo "=== chat through the python client ==="
CHAT=$(curl -sS -m 30 -H "Authorization: Bearer $USER_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","temperature":0,"messages":[{"role":"user","content":"e2e hello"}]}' \
  "http://127.0.0.1:$CLI_PORT/v1/chat/completions")
check "chat 200 via client" '"choices"' "$CHAT"
# The server's log row proves the full chain: client -> server -> mock
# (the mock's canned response carries 10/200 tokens). mockupstream only
# prints its request counter on a ticker, so it is not asserted here.

echo
echo "=== usage upload (upload_sec=2 was patched in before start) ==="
sleep 6
LOGS_JSON=$(curl -sS -b "$JAR" "$SRV/admin/api/v1/logs?limit=30")
check "server ingested usage rows" 'client:py-edge-e2e' "$LOGS_JSON"

echo
echo "=== client saw the signed bundle ==="
check "bundle applied" 'config bundle' "$(cat "$LOGS/client.log")"

echo
echo "=== admin sees the client online ==="
CLIENTS=$(curl -sS -b "$JAR" "$SRV/admin/api/v1/edge-clients")
check "edge client registered" 'py-edge-e2e' "$CLIENTS"

echo
echo "=================================================="
echo "  PASS=$pass  FAIL=$fail"
echo "  workdir: $WORK"
echo "=================================================="
[ "$fail" -eq 0 ] || exit 1
