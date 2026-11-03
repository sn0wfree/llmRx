#!/usr/bin/env bash
# Soak: run the Python client under continuous light load for 4 hours.
# Samples RSS every 60s; one buffered request every 10s; one streaming
# request every 60s. Appends progress to soak_progress.log.
set -u
REPO=/mnt/d/Github/llmRx
cd "$REPO" || exit 1
export PATH=/home/ll/.local/go/bin:$PATH
export PYTHONPATH="$REPO/clients/python/src"

WORK=/mnt/d/llmrx-soak
rm -rf "$WORK"; mkdir -p "$WORK"
SRV=18961; MOCK=18962; CLI=18963
SRVU=http://127.0.0.1:$SRV
CLIU=http://127.0.0.1:$CLI
DURATION=${SOAK_HOURS:-4}
PROG="$WORK/soak_progress.log"

cleanup() {
  [ -n "${CLI_PID:-}" ] && kill "$CLI_PID" 2>/dev/null
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null
}
trap cleanup EXIT

export PATH=/home/ll/.local/go/bin:$PATH
go build -o "$WORK/llmRx" ./cmd/gateway || exit 1
go build -o "$WORK/mock" ./scripts/mockupstream || exit 1
export LLMRX_KEY_MASTER=0000000000000000000000000000000000000000000000000000000000000000
MOCK_PORT=$MOCK
"$WORK/mock" -addr 127.0.0.1:$MOCK > /dev/null 2>&1 &
MOCK_PID=$!; sleep 1

cat > "$WORK/config.yml" <<EOF
server:
  host: 127.0.0.1
  port: $SRV
  allow_default_admin_password: true
database:
  driver: sqlite
  dsn: $WORK/llmrx.db
EOF
"$WORK/llmRx" -config "$WORK/config.yml" > "$WORK/server.log" 2>&1 &
SRV_PID=$!
for i in $(seq 1 40); do curl -sS -o /dev/null "$SRVU/health" 2>/dev/null && break; sleep 0.5; done

JAR="$WORK/admin.jar"
curl -sS -c "$JAR" -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' "$SRVU/admin/api/v1/login" > /dev/null
CH=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' -d "{\"name\":\"soak\",\"provider\":\"openai\",\"base_url\":\"http://127.0.0.1:$MOCK/v1\",\"protocol\":\"openai\",\"models\":[\"gpt-4o-mini\"]}" "$SRVU/admin/api/v1/channels")
CH_ID=$(printf '%s' "$CH" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
curl -sS -b "$JAR" -H 'Content-Type: application/json' -d '{"key":"sk-soak"}' "$SRVU/admin/api/v1/channels/$CH_ID/keys" > /dev/null
TK=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' -d '{"name":"soak"}' "$SRVU/admin/api/v1/tokens")
USER_KEY=$(printf '%s' "$TK" | python3 -c 'import json,sys;print(json.load(sys.stdin)["key"])')
curl -sS -b "$JAR" -X POST "$SRVU/admin/api/v1/reload" > /dev/null

cat > "$WORK/client.yml" <<EOF
listen: 127.0.0.1:$CLI
servers:
  - $SRVU
cache_enabled: true
cache_ttl_sec: 300
fallback_mode: cache_then_fail
usage_file: $WORK/usage.jsonl
usage_upload_sec: 3600
EOF
python3 -u -m llmrx_client -config "$WORK/client.yml" > "$WORK/client.log" 2>&1 &
CLI_PID=$!
for i in $(seq 1 40); do curl -sS -o /dev/null "$CLIU/healthz" 2>/dev/null && break; sleep 0.5; done

BODY='{"model":"gpt-4o-mini","temperature":0,"messages":[{"role":"user","content":"soak"}]}'
STREAM_BODY='{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"soak"}]}'
RSS_PID=$CLI_PID
END=$((SECONDS + DURATION * 3600))
ok=0; fail=0; n=0

echo "soak start $(date +%H:%M:%S) duration=${DURATION}h pid=$RSS_PID" >> "$PROG"
while [ $SECONDS -lt $END ]; do
  n=$((n+1))
  if [ $((n % 10)) -eq 0 ]; then
    B="$STREAM_BODY"
  else
    B="$BODY"
  fi
  if curl -sS -o /dev/null -m 30 -H "Authorization: Bearer $USER_KEY" \
       -H 'Content-Type: application/json' -d "$B" "$CLIU/v1/chat/completions"; then
    ok=$((ok+1))
  else
    fail=$((fail+1))
    echo "$(date +%H:%M:%S) REQUEST FAIL" >> "$PROG"
  fi
  if [ $((n % 6)) -eq 0 ]; then
    RSS=$(ps -o rss= -p "$RSS_PID" 2>/dev/null | tr -d ' ')
    FD=$(ls /proc/$RSS_PID/fd 2>/dev/null | wc -l)
    THR=$(cat /proc/$RSS_PID/status 2>/dev/null | grep Threads | awk '{print $2}')
    echo "$(date +%H:%M:%S) n=$n ok=$ok fail=$fail rss_kb=${RSS:-0} fds=$FD threads=$THR" >> "$PROG"
  fi
  sleep 10
done

echo "soak end $(date +%H:%M:%S) ok=$ok fail=$fail" >> "$PROG"
echo "=== final healthz ===" >> "$PROG"
curl -sS "$CLIU/healthz" >> "$PROG" 2>&1
echo >> "$PROG"
echo "=== client log tail ===" >> "$PROG"
tail -5 "$WORK/client.log" >> "$PROG"
