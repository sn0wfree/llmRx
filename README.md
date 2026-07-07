# llmRx

LLM API 智能路由网关。聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由（L1-L5）、用量计费和管理面板。

## 当前阶段：P7+ 闭环 + 多租户 + 热重载 ✅

P0 → P7+ 全部完成，所有 L1-L5 路由层齐全，多协议 provider，运行时配置，SSE 实时日志，告警，Docker，多租户强制，热重载。

### 核心能力

#### 部署 / 持久化
- Go 单二进制 + 嵌入式 React SPA（`go:embed`，零外部依赖）
- SQLite 单文件持久化（默认 `data/llmrx.db`，WAL + FK）
- YAML 配置驱动 → 启动时 seed 进 DB（首次启动）
- Dockerfile 多阶段 + distroless/static:nonroot
- docker-compose 示例
- CI：test.yml（vet + race + 60% 覆盖门槛 + build）+ docker.yml（buildx 多架构 amd64+arm64 → ghcr.io）

#### 管理控制台 + API
- `/admin/`（Dashboard / Channels / Tokens / Logs / Analytics / **Settings**）
- 管理面 CRUD：Channels / Keys / Tokens / Users / Alerts / Plans
- 管理员登录 + 会话 TTL（24h 默认）+ 后台 5min 清理
- Bearer Token 鉴权：内存 cache + DB 持久化

#### 鉴权 / 安全
- **Argon2id** 密码 hash（m=64MiB, t=3, p=2）+ 旧明文 / bcrypt 自动透明升级
- 改密端点 `POST /api/v1/users/{id}/password`
- **渠道 API Key 静态加密**（AES-256-GCM）：master key 从 `LLMRX_KEY_MASTER`
  启动加载；旧明文行首次读取时 lazy migrate；错 key / 篡改密文会硬失败
- **多租户强制**：
  - Per-Token RPM/TPM 限速（in-process sliding window + middleware 429）
  - Per-Token 模型白名单 / IP 白名单（403）
  - Per-Token / Per-Plan spend 累计（`UPDATE used_usd = used_usd + ?` 原子操作）
  - Per-Plan markup 叠加（plan.MarkupRatio × channel markup）

#### 路由（L1-L5）
- **L1** static match
- **L2** circuit breaker（失败计数 + 半开恢复窗口；admin 改阈值即时生效）
- **L3** cost 策略（cheapest / fastest / balanced，运行时可切）
- **L4** Intent Classifier（Rust cdylib + cgo，keyword 后端默认，ONNX feature flag）
- **L5** Thompson Sampling 自适应权重（Beta 后验 + 冷启动门控 + 静态优先级 blend）

#### Provider 协议
- **多协议**：OpenAI / Anthropic / Gemini（per-channel `protocol` 字段）
- **完整 OpenAI 规范透传**（Phase A）：
  - `temperature` / `top_p` / `max_tokens` / `max_completion_tokens`
  - `tools` / `tool_choice` / `parallel_tool_calls`
  - `response_format`（text / json_object / json_schema）
  - `stream_options` / `seed` / `user` / `metadata` / `logit_bias` 等
  - 多模态 `content` 数组（text + image_url）
  - Anthropic 工具定义 + `cache_control` 块（Phase B）
- **缓存命中折扣**（Phase B）：`PromptTokensDetails.CachedTokens` 按 `channel.CachedInputDiscount` 计费

#### 流式 + 实时
- **SSE 流式聊天** OpenAI + Anthropic 原生 token-by-token
- **流式 caps**：`stream_timeout_sec`（默认 5 min ctx 取消）+ `stream_max_body_bytes`（默认 32 MiB 终止）
- **broker subscriber cap** 防止 SSE 订阅 DoS

#### 观测 / 运维
- 用量日志：每次请求写入 `logs` 表（含 token_id / cost / duration / cached_tokens）
- 日志过滤 UI + **SSE 实时尾随**（Live toggle）
- Analytics 时序图 + Top-N
- **告警子系统**：error_rate / p95_latency / cost_spike / key_exhausted + webhook + 站内事件 + ack
- **运行时配置**（markup / breaker / alert cooldown / retention / cost strategy / stream caps / broker cap）
- **日志保留**后台清理（24h ticker）
- **Settings 4 Tab**：Routing / Security / Alerts / Maintenance
- **热重载**：
  - Channel / Token / User CRUD 全部即时生效
  - `PUT /api/v1/tokens/{id}` 改限速 + 白名单
  - `POST /api/v1/reload` 强制全量重载（token cache / pool / router state / alert rules）

## 升级说明

### 旧密码 hash 自动升级

P6 用 bcrypt，P7 升级到 Argon2id。三种格式都被自动识别并透明升级：

| 格式 | 检测 | 处理 |
|---|---|---|
| `<hex_salt>:<plaintext>` (P6 之前) | 32 hex + `:` | 升级为 argon2id |
| `$2a$...` / `$2b$...` (P6) | bcrypt 前缀 | 升级为 argon2id |
| `$argon2id$...` (P7+) | argon2id 前缀 | 无需升级 |

升级触发：用户首次成功登录后，handler 用 `auth.Hash(password)` 重写 DB。

### 多协议 provider

每个 channel 的 `protocol` 字段决定走哪个 provider 适配器：

| `protocol` | Provider | 端点 |
|---|---|---|
| `openai` (默认) / `openai-compatible` | `OpenAIProvider` | `POST {base}/chat/completions` |
| `anthropic` / `anthropic-messages` | `AnthropicProvider` | `POST {base}/v1/messages` (`x-api-key` + `anthropic-version` header) |
| `gemini` / `google-gemini` | `GeminiProvider` | `POST {base}/v1beta/models/{model}:generateContent?key=...` |

### L4 Intent Classifier

可选 Rust 组件。默认不构建 cdylib——运行时用 `intent.Nop{}`（no-op）。启用：

```bash
make intent-rust   # cargo build --release
LLMRX_INTENT_LIB=internal/intent/rust/target/release/libllmrx_intent.so ./llmRx
```

后端默认 `keyword`（零依赖），feature flag `onnx` 切换 ONNX Runtime（需系统装 `libonnxruntime`）。Channels 通过 `intents` 字段声明支持的 intent 标签（`code` / `chat` / `summary` / `translate` / `math` / `general`），路由时 L4 把匹配的 channel 提到 L3 排序之前。

### 多租户 / 计费

每个 Token 可以绑定一个 Plan：
- Token 行有独立 RPM / TPM 限速
- `models_whitelist` / `ip_whitelist` 做精细授权
- 每次请求原子累加 `tokens.used_usd` 和 `plans.used_usd`
- Plan 的 `markup_ratio` 在 channel markup 之上再加一层

请求路径：
```
client → Bearer Auth → Whitelist check → RPM/TPM 限速
       → L1-L5 路由 → 上游 → 累加 spend → 推送日志
```

超限：HTTP 429 + `rate_limited`；不在白名单：HTTP 403。

### 缓存命中折扣

Anthropic 提示缓存命中时，上游会回 `usage.prompt_tokens_details.cached_tokens`。llmRx 自动按 channel 的 `cached_input_discount` 折扣计费：

| `cached_input_discount` | 含义 | 实际收费 |
|---|---|---|
| `0.1` (默认 / Anthropic 实际) | 付 10% | cached 段 `0.1 × input_price` |
| `0.0` | 免费 | 0 |
| `1.0` | 不打折 | `1.0 × input_price` |

### 热重载

```bash
# 改密 / 改限速 / 改白名单 / 启用 Token → 立即生效
curl -X PUT http://localhost:8787/api/v1/tokens/42 \
     -H 'X-Session-Token: ...' \
     -d '{"rpm":120,"tpm":100000,"models_whitelist":["gpt-4","claude-3"]}'

# 强制全量重载（手工改 DB 后 / k8s exec / 异常恢复）
curl -X POST http://localhost:8787/api/v1/reload \
     -H 'X-Session-Token: ...'
# → {"ok":true,"channels":true,"tokens":N,"alerts_reloaded":true}
```

`POST /reload` 同时刷新：token cache、channel pool、router 状态（breaker + Thompson 后验）、alert rules。

### SSE 实时日志

Logs 页头部 **Live** 切换。开启后通过 `EventSource` 接收 `/api/v1/logs/stream` 的推送（默认 polling 自动暂停）。EventSource 不能设置自定义 header，鉴权走 `?session_token=` 查询串。

### 告警

Settings → Alerts 增删改规则。规则类型：

| 类型 | threshold 含义 | 触发条件 |
|---|---|---|
| `error_rate` | 0..1 比率 | window 内 status≥400 占比 ≥ threshold（且样本 ≥ 5） |
| `p95_latency` | 毫秒 | window 内最慢 5% 平均值 ≥ threshold（且样本 ≥ 5） |
| `cost_spike` | 倍率 | 当前 window 成本 / 上 window 成本 ≥ threshold |
| `key_exhausted` | N/A | 任一 enabled channel 没有任何 active key |

触发后：写入 `alert_events` 表 + （可选）POST 到 `webhook_url`（JSON body）+ 日志输出。`cooldown_sec` 防止抖动。

### Docker

```bash
docker build -t llmrx:dev .
docker run -p 8787:8787 -v $(pwd)/data:/data -v $(pwd)/config.yml:/data/config.yml:ro llmrx:dev
# 或
docker compose up
```

## 快速开始

### 一键启动

```bash
# 1. 构建后端（Go 1.18+）
go build -o llmRx ./cmd/gateway

# 2. （可选）前端首次构建；dist 已随仓库提交，可直接跳过
# cd web && npm install && npm run build && cd ..

# 3. 生成 master key（32 字节 hex，用于 channel API key 静态加密）
export LLMRX_KEY_MASTER=$(openssl rand -hex 32)

# 4. 启动：首次会 seed 默认 admin（用户名 admin，密码见 server.admin_password）
./llmRx -config config.yml

# 5. 访问
# 管理控制台 → http://localhost:8787/admin/
# API 入口   → http://localhost:8787/v1/chat/completions
# 健康检查   → http://localhost:8787/health
```

> ⚠️ **生产环境必须设置 `LLMRX_KEY_MASTER`**，否则网关启动会直接退出。
> 仅本地开发可在 `config.yml` 中设 `secrets.dev_allow_plaintext_keys: true` 跳过该要求，
> 此时 channel key 以明文存储，**不要**在任何非本机部署中使用。

### 调用 OpenAI 兼容入口

```bash
# 限速 Token (RPM=60, TPM=100K)
curl -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}' \
     http://localhost:8787/v1/chat/completions

# Anthropic（自动按 channel.protocol 路由）
curl -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"claude-3-opus","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}' \
     http://localhost:8787/v1/chat/completions

# 流式
curl -N -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"hi"}]}' \
     http://localhost:8787/v1/chat/completions

# Tool call 透传
curl -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"gpt-4","tools":[{"type":"function","function":{"name":"get_weather","parameters":{}}}],"messages":[]}' \
     http://localhost:8787/v1/chat/completions
```

### 接收实时日志（SSE）

```bash
curl -N 'http://localhost:8787/api/v1/logs/stream?session_token=<admin_session>'
# event: log
# data: {"id":123,"channel_id":1,"model":"deepseek-chat","status_code":200,...}
```

### 管理 API（curl 直连）

```bash
# 登录
curl -X POST http://localhost:8787/api/v1/login \
     -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"admin"}'
# → {"session_token":"...","user":{...}}

# Dashboard / Channels / Logs
curl http://localhost:8787/api/v1/dashboard -H 'X-Session-Token: <token>'
curl http://localhost:8787/api/v1/channels   -H 'X-Session-Token: <token>'
curl 'http://localhost:8787/api/v1/logs?limit=20' -H 'X-Session-Token: <token>'

# Token CRUD
curl -X POST http://localhost:8787/api/v1/tokens -H 'X-Session-Token: <token>' \
     -H 'Content-Type: application/json' \
     -d '{"name":"prod","plan_id":1,"rpm":120,"tpm":100000,"models_whitelist":["gpt-4","claude-3"]}'
curl -X PUT http://localhost:8787/api/v1/tokens/2 -H 'X-Session-Token: <token>' \
     -H 'Content-Type: application/json' \
     -d '{"rpm":240,"status":1}'
curl -X DELETE http://localhost:8787/api/v1/tokens/2 -H 'X-Session-Token: <token>'

# 改密
curl -X POST http://localhost:8787/api/v1/users/1/password \
     -H 'X-Session-Token: <token>' -H 'Content-Type: application/json' \
     -d '{"old_password":"admin","new_password":"newpass123"}'

# 告警 CRUD
curl http://localhost:8787/api/v1/alerts -H 'X-Session-Token: <token>'
curl -X POST http://localhost:8787/api/v1/alerts -H 'X-Session-Token: <token>' \
     -H 'Content-Type: application/json' \
     -d '{"name":"high-errors","type":"error_rate","threshold":0.5,"window_sec":300,"cooldown_sec":60,"webhook_url":"https://example.com/hook","enabled":true}'

# 告警事件
curl http://localhost:8787/api/v1/alerts/events -H 'X-Session-Token: <token>'

# 运行时配置
curl http://localhost:8787/api/v1/config -H 'X-Session-Token: <token>'
curl -X PUT http://localhost:8787/api/v1/config -H 'X-Session-Token: <token>' \
     -H 'Content-Type: application/json' \
     -d '{"cost_strategy":"balanced","markup_ratio":1.2,"log_retention_days":30,"stream_timeout_sec":300}'

# 强制全量重载
curl -X POST http://localhost:8787/api/v1/reload -H 'X-Session-Token: <token>'
```

### 重新构建前端

```bash
cd web
npm install
npm run build   # 输出到 web/dist/（已 symlink 复制到 internal/webui/dist/）
```

> 修改前端后需把 `web/dist/` 同步到 `internal/webui/dist/` 才能被 Go embed 收录：
> `cp -r web/dist/* internal/webui/dist/`

## 开发与测试

```bash
# 跑全部测试
go test ./...

# race 检测
go test -race ./...

# 覆盖率（生成 /tmp/cov.out）
go test -coverprofile=/tmp/cov.out ./...
go tool cover -func=/tmp/cov.out | grep -E 'internal/(admin|api)/'

# 单跑 handler 测试
go test -v ./internal/admin ./internal/api

# Coverage gate 60%；当前 ~65%
```

测试走 `httptest` + chi 子路由装配，数据库用 `t.TempDir()` 起的临时 SQLite；provider 在 `testhelper` 中以 mock 注入，避免打外网。详见 [docs/ARCHITECTURE.md §14](docs/ARCHITECTURE.md)。

## 文档导航

| 文档 | 内容 |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 18 节架构设计（路由管线、broker、SSE、告警、L4/L5、缓存折扣、多租户、热重载） |
| [docs/COMPARATIVE.md](docs/COMPARATIVE.md) | vs LiteLLM / One-API / Bifrost / Kong 的能力矩阵 + 路线图 |
| [docs/PASSTHROUGH.md](docs/PASSTHROUGH.md) | OpenAI 规范字段透传审计（Phase A + B 已完成） |
| [docs/P8-CACHING.md](docs/P8-CACHING.md) | P8 响应缓存设计（精确 + 语义） |
| [docs/P9-MULTIMODAL.md](docs/P9-MULTIMODAL.md) | P9 Image / Rerank / Audio 端点设计 |
| [docs/P10-OBSERVABILITY.md](docs/P10-OBSERVABILITY.md) | P10 OpenTelemetry + Prometheus 设计 |
| [docs/P11-MCP.md](docs/P11-MCP.md) | P11 MCP gateway 设计 |
| [CHANGELOG.md](CHANGELOG.md) | 各 Phase 变更记录 |

## 路线图

| Phase | 状态 | 内容 |
|---|---|---|
| P0 | ✅ | Go 骨架 + Provider 适配 + L1-L3 + `/v1/chat/completions` |
| P1 | ✅ | SQLite 持久化 + Token/Plan/User + Management API |
| P2 | ✅ | WebUI（React + Tailwind，go:embed） |
| P3 | ✅ | Session TTL + 日志过滤 UI + Analytics 时序/Top-N + L3 策略运行时切换 + 自动 web-sync |
| P4 | ✅ | L4 Intent Classifier（Rust ONNX 模块，cdylib） |
| P5 | ✅ | L5 Thompson Sampling 自适应权重 |
| P6 | ✅ | bcrypt 密码 hash 升级 + 改密 UI + 告警子系统 + SSE 实时日志 + Settings 4 Tab + 运行时 markup + 日志保留 + distroless Dockerfile + Docker CI |
| P7+ | ✅ | 多协议 provider / SSE 流式响应 / argon2id 升级 + broker cap + streaming caps |
| Passthrough A | ✅ | OpenAI 完整规范透传（tools / tool_choice / response_format / 多模态 / 全套 knobs） |
| Passthrough B | ✅ | cache_control 翻译 + cached_tokens 计费折扣 |
| Hardening | ✅ | 多租户强制（RPM/TPM + 白名单 + per-token/plan spend）+ 热重载（UpdateToken + /reload） |
| **P8** | ⏳ | 响应缓存（精确 + 语义；Memory / SQLite / Redis backends） |
| **P9** | ⏳ | 多模态端点（`/v1/images/generations` + `/v1/rerank` + `/v1/audio/*`） |
| **P10** | ⏳ | OpenTelemetry + Prometheus `/metrics` |
| **P11** | ⏳ | MCP gateway（server + client 模式） |
| P12+ | ⏳ | 集群模式 / 多节点同步 |