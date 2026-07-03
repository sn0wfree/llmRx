# llmRx

LLM API 智能路由网关。聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由（L1-L5）、用量计费和管理面板。

## 快速开始

```bash
# 构建
go build -o llmRx ./cmd/gateway

# 运行
./llmRx -config config.yml
```

## 架构文档

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
