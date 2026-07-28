#!/bin/sh
# scripts/build-docker.sh — build the docker image using a
# statically-linked Go binary produced on the host. No Node step
# is involved: the admin UI ships as Go html/template.
#
# Steps:
#   1. Compile a linux/amd64 statically-linked Go binary into
#      build/llmRx (CGO=on for mattn/go-sqlite3 + sqlite built
#      into the binary via -extldflags -static).
#   2. Run `docker build` to assemble the runtime layer.
#
# Usage:
#   scripts/build-docker.sh                 # default tag llmrx:local
#   scripts/build-docker.sh ghcr.io/me/x:dev
#   SKIP_GO_BUILD=1 scripts/build-docker.sh   # reuse build/llmRx
#
# Requires:  go (1.22+).

set -eu

IMAGE="${1:-llmrx:local}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$ROOT/build"

cd "$ROOT"

# ----- 1. Go binary (statically linked) -----------------------------------

mkdir -p "$BUILD_DIR"

if [ "${SKIP_GO_BUILD:-0}" = "1" ] && [ -f "$BUILD_DIR/llmRx" ]; then
    echo "build-docker: SKIP_GO_BUILD=1 — reusing $BUILD_DIR/llmRx"
else
    echo "build-docker: compiling linux/amd64 static Go binary → $BUILD_DIR/llmRx"
    # Robust GOPROXY chain — works behind Chinese mirrors too.
    export GOPROXY="${GOPROXY:-https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct}"
    export GOSUMDB="${GOSUMDB:-off}"
    # Static link so the runtime image can be `FROM scratch`.
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
        go build -ldflags="-s -w -extldflags '-static'" \
            -o "$BUILD_DIR/llmRx" ./cmd/gateway
fi

# ----- 1b. Optional UPX compression (reduces binary from ~14 MB to ~5 MB) ---

if [ "${SKIP_UPX:-0}" != "1" ] && command -v upx >/dev/null 2>&1; then
    echo "build-docker: compressing binary with upx"
    upx --best --lzma --overwrite "$BUILD_DIR/llmRx"
else
    echo "build-docker: upx not found or SKIP_UPX=1 — skipping compression"
fi

# ----- 2. docker build ---------------------------------------------------

echo "build-docker: building image $IMAGE"
exec docker build -t "$IMAGE" -f Dockerfile "$ROOT"