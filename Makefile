GO ?= go
BIN := llmRx
PKG := ./...

# The admin UI is Go html/template + HTMX + Tailwind CDN. There is
# no JavaScript build step: just `go build`. The Makefile has no
# web-sync / web-build / npm targets.

.PHONY: all build test test-race cover cover-check run clean build-go-only intent-rust

all: build

# ----- top-level targets -----

build:
	$(GO) build -o $(BIN) ./cmd/gateway

# build-go-only is the same as build today; kept for backwards
# compatibility with scripts that referenced the old target name.
build-go-only:
	$(GO) build -o $(BIN) ./cmd/gateway

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

cover:
	$(GO) test -coverprofile=/tmp/llmrx.cov.out $(PKG)
	$(GO) tool cover -func=/tmp/llmrx.cov.out | tail -1

# cover-check runs the full test suite with per-package coverage,
# parses each per-package figure, and fails if any package drops
# below the floor defined below. Run in CI before merging.
#
# Floors:
#   webui  ≥ 80%  — admin UI has lots of one-line render wrappers
#                   (renderer.Render error branches hard to hit)
#   store  ≥ 80%  — postgres.go is a skeleton (PG isn't deployed
#                   in tests); sqlite_impl.go is the hot path
#   api    ≥ 80%  — streaming/combo paths + multiple SDK endpoints
# We start at 80% rather than the aspirational 90% to give
# breathing room as new code lands; bumping the floor is a
# separate PR per package.
COVER_FLOOR_WEBUI := 80
COVER_FLOOR_STORE := 80
COVER_FLOOR_API   := 80

cover-check:
	@set -e; \
	echo ">>> running full test suite (this takes ~30s)"; \
	$(GO) test -coverprofile=/tmp/llmrx-webui.cov.out ./internal/webui/... > /dev/null; \
	$(GO) test -coverprofile=/tmp/llmrx-store.cov.out ./internal/store/... > /dev/null; \
	$(GO) test -coverprofile=/tmp/llmrx-api.cov.out   ./internal/api/...   > /dev/null; \
	w=$$($(GO) tool cover -func=/tmp/llmrx-webui.cov.out | awk '/^total:/ {gsub("%","",$$3); print int($$3)}'); \
	s=$$($(GO) tool cover -func=/tmp/llmrx-store.cov.out | awk '/^total:/ {gsub("%","",$$3); print int($$3)}'); \
	a=$$($(GO) tool cover -func=/tmp/llmrx-api.cov.out   | awk '/^total:/ {gsub("%","",$$3); print int($$3)}'); \
	echo "webui: $$w%  store: $$s%  api: $$a%  (floors: $(COVER_FLOOR_WEBUI)/$(COVER_FLOOR_STORE)/$(COVER_FLOOR_API))"; \
	fail=0; \
	if [ $$w -lt $(COVER_FLOOR_WEBUI) ]; then echo "FAIL: webui $$w < $(COVER_FLOOR_WEBUI)"; fail=1; fi; \
	if [ $$s -lt $(COVER_FLOOR_STORE) ]; then echo "FAIL: store $$s < $(COVER_FLOOR_STORE)"; fail=1; fi; \
	if [ $$a -lt $(COVER_FLOOR_API)   ]; then echo "FAIL: api   $$a < $(COVER_FLOOR_API)";   fail=1; fi; \
	exit $$fail

run: build
	./$(BIN) -config config.yml

clean:
	rm -f $(BIN) /tmp/llmrx.cov.out /tmp/llmrx-*.cov.out

# ----- docker -----
#
# The Dockerfile is built on `scratch` + the statically-linked Go
# binary from `make build-go-only` (or the build script). Image
# ends up around 13 MB — no shell, no busybox, no init helper.
#
# `make docker-build`   — host compile + docker build (llmrx:local)
# `make docker-run`     — auto-build (if needed) + docker compose up -d
# `make docker-logs`    — tail the running container
# `make docker-stop`    — stop & remove the container (data volume kept)
# `make docker-push`    — buildx multi-arch + push to a registry tag
#
# Override the tag with `make docker-build IMAGE=ghcr.io/me/llmrx:dev`.
# Skip the Go rebuild:        SKIP_GO_BUILD=1 make docker-build
IMAGE ?= llmrx:local

docker-build:
	./scripts/build-docker.sh $(IMAGE)

# Build (if image missing) then bring up the container. We
# deliberately do NOT pass `--build` to `docker compose up` — the
# default compose build path goes through buildkitd, which doesn't
# honor the docker daemon's registry-mirrors config. Running
# `docker build` first (above) keeps the build on the daemon path,
# which respects the mirror config. Users on public-internet hosts
# can still call `docker compose up -d --build` directly.
docker-run:
	@if ! docker image inspect $(IMAGE) >/dev/null 2>&1; then \
		echo ">>> Image $(IMAGE) not found locally; building first..."; \
		./scripts/build-docker.sh $(IMAGE); \
	fi
	docker compose up -d
	@echo
	@echo "llmRx: http://localhost:8787/admin/   (logs: make docker-logs)"

docker-logs:
	docker compose logs -f llmrx

docker-stop:
	docker compose down

docker-push:
	# amd64 only for now — the CGO sqlite3 build needs a cross gcc
	# for arm64, and CI builds the binary with GOARCH=amd64 before
	# docker build (see .github/workflows/docker.yml).
	docker buildx build \
		--platform linux/amd64 \
		--tag $(IMAGE) \
		--push -f Dockerfile .
	# NOTE: builds the image from a prebuilt build/llmRx (same as
	# scripts/build-docker.sh); run that script first, or CI's Go
	# step, so the binary exists in the build context.

# ----- L4 intent (Rust cdylib) -----
#
# The intent classifier is a Rust crate that compiles to a .so the
# Go side loads via dlopen at startup. Build with `make intent-rust`
# before running `make run` if you want L4 active; otherwise the
# router silently uses the in-process Nop classifier.

INTENT_DIR := internal/intent/rust
INTENT_LIB := $(INTENT_DIR)/target/release/libllmrx_intent.so

intent-rust:
	cd $(INTENT_DIR) && cargo build --release
	@echo "intent: built $(INTENT_LIB)"
