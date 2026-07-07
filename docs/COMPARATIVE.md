# Comparative Survey — llmRx vs Other LLM Gateways

> Sources: GitHub READMEs, project docs (LiteLLM 52.8k★, One-API 35.5k★,
> Bifrost 6.3k★, Kong 43.7k★, OpenRouter, Helicone, Portkey). Snapshot
> date: 2026-07. Goals:
>
> 1. Validate the existing feature surface against peers.
> 2. Identify known capability gaps.
> 3. Inform the P8+ roadmap.

## 1. Projects surveyed

| Project | Stars | Language | Positioning |
|---|---:|---|---|
| **LiteLLM** (BerriAI) | 52.8k | Python | Full LLM gateway + SDK, largest provider coverage |
| **One-API** (songquanpeng) | 35.5k | Go | Single binary, OpenAI-compatible relay |
| **Kong Gateway / AI** | 43.7k | Lua / Go | Enterprise API + AI gateway, plugin hub |
| **Bifrost** (maximhq) | 6.3k | Go | Ultra-low latency gateway, native Go |
| **OpenRouter** | SaaS | — | Unified commercial interface, 70+ providers |
| **Portkey** | — | TS | Production observability + governance |
| **Helicone** | — | TS | Observability-led gateway |

## 2. Feature matrix (snapshot: 2026-07, post multi-tenant + hot reload)

| Feature | LiteLLM | One-API | Bifrost | Kong AI | **llmRx (now)** |
|---|:---:|:---:|:---:|:---:|:---:|
| **Protocol coverage** | 100+ | 25+ | 23+ | 10+ | **3** |
| OpenAI-compatible API | ✅ | ✅ | ✅ | ✅ | ✅ |
| Streaming SSE | ✅ | ✅ | ✅ | ✅ | ✅ |
| Multi-protocol (Anthropic, Gemini) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Full OpenAI spec passthrough (tools / response_format / multimodal / etc.) | ✅ | partial | ✅ | ✅ | ✅ |
| Embeddings / Rerank / Audio / Images | ✅ | ❌ | partial | ✅ | ❌ (P9) |
| MCP gateway | ✅ (≥ v1.50) | ❌ | ✅ | ✅ | ❌ (P11) |
| A2A agent | ✅ (new) | ❌ | ❌ | ❌ | ❌ |
| Responses API | ✅ | ❌ | ❌ | ❌ | ❌ |
| Auto router (dynamic model pick) | ✅ | ❌ | ✅ | ✅ | partial (L4 keyword) |
| Tool/function call pass-through | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Exact-match response cache** | ✅ (Redis/s3/disk) | ❌ | ✅ | ✅ | ❌ (P8) |
| **Semantic cache** | ✅ (Qdrant / Redis / Disk / S3 / GCS) | ❌ | ✅ (Redis) | ✅ | ❌ (P9+) |
| Anthropic prompt caching passthrough | ✅ | ❌ | ✅ | ❌ | ✅ (cache_control blocks + cached_tokens discount) |
| **Per-token rate limit (RPM/TPM)** | ✅ (rpm/tpm) | ✅ (global) | ✅ | ✅ | ✅ (per-token sliding window) |
| **Token whitelist (model / IP)** | ✅ | ✅ (group) | ✅ | ✅ | ✅ |
| **Per-token spend tracking** | ✅ | ✅ (USD) | ✅ | ✅ | ✅ (atomic UPDATE) |
| **Per-plan markup** | ✅ | ✅ (group) | ✅ | ✅ | ✅ (plan.MarkupRatio on top of channel) |
| Multi-tenant / groups | ✅ | ✅ | ✅ | ✅ | ✅ (Plan + Token.plan_id) |
| Redemption code system | ❌ | ✅ | ❌ | ❌ | ❌ |
| Failover / auto-retry | ✅ | ✅ | ✅ | ✅ | ✅ (L2 breaker + downstream retry) |
| Load balancing | ✅ (latency/rpm/tpm) | ✅ (group) | ✅ (adaptive) | ✅ | ✅ (priority + strategy) |
| **Thompson / RL routing** | ❌ | ❌ | ✅ | ❌ | ✅ **L5** |
| **Intent classification L4** | ❌ | ❌ | ❌ | ❌ | ✅ **Rust cdylib + ONNX feature** |
| Circuit breaker | ✅ | ✅ | ✅ | ✅ | ✅ |
| Image generation | ✅ | ✅ | ✅ | ❌ | ❌ (P9) |
| **Guardrails (input/output)** | ✅ (Lakera / Presidio / PII) | ❌ | ✅ (plugin) | ✅ | ❌ |
| PII redaction | ✅ | ❌ | ✅ | ✅ | ❌ |
| Audit log + long retention | ✅ Postgres | ✅ | ✅ | ✅ | ✅ (SQLite + auto-cleanup) |
| **Live SSE log tail** | ✅ callback | ❌ | ✅ | ❌ | ✅ (toggle) |
| Analytics dashboard | ✅ | ✅ | ✅ | ✅ | ✅ Recharts |
| **Prometheus /metrics** | ✅ (callback) | ❌ | ✅ (native) | ✅ | ❌ (P10) |
| **OpenTelemetry traces** | ✅ | ❌ | ✅ | ✅ | ❌ (P10) |
| Alerts (webhook / Slack / PagerDuty) | ✅ | ✅ (3rd party) | ✅ | ✅ | ✅ webhook + builtin |
| Model mapping | ✅ | ✅ | ✅ | ✅ | ❌ |
| **Hot reload (no restart)** | ✅ yaml | ✅ env | ✅ API + UI | ✅ | ✅ **UpdateToken + /reload** |
| Web UI admin | ✅ React | ✅ Vue | ✅ React | ✅ Kong Manager | ✅ React + Recharts |
| SSO / SAML / OIDC | ✅ Enterprise | ❌ | ✅ Enterprise | ✅ | ❌ |
| RBAC | ✅ | ✅ (admin/user) | ✅ | ✅ | ✅ (admin/normal) |
| Multi-node master/slave | ✅ | ✅ (master/slave + Redis sync) | ✅ (cluster) | ✅ (CP/DP) | ❌ |
| **Streaming caps (timeout / body cap)** | ✅ | ❌ | ✅ | ✅ | ✅ (configurable) |
| **Broker subscriber cap (DoS protection)** | ✅ | ❌ | ✅ | ✅ | ✅ |
| Single binary deploy | ❌ | ✅ | ✅ | ❌ | ✅ |
| Docker / distroless | ✅ | ✅ | ✅ | ✅ | ✅ distroless |
| Multi-arch (amd64 / arm64) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Plugin system | ❌ | ❌ | ✅ | ✅ (strongest) | ❌ |
| Python SDK + Proxy server | ✅ | ❌ | ❌ | ❌ | ❌ |

## 3. Where llmRx is ahead ✨

1. **L4 Intent Classifier (Rust cdylib + cgo)** — None of the surveyed gateways expose an ONNX-backed intent classifier for free. LiteLLM offers content moderation but not a routing-signal classifier.
2. **L5 Thompson Sampling** — Only Bifrost advertises an RL router; implementation differs (Bifrost uses adaptive LB, llmRx uses Beta posterior with static-priority blending).
3. **Single binary + distroless + Rust ONNX feature flag** — One binary carries the web UI, the routing engine, and an optional classifier.
4. **Argon2id with transparent bcrypt + plaintext upgrade** — peer gateways store whatever the admin entered.
5. **Runtime-config atomic switch** — markup / breaker / retention / cost strategy / streaming caps / broker cap / alert cooldown change without restart.
6. **Live SSE log toggle** — explicit Live button + auto-pause of polling.
7. **Per-token / per-Plan spend tracking** with **atomic increment** (`UPDATE used_usd = used_usd + ?`) — no read-modify-write race.
8. **Streaming caps** — `stream_timeout_sec` (5 min default) + `stream_max_body_bytes` (32 MiB default); malformed upstream can't starve the gateway.
9. **Anthropic cache_control passthrough** — system blocks + message content blocks with `5m|1h|ephemeral`.
10. **Hot reload** — `UpdateToken` + global `POST /api/v1/reload` covers every cache layer.

## 4. Capability gaps (ranked, refreshed)

### Tier 1 — critical for enterprise

| Feature | Description | Priority | Doc |
|---|---|:---:|---|
| **Exact-match response cache** | Similar prompt → cached response, save latency + cost | ⭐⭐⭐ | `docs/P8-CACHING.md` |
| **Semantic cache** | Embedding-based similarity hit | ⭐⭐⭐ | parked P9+ |
| Guardrails (input) | PII / prompt-injection / jailbreak detection | ⭐⭐⭐ | parked |
| **Image / Rerank / Audio endpoints** | `/v1/images/generations` + `/v1/rerank` + `/v1/audio/*` | ⭐⭐⭐ | `docs/P9-MULTIMODAL.md` |
| **Prometheus `/metrics` + OTel** | Enterprise gating; commercial deployments | ⭐⭐⭐ | `docs/P10-OBSERVABILITY.md` |

### Tier 2 — operational

| Feature | Description | Priority | Doc |
|---|---|:---:|---|
| MCP gateway | Tool-call routing | ⭐⭐ | `docs/P11-MCP.md` |
| A2A agent gateway | Anthropic's A2A protocol | ⭐ | parked |
| Auto router | "cheap & fast" abstraction | ⭐ | parked |
| Image input passthrough | GPT-4V / Gemini Vision already works (Phase A); pure passthrough | ✅ | `docs/PASSTHROUGH.md` |

### Tier 3 — nice-to-have

| Feature | Description | Priority |
|---|---|:---:|
| Cluster mode | Multi-instance coordination | ⭐ |
| SSO / OIDC | Enterprise login | ⭐ |
| SDK integration test suite | Cross-SDK smoke tests | ⭐ |
| Token redemption code system | One-API parity | ⭐ |

## 5. Roadmap (post-survey)

| Phase | Doc | Status |
|---|---|---|
| P8 caching | `docs/P8-CACHING.md` | ⏳ next |
| P9 multimodal | `docs/P9-MULTIMODAL.md` | ⏳ |
| P10 observability | `docs/P10-OBSERVABILITY.md` | ⏳ |
| P11 MCP gateway | `docs/P11-MCP.md` | ⏳ |

## 6. Take-aways

* llmRx is currently a **lightweight + adaptive + single-binary + multi-tenant** play.
  It overlaps One-API / Bifrost in core "OpenAI-compatible relay" but
  pulls ahead on L4 + L5 ML routing, multi-tenant enforcement (per-token
  RPM/TPM + whitelists + spend), hot reload, and streaming caps.
* The biggest remaining gaps for an enterprise push are **caching**,
  **multimodal endpoints**, and **observability** — these are the next
  three milestones (P8-P10).
* Operational maturity (OTel, hot-reload, plugins) is closing fast;
  llmRx is now closer to "production-ready self-hosted gateway" than
  "single-process toy".