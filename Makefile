GO ?= go
BIN := llmRx
PKG := ./...

.PHONY: all build test test-race cover web-sync run clean

all: build

build:
	$(GO) build -o $(BIN) ./cmd/gateway

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

cover:
	$(GO) test -coverprofile=/tmp/llmrx.cov.out $(PKG)
	$(GO) tool cover -func=/tmp/llmrx.cov.out | tail -1

web-sync:
	@if [ ! -d web/dist ]; then \
		echo "web/dist missing — run 'cd web && npm install && npm run build' first"; exit 1; \
	fi
	rm -rf internal/webui/dist
	mkdir -p internal/webui/dist
	cp -r web/dist/. internal/webui/dist/

run: build
	./$(BIN) -config config.yml

clean:
	rm -f $(BIN) /tmp/llmrx.cov.out
