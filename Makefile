GO ?= go
BIN := llmRx
PKG := ./...

# The admin UI is Go html/template + HTMX + Tailwind CDN. There is
# no JavaScript build step: just `go build`. The Makefile has no
# web-sync / web-build / npm targets.

.PHONY: all build test test-race cover run clean build-go-only intent-rust

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

run: build
	./$(BIN) -config config.yml

clean:
	rm -f $(BIN) /tmp/llmrx.cov.out

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
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--tag $(IMAGE) \
		--push -f Dockerfile .

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
