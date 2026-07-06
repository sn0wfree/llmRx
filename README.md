# llmRx

LLM API 智能路由网关。聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由（L1-L5）、用量计费和管理面板。

## 当前阶段：P2 闭环 ✅

P0 + P1 + P2 已完成：管理控制台嵌入单二进制。

- ✅ Go 单二进制 + 嵌入式 React SPA（`go:embed`，零外部依赖）
- ✅ SQLite 单文件持久化（默认 `data/llmrx.db`，WAL + FK）
- ✅ YAML 配置驱动 → 启动时 seed 进 DB（首次启动）
- ✅ 管理控制台：`/admin/`（Dashboard / Channels / Tokens / Logs / Settings）
- ✅ 管理面 CRUD（Channels / Keys / Tokens / Users）
- ✅ 管理员登录 + 会话 token（cookie 或 `X-Session-Token`）
- ✅ Bearer Token 鉴权：内存 cache + DB 持久化
- ✅ 用量日志：每次请求写入 `logs` 表（含 token_id / cost / duration）
- ✅ Dashboard 聚合：channels、tokens、keys、logs、cost
- ✅ Channel Pool：轮询选 Key，跳过非 Active，热 reload
- ✅ L1 Static + L2 Circuit Breaker + L3 Cost Optimizer
- ✅ OpenAI 兼容入口：`/v1/chat/completions` + `/v1/models` + `/health`
- ⏳ Streaming：显式 501，留 TODO（P3 之前）
- ⏳ 多协议 provider / Plan / Markup / SSE 日志：留 P3+

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
```

### 重新构建前端

```bash
cd web
npm install
npm run build   # 输出到 web/dist/（已 symlink 复制到 internal/webui/dist/）
```

> 修改前端后需把 `web/dist/` 同步到 `internal/webui/dist/` 才能被 Go embed 收录：
> `cp -r web/dist/* internal/webui/dist/`

## 架构文档

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## 路线图

| Phase | 状态 | 内容 |
|---|---|---|
| P0 | ✅ | Go 骨架 + Provider 适配 + L1-L3 + `/v1/chat/completions` |
| P1 | ✅ | SQLite 持久化 + Token/Plan/User + Management API |
| P2 | ✅ | WebUI（React + Tailwind，go:embed） |
| P3 | ⏳ | L3 多策略完善 + Analytics UI + 日志查询 + Session TTL |
| P4 | ⏳ | L4 Intent Classifier（Rust ONNX 模块，cdylib） |
| P5 | ⏳ | L5 Thompson Sampling 自适应权重 |
| P6 | ⏳ | Settings + 告警 + Dockerfile + 密码 hash 升级 |
