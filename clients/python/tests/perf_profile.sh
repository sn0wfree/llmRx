#!/usr/bin/env bash
# Performance profile: Python edge client vs direct-to-server baseline.
# Measures buffered latency, SSE time-to-first-byte, and concurrent
# throughput. Local only; no secrets involved.
set -u
REPO=/mnt/d/Github/llmRx
cd "$REPO" || exit 1
export PATH=/home/ll/.local/go/bin:$PATH
export PYTHONPATH="$REPO/clients/python/src"

WORK=$(mktemp -d /tmp/llmrx-perf.XXXXXX)
LOGS="$WORK/logs"; mkdir -p "$LOGS"
SRV=18951; MOCK=18952; CLI=18953
SRVU=http://127.0.0.1:$SRV
CLIU=http://127.0.0.1:$CLI

cleanup() {
  [ -n "${CLI_PID:-}" ] && kill "$CLI_PID" 2>/dev/null
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null
}
trap cleanup EXIT

echo "=== build ==="
go build -o "$WORK/llmRx" ./cmd/gateway 2>"$LOGS/build.log" || { echo "server build failed"; exit 1; }
go build -o "$WORK/mock" ./scripts/mockupstream 2>>"$LOGS/build.log" || exit 1

export LLMRX_KEY_MASTER=0000000000000000000000000000000000000000000000000000000000000000
MOCK_PORT=$MOCK
"$WORK/mock" -addr 127.0.0.1:$MOCK_PORT > "$LOGS/mock.log" 2>&1 &
MOCK_PID=$!
sleep 1
curl -sS -m 5 "http://127.0.0.1:$MOCK_PORT/health" > /dev/null || { echo "mock not up"; cat "$LOGS/mock.log"; exit 1; }

cat > "$WORK/config.yml" <<EOF
server:
  host: 127.0.0.1
  port: $SRV
  allow_default_admin_password: true
database:
  driver: sqlite
  dsn: $WORK/llmrx.db
EOF
"$WORK/llmRx" -config "$WORK/config.yml" > "$LOGS/server.log" 2>&1 &
SRV_PID=$!
for i in $(seq 1 40); do curl -sS -o /dev/null "$SRVU/health" 2>/dev/null && break; sleep 0.5; done

JAR="$WORK/admin.jar"
curl -sS -c "$JAR" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' "$SRVU/admin/api/v1/login" > /dev/null
CH=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d "{\"name\":\"perf\",\"provider\":\"openai\",\"base_url\":\"http://127.0.0.1:$MOCK/v1\",\"protocol\":\"openai\",\"models\":[\"gpt-4o-mini\"]}" \
  "$SRVU/admin/api/v1/channels")
CH_ID=$(printf '%s' "$CH" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d '{"key":"sk-perf"}' "$SRVU/admin/api/v1/channels/$CH_ID/keys" > /dev/null
TK=$(curl -sS -b "$JAR" -H 'Content-Type: application/json' \
  -d '{"name":"perf"}' "$SRVU/admin/api/v1/tokens")
USER_KEY=$(printf '%s' "$TK" | python3 -c 'import json,sys;print(json.load(sys.stdin)["key"])')
curl -sS -b "$JAR" -X POST "$SRVU/admin/api/v1/reload" > /dev/null

cat > "$WORK/client.yml" <<EOF
listen: 127.0.0.1:$CLI
servers:
  - $SRVU
cache_enabled: false
fallback_mode: cache_then_fail
usage_upload_sec: 3600
EOF
python3 -u -m llmrx_client -config "$WORK/client.yml" > "$LOGS/client.log" 2>&1 &
CLI_PID=$!
for i in $(seq 1 40); do curl -sS -o /dev/null "$CLIU/healthz" 2>/dev/null && break; sleep 0.5; done

REQ_BODY='{"model":"gpt-4o-mini","temperature":0,"messages":[{"role":"user","content":"perf"}]}'
hdr() { printf 'Authorization: Bearer %s' "$USER_KEY"; }

latencies() { # latencies <url> <n>
  for i in $(seq 1 "$2"); do
    curl -sS -o /dev/null -m 30 -w '%{time_total}\n' \
      -H "Authorization: Bearer $USER_KEY" -H 'Content-Type: application/json' \
      -d "$REQ_BODY" "$1"
  done
}
stats() { # stats on stdin
  python3 -c '
import sys
xs = sorted(float(x) for x in sys.stdin)
n = len(xs)
print(f"n={n} avg={sum(xs)/n*1000:.1f}ms p50={xs[n//2]*1000:.1f}ms p95={xs[int(n*0.95)-1]*1000:.1f}ms max={xs[-1]*1000:.1f}ms")'
}

echo
echo "=== warmup (5 via client) ==="
latencies "$CLIU/v1/chat/completions" 5 > /dev/null
# Validity gate: a real proxied answer must contain choices; otherwise
# every number below measures error responses, not the proxy.
PROOF=$(curl -sS -m 30 -H "Authorization: Bearer $USER_KEY" \
  -H 'Content-Type: application/json' -d "$REQ_BODY" \
  "$CLIU/v1/chat/completions")
echo "$PROOF" | grep -q '"choices"' || { echo "INVALID RESPONSE: $PROOF"; exit 1; }
echo "  validity gate passed (real upstream response confirmed)"

echo "=== A. buffered latency, 50 req ==="
echo -n "  direct server: "; latencies "$SRVU/v1/chat/completions" 50 | stats
echo -n "  via py client: "; latencies "$CLIU/v1/chat/completions" 50 | stats

echo
echo "=== B. SSE time-to-first-byte, 20 streams ==="
ttft() { # ttft <url>
  for i in $(seq 1 20); do
    curl -sS -N -o /dev/null -m 30 -w '%{time_starttransfer}\n' \
      -H "Authorization: Bearer $USER_KEY" -H 'Content-Type: application/json' \
      -d "{\"model\":\"gpt-4o-mini\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"perf\"}]}" "$1"
  done
}
echo -n "  direct server: "; ttft "$SRVU/v1/chat/completions" | stats
echo -n "  via py client: "; ttft "$CLIU/v1/chat/completions" | stats

echo
echo "=== C. concurrent throughput: 8 workers x 25 req ==="
runload() { # runload <url>
  seq 1 8 | xargs -P 8 -I{} bash -c '
    for i in $(seq 1 25); do
      curl -sS -o /dev/null -m 30 \
        -H "Authorization: Bearer '"$USER_KEY"'" -H "Content-Type: application/json" \
        -d "'"$REQ_BODY"'" "'"$1"'"
    done'
}
t0=$(date +%s.%N)
runload "$SRVU/v1/chat/completions"; d1=$(echo "$(date +%s.%N) - $t0" | bc)
t0=$(date +%s.%N)
runload "$CLIU/v1/chat/completions"; d2=$(echo "$(date +%s.%N) - $t0" | bc)
python3 -c "print(f'  direct: 200 req in {$d1:.2f}s = {200/$d1:.0f} rps')"
python3 -c "print(f'  client: 200 req in {$d2:.2f}s = {200/$d2:.0f} rps')"

echo
echo "=== client health after load ==="
curl -sS "$CLIU/healthz"
echo
echo "WORKDIR=$WORK"
