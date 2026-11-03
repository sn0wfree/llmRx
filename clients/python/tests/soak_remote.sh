#!/usr/bin/env bash
# Soak the Python edge client against an ALREADY RUNNING llmRx server.
#
# Usage: soak_remote.sh <server_url> <client_token_file> [hours] [workdir]
#
# The client runs with client_token set, so the full control plane is
# exercised: heartbeat, signed config bundle, and usage upload to
# <server>/client/v1/usage every 60s. Progress (RSS / fds / request
# counters) lands in <workdir>/soak_progress.log every 60s.
set -u
SERVER=$1
TOKEN_FILE=$2
HOURS=${3:-4}
WORK=${4:-/opt/llmrx-test/soak}
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
mkdir -p "$WORK"
TOKEN=$(cat "$TOKEN_FILE")
PORT=18993

cat > "$WORK/client.yml" <<EOF
listen: 127.0.0.1:$PORT
servers:
  - $SERVER
client_token: "$TOKEN"
client_name: soak-server
cache_enabled: true
cache_ttl_sec: 300
fallback_mode: cache_then_fail
usage_file: $WORK/usage.jsonl
usage_upload_sec: 60
EOF

export PYTHONPATH="$REPO/clients/python/src"
python3 -u -m llmrx_client -config "$WORK/client.yml" > "$WORK/client.log" 2>&1 &
CLI_PID=$!

for i in $(seq 1 30); do
  curl -sS -o /dev/null -m 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null && break
  sleep 1
done
curl -sS -o /dev/null -m 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null || {
  echo "client did not come up; log:"; tail -5 "$WORK/client.log"; exit 1
}

BODY='{"model":"gpt-4o-mini","temperature":0,"messages":[{"role":"user","content":"soak"}]}'
STREAM='{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"soak"}]}'
END=$((SECONDS + HOURS * 3600))
ok=0; fail=0; n=0
PROG="$WORK/soak_progress.log"

echo "soak start $(date +%H:%M:%S) duration=${HOURS}h server=$SERVER pid=$CLI_PID" >> "$PROG"
while [ $SECONDS -lt $END ]; do
  n=$((n + 1))
  B="$BODY"; [ $((n % 10)) -eq 0 ] && B="$STREAM"
  if curl -sS -o /dev/null -m 30 -H 'Content-Type: application/json' \
       -d "$B" "http://127.0.0.1:$PORT/v1/chat/completions"; then
    ok=$((ok + 1))
  else
    fail=$((fail + 1))
    echo "$(date +%H:%M:%S) REQUEST FAIL" >> "$PROG"
  fi
  if [ $((n % 6)) -eq 0 ]; then
    RSS=$(awk '/VmRSS/{print $2}' /proc/$CLI_PID/status 2>/dev/null)
    FD=$(ls /proc/$CLI_PID/fd 2>/dev/null | wc -l)
    THR=$(awk '/Threads/{print $2}' /proc/$CLI_PID/status 2>/dev/null)
    PEND=$(curl -sS -m 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null | \
      grep -o '"pending_usage":[0-9]*' | cut -d: -f2)
    echo "$(date +%H:%M:%S) n=$n ok=$ok fail=$fail rss_kb=${RSS:-0} fds=$FD threads=$THR pending_usage=${PEND:-?}" >> "$PROG"
  fi
  sleep 10
done

echo "soak end $(date +%H:%M:%S) ok=$ok fail=$fail" >> "$PROG"
curl -sS -m 5 "http://127.0.0.1:$PORT/healthz" >> "$PROG" 2>&1
echo >> "$PROG"
tail -3 "$WORK/client.log" >> "$PROG"
kill "$CLI_PID" 2>/dev/null
