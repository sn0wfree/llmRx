#!/bin/bash
set -euo pipefail

LLMRX_KEY_MASTER="${LLMRX_KEY_MASTER:-}"
if [ -z "$LLMRX_KEY_MASTER" ]; then
  LLMRX_KEY_MASTER=$(openssl rand -hex 32)
  export LLMRX_KEY_MASTER
  echo "[start] LLMRX_KEY_MASTER not set — generated fresh key"
fi

if [ ! -f ./llmRx ]; then
  echo "[start] building llmRx..."
  go build -o llmRx ./cmd/gateway
fi

echo "[start] listening on :8787  admin → http://localhost:8787/admin/"
exec ./llmRx -config config.yml
