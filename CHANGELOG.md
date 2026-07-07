# Changelog

All notable changes to llmRx are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased] — Hardening + P7+ 闭环 + Passthrough + 多租户 + 热重载

### Security
- **Argon2id** password hash replaces P6 bcrypt. New params:
  m=65536 KiB, t=3, p=2, 16-byte salt, 32-byte key. Both bcrypt
  and pre-P6 plaintext hashes are detected and transparently
  upgraded to Argon2id on the user's next successful login.
- **At-rest encryption of channel API keys** (AES-256-GCM).
  New `internal/secrets` package wraps the AEAD; master key is
  loaded from `LLMRX_KEY_MASTER` (32-byte hex, `openssl rand -hex 32`)
  at startup. New `key_ciphertext` column on `keys`; the legacy
  `key` column is treated as transient — writes encrypt + clear
  plaintext, reads decrypt ciphertext, and legacy rows are
  best-effort migrated on first access. The gateway refuses to
  start without a master key in production; `DEV_ALLOW_PLAINTEXT_KEYS=true`
  re-enables plaintext mode for local dev only. A tampered
  ciphertext, a wrong master key, or a ciphertext row read
  without a configured manager all fail loudly (no silent empty
  Authorization header to the upstream). Admin handlers mask
  secrets via `secrets.Mask` (`sk-a***mnop`). `store.Store`
  interface gains `Ping(ctx)`.

### Added

#### Streaming + cap hardening
- **SSE streaming** for `/v1/chat/completions` with `stream=true`.
  OpenAI + Anthropic providers implement a new `StreamingProvider`
  interface that opens an SSE connection to the upstream, parses
  chunks as they arrive, and writes them back to the client with
  `text/event-stream`. Errors are emitted as a single `event: error`
  frame before the connection closes. Each stream consumes a
  `logs` row at the end.
- **Streaming caps** (configurable, all default-on):
  - `stream_timeout_sec` (default 300): ctx-deadline aborts mid-flight
    with HTTP 504 + `event: error {"message":"stream timeout exceeded"}`
  - `stream_max_body_bytes` (default 32 MiB): cumulative bytes written
    to the wire is capped; a malformed upstream streaming indefinitely
    is terminated with HTTP 413 + `stream max body bytes exceeded`
- **Broker subscriber cap** (`server.max_log_subscribers`, default
  256 for in-process `testhelper`, unbounded for production):
  `broker.New(max)` + `ErrTooManySubscribers` rejects new SSE
  connections with HTTP 503.

#### Routing
- **L5 Thompson Sampling** (`internal/router/thompson`) — each
  channel gets a Beta(α, β) posterior over its success probability.
  After every `RecordSuccess` / `RecordFailure` the posterior
  updates; the router samples θ per candidate and ranks by sample.
  `MinSamplesPerChannel` cold-start gate (default 5) prevents L5
  from perturbing L3's deterministic ordering until enough data
  has been collected.
- **L4 Intent Classifier** (`internal/intent/`) — Rust cdylib + cgo
  binding. Default backend is a keyword scorer (5 intent labels);
  `onnx` cargo feature enables ONNX Runtime. Channels declare
  supported `intents`; during routing, channels whose `intents`
  include the predicted intent are bubbled to the front.
  If the cdylib is missing at startup, the engine uses `intent.Nop{}`.

#### Multi-protocol providers
- **OpenAIProvider** — `/chat/completions` + SSE.
- **AnthropicProvider** — `/v1/messages` with `x-api-key` +
  `anthropic-version` headers, system prompt as a top-level field,
  response `content[].text` reassembled into OpenAI shape, SSE
  translated to OpenAI chunks.
- **GeminiProvider** — `/v1beta/models/{model}:generateContent?key=...`
  with `systemInstruction`, `contents[].parts[].text`, usage mapped
  from `usageMetadata`.
- `api.Handler` picks the adapter per channel based on
  `Channel.Protocol` (`openai` | `anthropic` | `gemini`, default `openai`).

#### Full OpenAI spec passthrough (Phase A of PASSTHROUGH.md)
- `provider.ChatRequest` widened with 24 new fields: `temperature`
  (`*float64`), `top_p`, `max_completion_tokens`, `n`, `seed`,
  `user`, `logprobs`, `top_logprobs`, `logit_bias`, `stop`,
  `frequency_penalty`, `presence_penalty`, `parallel_tool_calls`,
  `store`, `reasoning_effort`, `prompt_cache_key`, `metadata`,
  `tools`, `tool_choice`, `response_format`, `stream_options`.
- `provider.Message` widens to allow `content` as string OR
  `[]ContentPart` (multimodal text + image_url), plus `name`,
  `tool_calls`, `tool_call_id`, `cache_control` (Anthropic), `refusal`.
- `provider.ChatResponse` gains `system_fingerprint` + `logprobs`.
- `provider.StreamChunk` gains `system_fingerprint` + `logprobs`.
- `provider.Usage` gains `PromptTokensDetails.CachedTokens` +
  `CompletionTokensDetails` (reasoning_tokens).
- `AnthropicProvider.translateReq` plumbs: temperature, top_p,
  top_k, stop_sequences, metadata, tools (with input_schema),
  tool_choice, `max_completion_tokens` preferred over `max_tokens`.
- `GeminiProvider.translateReq` plumbs: temperature, top_p, top_k,
  candidate_count, stopSequences, responseMimeType (json_object
  → `application/json`), responseSchema (json_schema), tools
  (functionDeclarations), tool_config.function_calling_config.mode
  (OpenAI `auto|required|none` ↔ Gemini `AUTO|ANY|NONE`).

#### Cache control + spend discount (Phase B)
- **Anthropic cache_control passthrough**: `Message.CacheControl`
  switches `system` and `messages[].content` to array form on the
  wire. System blocks accept `5m|1h|ephemeral`; messages only
  `ephemeral`. Plain prompts keep the string form (minimal wire).
- **Cached-token cost discount**: `model.Channel.CachedInputDiscount`
  (default 0.1 = pay 10%, Anthropic's actual rate). `calcCost()`
  computes `(prompt - cached)/1e6 × input + cached/1e6 × input ×
  discount + completion/1e6 × output`. `model.Log.CachedTokens`
  persisted for analytics. Defensive: cached > prompt is clamped.

#### Multi-tenant enforcement
- **Per-token rate limiting**: `internal/ratelimit.Limiter` is a
  sliding-window (60s) RPM/TPM enforcer keyed by token ID. Process-local;
  race-clean.
- `middleware.TokenInfo` carries `PlanID / RPM / TPM / ModelsWhitelist /
  IPWhitelist`. The middleware places the full TokenInfo on context.
- `middleware.WithLimits(lookup, enforcer)` wraps `Token()`. Over-cap
  requests return HTTP 429 + `rate_limited`.
- `middleware.HasModelAccess(model)` / `HasIPAccess(ip)` enforce the
  whitelists (`*` matches anything). `ChatCompletions` checks both
  before any provider work; rejects with HTTP 403.
- **Per-token spend tracking**: `store.IncrementTokenSpend` does an
  atomic `UPDATE tokens.used_usd = used_usd + ?`. `emitLog`
  invokes it on every request.
- **Per-plan spend tracking**: `store.IncrementPlanSpend`. `emitLog`
  reads `TokenInfo.PlanID` from the request context, looks up the
  Plan, and applies `Plan.MarkupRatio` on top of the channel markup
  before persisting the log row.

#### Hot reload
- **`PUT /api/v1/tokens/{id}`** (`UpdateToken`) — patches
  `plan_id`, `status`, `RPM`, `TPM`, `model_whitelist`,
  `ip_whitelist`, `expires_in_days`. Status changes flow through
  the token cache so a disabled token is rejected on the next
  request, no restart needed.
- **`POST /api/v1/reload`** (`ReloadAll`) — forces every in-memory
  cache to re-read from the store: token cache, channel pool,
  router engine state (breaker + Thompson posterior), alert rules.
  Idempotent. Used after manual DB edits or `kubectl exec`.
- `RouterEngine.ReloadAllChannels()` clears every channel's breaker
  entry + Thompson posterior. `ReloadChannel(id)` now also closes
  the breaker entry (previously just warmed the cache).
- Channel / Token / User CRUD admin handlers already auto-reload
  the relevant cache on write; this change adds `UpdateToken` and
  the global `reload` endpoint.

#### Schema migrations (auto, `addColumnIfMissing`)
- `channels.protocol` (P7+), `channels.intents` (P7+),
  `channels.cached_input_discount` (P7+ Phase B),
  `logs.cached_tokens` (P7+ Phase B),
  `tokens.used_usd` (multi-tenant).

#### Internal
- `golang.org/x/crypto` pinned to `v0.5.0` (Go 1.18 compatible;
  newer versions require Go 1.25+).
- `runtime.Defaults` extended with `CostStrategy()` /
  `SetCostStrategy()`.
- `model.Channel` gains `Intents []string`, `Protocol string`,
  `CachedInputDiscount float64`.
- `model.Token` gains `UsedUSD float64`.
- `model.Log` gains `CachedTokens int`.
- `provider.Provider` interface extended with optional
  `StreamingProvider` sub-interface.
- New packages: `internal/auth`, `internal/broker`, `internal/sse`,
  `internal/alert`, `internal/alert/channels`, `internal/runtime`,
  `internal/intent` (Go wrapper), `internal/intent/rust` (Rust crate),
  `internal/router/thompson`, `internal/ratelimit`.
- `middleware` gains `TokenInfoKey`, `WithLimits`, `HasModelAccess`,
  `HasIPAccess`.
- `internal/store.Store` gains `UpdateToken`, `GetTokenByID`,
  `IncrementTokenSpend`, `IncrementPlanSpend`.

#### Docs
- `docs/ARCHITECTURE.md` — 18 sections, covers routing pipeline,
  broker, SSE, alerts, L4/L5, caching discount, multi-tenant,
  hot reload.
- `docs/COMPARATIVE.md` — vs LiteLLM / One-API / Bifrost / Kong
  feature matrix + tier-ranked gap list.
- `docs/PASSTHROUGH.md` — per-field OpenAI spec audit, Phase A+B
  status.
- `docs/P8-CACHING.md` — exact-match response cache design (P8).
- `docs/P9-MULTIMODAL.md` — Image / Rerank / Audio endpoints (P9).
- `docs/P10-OBSERVABILITY.md` — OTel + Prometheus (P10).
- `docs/P11-MCP.md` — MCP gateway (P11).

#### CI
- `test.yml` coverage gate raised from 55% → 60% → still ~65% after
  hardening. New optional step builds the L4 cdylib when `cargo`
  is on PATH.

## [P6] — earlier

P0 + P1 + P2 + P3 + P6: bcrypt 密码 hash + 改密 UI + 告警子系统（webhook + 站内） + SSE 实时日志 + Settings 4 Tab + 运行时 markup + 日志保留 + Dockerfile（distroless） + docker-compose + Docker CI（amd64+arm64）。

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
- **Docker runtime image shrunk to ~13 MB** (was ~109 MB). The
  multi-stage distroless Dockerfile is replaced by a thin
  `FROM scratch` + statically-linked CGO binary (`-ldflags="-s -w
  -extldflags '-static'"`); the Go binary itself handles master-key
  bootstrap (`env → /data/llmrx.key → generate`), bind-mount
  `/data` chown fixup, privilege drop (`setuid` to llmrx), and
  docker HEALTHCHECK probe (`-healthcheck` flag does raw TCP +
  HTTP/1.0 GET). No shell, no busybox, no separate init helper,
  no entrypoint script. Unit tests in `cmd/gateway/bootstrap_test.go`
  cover all three bootstrap functions and the healthcheck probe.

### Internal
- New packages: `internal/auth`, `internal/broker`, `internal/sse`,
  `internal/alert`, `internal/alert/channels`, `internal/runtime`,
  `internal/secrets`.
- `internal/store.Store` interface gains: `DeleteLogsBefore`,
  full alerts CRUD, `RawQuery` / `RawQueryRow` for subsystem SQL,
  `Ping(ctx)` for liveness checks.
- `middleware.AdminOnly` accepts `?session_token=` query string as
  the final auth fallback (needed by EventSource which can't set
  custom headers).

## [P3] — earlier

Session TTL + 日志过滤 UI + Analytics 时序/Top-N（Recharts） + L3 策略运行时切换 + 自动 web-sync。

## [P0..P2] — initial

Go 骨架 + SQLite + WebUI + `/v1/chat/completions` + Token/Plan/User + Management API.