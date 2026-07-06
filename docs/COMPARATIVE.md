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

## 2. Feature matrix

| Feature | LiteLLM | One-API | Bifrost | Kong AI | llmRx (now) |
|---|:---:|:---:|:---:|:---:|:---:|
| **Protocol coverage** | 100+ | 25+ | 23+ | 10+ | **3** |
| OpenAI-compatible API | ✅ | ✅ | ✅ | ✅ | ✅ |
| Streaming SSE | ✅ | ✅ | ✅ | ✅ | ✅ |
| Multi-protocol (Anthropic, Gemini) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Embeddings / Rerank / Audio / Images | ✅ | ❌ | partial | ✅ | ❌ |
| MCP gateway | ✅ (≥ v1.50) | ❌ | ✅ | ✅ | ❌ |
| A2A agent | ✅ (new) | ❌ | ❌ | ❌ | ❌ |
| Responses API | ✅ | ❌ | ❌ | ❌ | ❌ |
| Auto router (dynamic model pick) | ✅ | ❌ | ✅ | ✅ | partial (L4 keyword) |
| Tool/function call pass-through | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Semantic cache** | ✅ (Qdrant / Redis / Disk / S3 / GCS) | ❌ | ✅ (Redis) | ✅ | ❌ |
| **Exact-match response cache** | ✅ (Redis/s3/disk) | ❌ | ✅ | ✅ | ❌ |
| Anthropic prompt caching | ✅ | ❌ | ✅ | ❌ | ❌ |
| Virtual keys + limits | ✅ | ✅ | ✅ | ✅ | ✅ basic |
| Multi-tenant / groups | ✅ | ✅ | ✅ | ✅ | ❌ |
| Per-user billing | ✅ | ✅ (USD) | ✅ | ✅ | ❌ |
| Redemption code system | ❌ | ✅ | ❌ | ❌ | ❌ |
| Failover / auto-retry | ✅ | ✅ | ✅ | ✅ | ✅ (L2 breaker) |
| Load balancing | ✅ | ✅ (group) | ✅ (adaptive) | ✅ | ✅ (priority + strategy) |
| **Thompson / RL routing** | ❌ | ❌ | ✅ | ❌ | ✅ **L5** |
| **Intent classification L4** | ❌ | ❌ | ❌ | ❌ | ✅ **Rust ONNX** |
| Circuit breaker | ✅ | ✅ | ✅ | ✅ | ✅ |
| Rate limiting | ✅ (rpm/tpm) | ✅ (global) | ✅ | ✅ | ⚠️ config |
| Batch API | ✅ | ❌ | ❌ | ❌ | ❌ |
| Image generation | ✅ | ✅ | ✅ | ❌ | ❌ |
| **Guardrails (input/output)** | ✅ (Lakera / Presidio / PII) | ❌ | ✅ (plugin) | ✅ | ❌ |
| PII redaction | ✅ | ❌ | ✅ | ✅ | ❌ |
| Audit log + long retention | ✅ Postgres | ✅ | ✅ | ✅ | ✅ (SQLite + auto-cleanup) |
| **Live SSE log tail** | ✅ callback | ❌ | ✅ | ❌ | ✅ (toggle) |
| Analytics dashboard | ✅ | ✅ | ✅ | ✅ | ✅ Recharts |
| Prometheus / OTLP / OpenTelemetry | ✅ (callback) | ❌ | ✅ (native) | ✅ | ❌ |
| Alerts (webhook / Slack / PagerDuty) | ✅ | ✅ (3rd party) | ✅ | ✅ | ✅ webhook + builtin |
| Model mapping | ✅ | ✅ | ✅ | ✅ | ❌ |
| **Hot reload config** | ✅ yaml | ✅ env | ✅ API + UI | ✅ | ❌ (requires restart) |
| Web UI admin | ✅ React | ✅ Vue | ✅ React | ✅ Kong Manager | ✅ React + Recharts |
| SSO / SAML / OIDC | ✅ Enterprise | ❌ | ✅ Enterprise | ✅ | ❌ |
| RBAC | ✅ | ✅ (admin/user) | ✅ | ✅ | ✅ |
| Multi-node master/slave | ✅ | ✅ (master/slave + Redis sync) | ✅ (cluster) | ✅ (CP/DP) | ❌ |
| Single binary deploy | ❌ | ✅ | ✅ | ❌ | ✅ |
| Docker / distroless | ✅ | ✅ | ✅ | ✅ | ✅ distroless |
| Multi-arch (amd64 / arm64) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Tracing (OTel) | ✅ | ❌ | ✅ | ✅ | ❌ |
| Realtime cost tracking | ✅ | ✅ | ✅ | ✅ | ✅ |
| Response schema validation | ✅ | ✅ | ✅ | ✅ | ❌ |
| Cache-control headers (TTL / no-store / s-maxage) | ✅ | ❌ | ✅ | ✅ | ❌ |
| Backup / restore | ✅ pg_dump | ✅ | ✅ | ✅ | ⚠️ SQLite `cp` |
| Plugin system | ❌ | ❌ | ✅ | ✅ (strongest) | ❌ |
| Python SDK + Proxy server | ✅ | ❌ | ❌ | ❌ | ❌ |

## 3. Where llmRx is ahead ✨

1. **L4 Intent Classifier (Rust + cgo)** — None of the surveyed gateways expose an ONNX-backed intent classifier for free. LiteLLM offers content moderation but not a routing-signal classifier.
2. **L5 Thompson Sampling** — Only Bifrost advertises an RL router; implementation differs (Bifrost uses adaptive LB, llmRx uses Beta posterior with static-priority blending).
3. **Single binary + distroless + Rust ONNX feature flag** — One binary carries the web UI, the routing engine, and an optional classifier.
4. **Argon2id with transparent bcrypt + plaintext upgrade** — peer gateways store whatever the admin entered.
5. **Runtime-config atomic switch** — markup / breaker / retention / cost strategy change without restart.
6. **Live SSE log toggle** — explicit Live button + auto-pause of polling.

## 4. Capability gaps (ranked)

### Tier 1 — critical for enterprise

| Feature | Description | Priority |
|---|---|:---:|
| Semantic cache | Similar prompt → cached response, save latency + cost | ⭐⭐⭐ |
| Exact-match response cache | `(model, prompt hash, temperature)` hit | ⭐⭐⭐ |
| Multi-tenant (Users / Plans / Groups) | Group markups, per-tenant spend caps | ⭐⭐⭐ |
| Guardrails (input) | PII / prompt-injection / jailbreak detection | ⭐⭐⭐ |

### Tier 2 — operational

| Feature | Description | Priority |
|---|---|:---:|
| Hot-reload (channels/tokens) | No restart to pick up a new key | ⭐⭐ |
| OpenTelemetry + Prometheus | OTLP traces, /metrics endpoint | ⭐⭐ |
| MCP gateway | Tool-call routing | ⭐⭐ |
| Image generation | `/v1/images/generations` | ⭐⭐ |
| Plugin / middleware hook | let users extend | ⭐⭐ |

### Tier 3 — nice-to-have

| Feature | Description | Priority |
|---|---|:---:|
| Batch API | Async bulk requests | ⭐ |
| Auto router | "cheap & fast" abstraction | ⭐ |
| Embeddings endpoint | `/v1/embeddings` forward + cache | ⭐ |
| Audio (STT / TTS) | Transcription + speech | ⭐ |
| Rerank endpoint | `/v1/rerank` | ⭐ |
| SSO / OIDC | Enterprise login | ⭐ |
| Cluster mode | Multi-instance coordination | ⭐ |

## 5. Take-aways

* llmRx is currently a **lightweight + adaptive + single-binary** play.
  It overlaps One-API / Bifrost in core "OpenAI-compatible relay" but
  pulls ahead on L4 + L5 ML routing, neither of which have direct peers.
* The biggest gaps for an enterprise push are **caching + multi-tenant
  + guardrails** — these are table stakes in LiteLLM / Kong / Portkey.
* Operational maturity (OTel, hot-reload, plugins) is the second-tier
  gap; closing it lets llmRx escape "single-process" deployments.
