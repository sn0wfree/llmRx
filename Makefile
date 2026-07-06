GO ?= go
BIN := llmRx
PKG := ./...

# `make build` (and anything that depends on it) auto-runs web-sync
# when web/src/ has changed since the last sync. Set SKIP_WEB_SYNC=1
# to bypass (e.g. in CI without Node), or set HAS_NODE=1 to force.
# The committed internal/webui/dist/ is the source of truth for
# Node-less builds, so a clean checkout builds out of the box.

.PHONY: all build test test-race cover web-sync web-build run clean build-go-only

all: build

# ----- auto web-sync -----

# new_web: 1 when web source (or freshly built web/dist) is newer
# than internal/webui/dist. Stamps are mtimes; using find -newer is
# more robust than touch because we don't write into web/src.
HAS_NODE := $(shell command -v npm >/dev/null 2>&1 && echo 1 || echo 0)

# Detect: web/dist newer than embedded? rebuild embed
# Detect: any web/src file newer than embedded? rebuild SPA + embed
NEED_REBUILD_SPA := $(shell \
  if [ ! -d internal/webui/dist ]; then echo 1; \
  elif [ -d web/src ] && [ -n "$$(find web/src -newer internal/webui/dist -type f 2>/dev/null | head -1)" ]; then echo 1; \
  elif [ -d web/dist ] && [ -n "$$(find web/dist -newer internal/webui/dist -type f 2>/dev/null | head -1)" ]; then echo 1; \
  else echo 0; \
  fi)

# Honour opt-out env: SKIP_WEB_SYNC=1 → never touch the embed dir.
ifdef SKIP_WEB_SYNC
  NEED_REBUILD_SPA = 0
endif

web-build:
	@if [ -n "$$SKIP_WEB_SYNC" ]; then \
		echo "web: skipped (SKIP_WEB_SYNC)"; \
	elif [ "$(HAS_NODE)" != "1" ]; then \
		echo "web: skipped (npm not on PATH; using committed dist)"; \
	elif [ -z "$$(find web/src -newer web/dist/index.html -type f 2>/dev/null | head -1)" ] && [ -f web/dist/index.html ]; then \
		echo "web: already up to date"; \
	else \
		echo "web: npm run build"; \
		cd web && npm install --silent && npm run build; \
	fi

# web-sync copies web/dist -> internal/webui/dist unconditionally
# when called directly. When called from build, it only acts if
# NEED_REBUILD_SPA=1.
web-sync: web-build
	@if [ -n "$$SKIP_WEB_SYNC" ]; then \
		echo "embed: skipped (SKIP_WEB_SYNC)"; \
	elif [ "$(NEED_REBUILD_SPA)" != "1" ] && [ -d internal/webui/dist ] && [ -n "$$(ls internal/webui/dist 2>/dev/null)" ]; then \
		echo "embed: up to date"; \
	else \
		if [ ! -d web/dist ]; then \
			echo "embed: web/dist missing and SKIP_WEB_SYNC not set — using committed internal/webui/dist"; \
		else \
			echo "embed: syncing web/dist -> internal/webui/dist"; \
			rm -rf internal/webui/dist; \
			mkdir -p internal/webui/dist; \
			cp -r web/dist/. internal/webui/dist/; \
		fi \
	fi

# ----- top-level targets -----

build: web-sync
	$(GO) build -o $(BIN) ./cmd/gateway

# build-go-only: skip the web-sync chain entirely; uses whatever
# internal/webui/dist/ is currently committed. Use in CI containers
# without Node, or for fast iterative Go builds.
build-go-only:
	$(GO) build -o $(BIN) ./cmd/gateway

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

cover:
	$(GO) test -coverprofile=/tmp/llmrx.cov.out $(PKG)
	$(GO) tool cover -func=/tmp/llmrx.cov.out | tail -1

run: build
	./$(BIN) -config config.yml

clean:
	rm -f $(BIN) /tmp/llmrx.cov.out
