# llmRx

LLM API 智能路由网关。聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由（L1-L5）、用量计费和管理面板。

## 当前阶段：P7+ 闭环 ✅

P0 → P7+ 全部完成。所有 L1-L5 路由层齐全，多协议 provider，运行时配置，SSE 实时日志，告警，Docker。

- ✅ Go 单二进制 + 嵌入式 React SPA（`go:embed`，零外部依赖）
- ✅ SQLite 单文件持久化（默认 `data/llmrx.db`，WAL + FK）
- ✅ YAML 配置驱动 → 启动时 seed 进 DB（首次启动）
- ✅ 管理控制台：`/admin/`（Dashboard / Channels / Tokens / Logs / Analytics / **Settings**）
- ✅ 管理面 CRUD（Channels / Keys / Tokens / Users / Alerts）
- ✅ 管理员登录 + 会话 TTL（24h 默认）+ 后台 5min 清理
- ✅ Bearer Token 鉴权：内存 cache + DB 持久化
- ✅ **Argon2id** 密码 hash（m=64MiB, t=3, p=2）+ 旧明文 / bcrypt hash 登录时透明升级
- ✅ **改密**端点 `POST /api/v1/users/{id}/password`
- ✅ 用量日志：每次请求写入 `logs` 表（含 token_id / cost / duration）
- ✅ 日志过滤 UI + **SSE 实时尾随**
- ✅ Analytics 时序图 + Top-N
- ✅ L3 cost 策略运行时切换
- ✅ **告警子系统**：error_rate / p95_latency / cost_spike / key_exhausted + webhook + 站内事件 + ack
- ✅ **运行时计费 markup 倍率**（Settings 改立即生效）
- ✅ **日志保留**后台清理
- ✅ **Settings 4 Tab**：Routing / Security / Alerts / Maintenance
- ✅ **SSE 流式响应**：`/v1/chat/completions` 的 `stream=true` 现在真正转发 token-by-token
- ✅ **L5 Thompson Sampling** 自适应权重（Beta 后验 + 冷启动门控 + 静态优先级 blend）
- ✅ **L4 Intent Classifier**（Rust cdylib + cgo，keyword 后端默认，ONNX 后端 feature flag）
- ✅ **多协议 provider**：OpenAI / Anthropic / Gemini（per-channel `protocol` 字段）
- ✅ **Dockerfile** 多阶段 + distroless
- ✅ **docker-compose** 示例
- ✅ **CI**：3 workflow
  - `test.yml`：vet + race test + 60% 覆盖门槛 + build + 可选 L4 cdylib
  - `docker.yml`（v\* tag 触发）：buildx 多架构 + push ghcr.io
- ✅ L1-L5 全路由层（Static / Breaker / Cost / Intent / Thompson）
- ✅ 100+ 测试用例 race-clean
- ⏳ 集群模式 / 多节点同步：P8+

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

### SSE 实时日志

Logs 页头部新增 **Live** 切换。开启后通过 `EventSource` 接收 `/api/v1/logs/stream` 的推送（默认 polling 自动暂停）。EventSource 不能设置自定义 header，鉴权走 `?session_token=` 查询串。

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

# 3. 启动：首次会 seed 默认 admin（用户名 admin，密码见 server.admin_password）
./llmRx -config config.yml

# 4. 访问
# 管理控制台 → http://localhost:8787/admin/
# API 入口   → http://localhost:8787/v1/chat/completions
# 健康检查   → http://localhost:8787/health
```

### 调用 OpenAI 兼容入口

```bash
curl -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}' \
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

# 拿 dashboard
curl http://localhost:8787/api/v1/dashboard -H 'X-Session-Token: <token>'
curl http://localhost:8787/api/v1/channels  -H 'X-Session-Token: <token>'
curl http://localhost:8787/api/v1/logs?limit=20 -H 'X-Session-Token: <token>'

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
     -d '{"cost_strategy":"balanced","markup_ratio":1.2,"log_retention_days":30}'
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
```

测试走 `httptest` + chi 子路由装配，数据库用 `t.TempDir()` 起的临时 SQLite；provider 在 `testhelper` 中以 mock 注入，避免打外网。详见 [docs/ARCHITECTURE.md §14](docs/ARCHITECTURE.md)。

## 架构文档

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## 路线图

| Phase | 状态 | 内容 |
|---|---|---|
| P0 | ✅ | Go 骨架 + Provider 适配 + L1-L3 + `/v1/chat/completions` |
| P1 | ✅ | SQLite 持久化 + Token/Plan/User + Management API |
| P2 | ✅ | WebUI（React + Tailwind，go:embed） |
| P3 | ✅ | Session TTL + 日志过滤 UI + Analytics 时序/Top-N（Recharts） + L3 策略运行时切换 + 自动 web-sync |
| P6 | ✅ | bcrypt 密码 hash 升级 + 改密 UI + 告警子系统（webhook + 站内） + SSE 实时日志 + Settings 4 Tab + 运行时 markup + 日志保留 + Dockerfile（distroless） + docker-compose + Docker CI（amd64+arm64） |
| P4 | ⏳ | L4 Intent Classifier（Rust ONNX 模块，cdylib） |
| P5 | ⏳ | L5 Thompson Sampling 自适应权重 |
| P7+ | ⏳ | 多协议 provider / SSE 流式响应 / argon2id 升级 / 集群模式 |
