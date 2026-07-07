#!/bin/sh
# scripts/build-docker.sh — build the docker image using pre-built
# artifacts produced on the host. Faster and works without pulling
# a Go toolchain / Node into the container.
#
# Steps:
#   1. Optionally rebuild the SPA (web/src/ → internal/webui/dist/).
#      Skip if `web/src` is absent or hasn't changed; the committed
#      internal/webui/dist is good enough otherwise.
#   2. Compile a linux/amd64 statically-linked Go binary into
#      build/llmRx (CGO=on for mattn/go-sqlite3 + sqlite built into
#      the binary via -extldflags -static). The embedded SPA makes
#      this a single file.
#   3. Run `docker build` to assemble the runtime layer.
#
# Usage:
#   scripts/build-docker.sh                 # default tag llmrx:local
#   scripts/build-docker.sh ghcr.io/me/x:dev
#   SKIP_WEB_BUILD=1 scripts/build-docker.sh   # use committed SPA
#   SKIP_GO_BUILD=1  scripts/build-docker.sh   # reuse build/llmRx
#
# Requires:  go (1.22+) and either npm OR SKIP_WEB_BUILD=1.

set -eu

IMAGE="${1:-llmrx:local}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$ROOT/build"

cd "$ROOT"

# ----- 1. SPA (optional) --------------------------------------------------

if [ "${SKIP_WEB_BUILD:-0}" = "1" ]; then
    echo "build-docker: SKIP_WEB_BUILD=1 — using committed internal/webui/dist"
elif [ -d web/src ] && command -v npm >/dev/null 2>&1; then
    echo "build-docker: building SPA (web/src → internal/webui/dist)"
    (cd web && npm ci --silent && npm run build)
    cp -r web/dist/* internal/webui/dist/
else
    echo "build-docker: skipping SPA build (no web/src or npm); using committed internal/webui/dist"
fi

# ----- 2. Go binary (statically linked) -----------------------------------

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

# ----- 3. docker build ---------------------------------------------------

echo "build-docker: building image $IMAGE"
exec docker build -t "$IMAGE" -f Dockerfile "$ROOT"