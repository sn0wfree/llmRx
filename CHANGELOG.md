# Changelog

All notable changes to llmRx are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased] — P7+ 闭环

### Security
- **Argon2id** password hash replaces P6 bcrypt. New params:
  m=65536 KiB, t=3, p=2, 16-byte salt, 32-byte key. Both bcrypt
  and pre-P6 plaintext hashes are detected and transparently
  upgraded to Argon2id on the user's next successful login.

### Added
- **SSE streaming** for `/v1/chat/completions` with `stream=true`.
  The `OpenAIProvider` and `AnthropicProvider` implement a new
  `StreamingProvider` interface that opens an SSE connection to
  the upstream, parses chunks as they arrive, and writes them
  back to the client with `text/event-stream`. The Gemini
  provider uses the non-streaming path (Google's stream protocol
  is materially different and out of scope here). Errors
  upstream are emitted as a single `event: error` frame before
  the connection closes. Each stream consumes a `logs` row at the
  end (with usage from the last chunk if the upstream emits it).
- **L5 Thompson Sampling** (`internal/router/thompson`) for
  adaptive channel weights. Each channel gets a Beta(α, β)
  posterior over its success probability. After every
  `RecordSuccess` / `RecordFailure` the posterior updates; the
  router samples θ per candidate and ranks by sample. A
  configurable `MinSamplesPerChannel` cold-start gate (default 5)
  prevents L5 from perturbing L3's deterministic ordering until
  enough data has been collected.
- **L4 Intent Classifier** (`internal/intent/`) implemented in
  Rust as a `cdylib` and loaded by the Go side via cgo. Default
  backend is a small keyword scorer (5 intent labels:
  `code`, `chat`, `summary`, `translate`, `math`); the `onnx`
  cargo feature enables an ONNX Runtime backend for true
  inference. Channels may declare an `intents` JSON column;
  during routing, channels whose `intents` include the predicted
  intent are bubbled to the front of the candidate list. If the
  cdylib is missing at startup, the engine uses `intent.Nop{}`
  and logs a warning — nothing breaks.
- **Multi-protocol provider adapters** (`internal/provider/multi.go`):
  - `OpenAIProvider` (existing) — `/chat/completions` + SSE.
  - `AnthropicProvider` — `/v1/messages` with `x-api-key` +
    `anthropic-version` headers, system prompt as a top-level
    field, response `content[].text` reassembled into the
    OpenAI-shape response, SSE translated to OpenAI chunks.
  - `GeminiProvider` — `/v1beta/models/{model}:generateContent?key=...`
    with `systemInstruction`, `contents[].parts[].text`, usage
    mapped from `usageMetadata`.
  - The `api.Handler` picks the adapter per channel based on the
    `Channel.Protocol` field (`openai` | `anthropic` | `gemini`,
    default `openai`).
- **Channel `intents` and `protocol` columns** added via the
  existing schema-migration helper (no manual DB step needed).
- **CI `test.yml`**: coverage gate raised from 55% → 60%. New
  optional step builds the L4 cdylib when `cargo` is on PATH.

### Internal
- `golang.org/x/crypto` pinned to `v0.5.0` (works with Go 1.18;
  newer versions require Go 1.25+).
- `runtime.Defaults` extended with `CostStrategy()` / `SetCostStrategy()`.
- `model.Channel` gains `Intents []string` and `Protocol string`.
- `provider.Provider` interface extended with optional
  `StreamingProvider` sub-interface.
- New packages: `internal/intent` (Go wrapper), `internal/intent/rust`
  (Rust crate), `internal/router/thompson`.

## [P6] — earlier

P0 + P1 + P2 + P3 + P6: bcrypt 密码 hash + 改密 UI + 告警子系统（webhook + 站内） + SSE 实时日志 + Settings 4 Tab + 运行时 markup + 日志保留 + Dockerfile（distroless） + docker-compose + Docker CI（amd64+arm64）。

## [P3] — earlier

See git log for P0–P3 history.

### Security
- **bcrypt password hash** replaces pre-P6 plaintext `<salt>:<password>`
  scheme. Old hashes are detected and transparently re-hashed on
  next successful login.
- **Password change endpoint** `POST /api/v1/users/{id}/password`
  with old-password verification for self, admin override for others.
  Changes invalidate all active sessions for the target user.

### Added
- **SSE live log tailing** via `GET /api/v1/logs/stream`. The Logs
  page gains a "Live" toggle that opens an `EventSource` and prepends
  new entries in real time. Backed by an in-process broker
  (`internal/broker`).
- **Alert subsystem** (`internal/alert`):
  - Four rule types: `error_rate`, `p95_latency`, `cost_spike`, `key_exhausted`
  - `internal/alert/channels/webhook.go` — JSON POST to configurable URL
  - `internal/alert/channels/builtin.go` — stdout + persists to `alert_events`
  - 30s tick loop with per-rule cooldown gate
  - CRUD: `GET/POST/PUT/DELETE /api/v1/alerts`
  - Events: `GET /api/v1/alerts/events`, `POST /api/v1/alerts/events/{id}/ack`
- **Settings page** expanded to four tabs:
  - Routing — L3 cost strategy + runtime billing markup
  - Security — change admin password
  - Alerts — list/create/toggle/delete rules + recent events + ack
  - Maintenance — circuit-breaker defaults, alert cooldown default,
    log retention in days
- **Runtime configuration** for markup ratio, breaker defaults, alert
  cooldown, log retention. All changeable via
  `GET/PUT /api/v1/config` and persist in the in-process
  `runtime.Defaults` (atomic; takes effect on the next request).
- **Log retention** background loop: deletes logs older than the
  configured retention once per day. Set to 0 to disable.
- **Dockerfile** (multi-stage: `node:20-alpine` → `golang:1.22-alpine` →
  `gcr.io/distroless/static:nonroot`) + `.dockerignore` +
  `docker-compose.yml`.
- **GitHub Actions** new workflow `docker.yml`: buildx multi-arch
  (linux/amd64, linux/arm64) on `v*` tag → push to ghcr.io.

### Changed
- `internal/api.Handler` now reads billing markup from
  `runtime.Defaults` (atomic float64) instead of a constant.
- `cmd/gateway/main.go` constructs and wires `runtime.Defaults`,
  `broker.Broker`, and `alert.Manager`.
- Coverage gate raised from 50% to 55%.

### Internal
- New packages: `internal/auth`, `internal/broker`, `internal/sse`,
  `internal/alert`, `internal/alert/channels`, `internal/runtime`.
- `internal/store.Store` interface gains: `DeleteLogsBefore`,
  full alerts CRUD, `RawQuery` / `RawQueryRow` for subsystem SQL.
- `middleware.AdminOnly` accepts `?session_token=` query string as
  the final auth fallback (needed by EventSource which can't set
  custom headers).

## [P3] — earlier

See git log for P0–P3 history.
