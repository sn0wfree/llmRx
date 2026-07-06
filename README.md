# llmRx

LLM API 智能路由网关。聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由（L1-L5）、用量计费和管理面板。

## 当前阶段：P1 闭环 ✅

P0 + P1 已完成，端到端跑通：

- ✅ Go 单二进制（chi + chi-cors + yaml.v3 + go-sqlite3）
- ✅ SQLite 单文件持久化（默认 `data/llmrx.db`，WAL + FK）
- ✅ YAML 配置驱动 → 启动时 seed 进 DB（首次启动）
- ✅ 管理面 CRUD（Channels / Keys / Tokens / Users）
- ✅ 管理员登录 + 会话 token（cookie 或 `X-Session-Token`）
- ✅ Bearer Token 鉴权：内存 cache + DB 持久化
- ✅ 用量日志：每次请求写入 `logs` 表（含 token_id / cost / duration）
- ✅ Dashboard 聚合：channels、tokens、keys、logs、cost
- ✅ Channel Pool：轮询选 Key，跳过非 Active，热 reload
- ✅ L1 Static + L2 Circuit Breaker + L3 Cost Optimizer
- ✅ OpenAI 兼容入口：`/v1/chat/completions` + `/v1/models` + `/health`
- ✅ Provider adapter：单 OpenAI-compatible 实现
- ✅ 结构化日志输出（stdout JSON + DB）
- ⏳ Streaming：显式 501，留 TODO（P3 之前）
- ⏳ 多协议 provider / Plan / Markup 计算：留 P3

## 快速开始

```bash
# 构建（需要 Go 1.18+）
go build -o llmRx ./cmd/gateway

# 首次启动会 seed 默认 admin（username=admin，密码见 server.admin_password，默认 admin）
./llmRx -config config.yml

# 改密码：在 config.yml 加 server.admin_password: yourpassword

# 调用 OpenAI 兼容入口
curl http://localhost:8787/health
curl -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}' \
     http://localhost:8787/v1/chat/completions

# 管理面
curl -X POST http://localhost:8787/api/v1/login \
     -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"admin"}'
# → {"session_token":"...","user":{...}}

curl http://localhost:8787/api/v1/dashboard \
     -H 'X-Session-Token: <session_token>'
curl http://localhost:8787/api/v1/channels \
     -H 'X-Session-Token: <session_token>'
curl http://localhost:8787/api/v1/logs?limit=20 \
     -H 'X-Session-Token: <session_token>'
```

## 架构文档

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## 路线图

| Phase | 状态 | 内容 |
|---|---|---|
| P0 | ✅ | Go 骨架 + Provider 适配 + L1-L3 + `/v1/chat/completions` |
| P1 | ✅ | SQLite 持久化 + Token/Plan/User + Management API + Dashboard |
| P2 | ⏳ | WebUI（Dashboard / Channels / Tokens / Users，go:embed） |
| P3 | ⏳ | L3 多策略完善 + Analytics + 日志查询 UI |
| P4 | ⏳ | L4 Intent Classifier（Rust ONNX 模块，cdylib） |
| P5 | ⏳ | L5 Thompson Sampling 自适应权重 |
| P6 | ⏳ | Settings + 告警 + Dockerfile |
