# llmRx

LLM API 智能路由网关。聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由（L1-L5）、用量计费和管理面板。

## 当前阶段：P0 闭环 ✅

P0 核心链路已可端到端跑通：

- ✅ Go 单二进制（chi + chi-cors + yaml.v3）
- ✅ YAML 配置驱动（channels + tokens）
- ✅ Channel Pool：轮询选 Key，跳过非 Active
- ✅ L1 Static + L2 Circuit Breaker + L3 Cost Optimizer
- ✅ OpenAI 兼容入口：`/v1/chat/completions` + `/v1/models` + `/health`
- ✅ Bearer Token 鉴权（来自 `cfg.tokens` 白名单）
- ✅ Provider adapter：单 OpenAI-compatible 实现（覆盖 deepseek / minimax / openai）
- ✅ 结构化日志输出（含路由路径、key 掩码、耗时、cost）
- ✅ Streaming / multi-protocol provider 显式拒绝，留 TODO

## 快速开始

```bash
# 构建（需要 Go 1.18+）
go build -o llmRx ./cmd/gateway

# 运行
./llmRx -config config.yml

# 调用
curl http://localhost:8787/health
curl -H 'Authorization: Bearer sk-test-token-123' \
     -H 'Content-Type: application/json' \
     -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}' \
     http://localhost:8787/v1/chat/completions
```

## 架构文档

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## 路线图

| Phase | 状态 | 内容 |
|---|---|---|
| P0 | ✅ | Go 骨架 + Provider 适配 + L1-L3 + `/v1/chat/completions` |
| P1 | ⏳ | SQLite 持久化 + Token/Plan/User + Management API |
| P2 | ⏳ | WebUI（Dashboard / Channels / Tokens / Users） |
| P3 | ⏳ | L3 多策略完善 + Analytics + 日志查询 |
| P4 | ⏳ | L4 Intent Classifier（Rust ONNX 模块，cdylib） |
| P5 | ⏳ | L5 Thompson Sampling 自适应权重 |
| P6 | ⏳ | Settings + 告警 + Dockerfile |
