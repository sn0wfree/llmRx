# 业界横向对比 — LLM API 网关竞品分析

> 2026-07-27 · 竞品调研文档 v0.1

---

## 目录

1. [调研项目概览](#1-调研项目概览)
2. [功能矩阵](#2-功能矩阵)
3. [llmRx 的独有优势](#3-llmrx-的独有优势)
4. [llmRx 的主要差距](#4-llmrx-的主要差距)
5. [功能差距优先级排序](#5-功能差距优先级排序)
6. [建议路线图](#6-建议路线图)

---

## 1. 调研项目概览

| 项目 | 语言 | 定位 | GitHub Stars | 核心卖点 |
|---|---|---|---|---|
| **LiteLLM** | Python | AI Gateway | 25k+ | 100+ provider SDK + Proxy Server |
| **Portkey** | Node.js | AI Gateway | 10k+ | 极低延迟（<1ms）+ 企业级 Guardrails |
| **Open WebUI** | Python | AI 平台 | 80k+ | Chat UI + RAG + 插件生态 |
| **llmRx** | Go | LLM API 网关 | 本项目 | L1-L5 智能路由 + SQLite 零依赖 |

### 各项目定位差异

- **LiteLLM**：最全面的 provider 适配层（100+），既是 Python SDK 又是 Proxy Server。侧重于「让任何 Python 项目无缝切换 provider」。YC W23 出品，Netflix/Google 等企业用户。
- **Portkey**：最轻量的企业级网关（122KB Node.js），强调 <1ms 延迟和 10B+ tokens/天的生产规模。侧重于「可靠性 + 安全 + 成本管控」。
- **Open WebUI**：最大的自托管 AI 平台，侧重于「Chat UI + RAG + 插件」，网关功能是副产品。
- **llmRx**：Go 原生，侧重于「智能路由 + 极简部署 + 零外部依赖」。

---

## 2. 功能矩阵

### 2.1 核心代理

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| OpenAI 兼容 API | ✅ | ✅ | ✅ | ✅ |
| `/chat/completions` | ✅ | ✅ | ✅ | ✅ |
| `/models` | ✅ | ✅ | — | ✅ |
| 流式 SSE | ✅ | ✅ | ✅ | ✅ |
| `/embeddings` | ✅ | ✅ | — | ❌ |
| `/images/generations` | ✅ | ✅ | ✅ | ❌ |
| `/audio` (TTS/STT) | ✅ | ✅ | ✅ | ❌ |
| `/responses` (Responses API) | ✅ | ✅ | — | ❌ |
| `/rerank` | ✅ | ✅ | — | ❌ |
| `/batches` | ✅ | ✅ | — | ❌ |

### 2.2 Provider 数量与协议

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| 支持 Provider 数 | **100+** | **45+** | 20+ | **9 内置 + 自定义** |
| OpenAI 兼容 | ✅ | ✅ | ✅ | ✅ |
| Anthropic 原生 | ✅ | ✅ | ❌ | ✅ |
| Gemini 原生 | ✅ | ✅ | ❌ | ✅ |
| Azure OpenAI | ✅ | ✅ | ✅ | ❌ |
| AWS Bedrock | ✅ | ✅ | ❌ | ❌ |
| 本地模型 (Ollama/vLLM) | ✅ | ✅ | ✅ | ❌ |
| BYOK (消费者自带 Key) | ✅ | ✅ | ✅ | 🔄 Phase 1.5 |

### 2.3 路由与负载均衡

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| 成本路由 (cheapest) | ✅ | ❌ | ❌ | ✅ L3 |
| 优先级路由 (fastest) | ✅ | ❌ | ❌ | ✅ L3 |
| 均衡路由 (balanced) | ✅ | ❌ | ❌ | ✅ L3 |
| 熔断器 (Circuit Breaker) | ✅ | ❌ | ❌ | ✅ L2 |
| Thompson Sampling 自适应 | ❌ | ❌ | ❌ | **✅ L5 (独有)** |
| 意图分类路由 (L4) | ❌ | ❌ | ❌ | **✅ (Rust native)** |
| 权重负载均衡 | ✅ | ✅ | ❌ | ✅ (via Priority) |
| Fallback 链 | ✅ | ✅ | ❌ | ✅ (Combo serial) |
| 自动重试 + 指数退避 | ✅ | ✅ | ❌ | ❌ |
| 请求超时控制 | ✅ | ✅ | ❌ | ❌ |

### 2.4 Token/Key 管理

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| Virtual Key | ✅ | ✅ | ✅ | ✅ |
| sk- 前缀 | ✅ | ✅ | — | ✅ |
| RPM 限流 | ✅ | ✅ | ❌ | ✅ |
| TPM 限流 | ✅ | ✅ | ❌ | ✅ |
| 模型白名单 | ✅ | ✅ | ✅ | ✅ |
| IP 白名单 | ✅ | ✅ | ❌ | ✅ |
| 过期时间 | ✅ | ✅ | ✅ | ✅ |
| 每 Key 费用预算 | ✅ | ✅ | ✅ | ✅ (via Plan) |

### 2.5 组合模型 (Combo / Model Groups)

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| 虚拟模型名 | ✅ (model group) | ✅ | ✅ | ✅ |
| 每 token 独立模型池 | ❌ (全局) | ❌ (全局) | ✅ (per-user) | **✅ (per-token)** |
| load_balance 模式 | ✅ | ✅ | ✅ | ✅ |
| serial fallback 模式 | ✅ | ✅ | ❌ | ✅ |
| parallel 模式 | ✅ | ❌ | ✅ | ⏳ (预留) |
| intent 模式 | ❌ | ❌ | ❌ | ⏳ (预留) |
| Per-combo strategy 覆盖 | ❌ | ❌ | ❌ | **✅ (独有)** |

### 2.6 计费与成本

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| 输入/输出价格配置 | ✅ | ✅ | ❌ | ✅ |
| markup_ratio 计费 | ❌ | ❌ | ❌ | ✅ |
| Plan 预算 (USD) | ✅ | ✅ | ✅ | ✅ |
| 原子 SQL 预算扣减 | ✅ | ✅ | ❌ | ✅ |
| 用量分析 (时序) | ✅ | ✅ | ✅ | ✅ |
| Top-N 分析 | ✅ | ✅ | ✅ | ✅ |
| Prompt caching 折扣 | ❌ | ❌ | ❌ | ✅ (CachedInputDiscount) |

### 2.7 安全

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| 密码 Hash | ✅ | ✅ | ✅ | ✅ (argon2id) |
| 密钥加密存储 | ✅ | ✅ | ✅ | ✅ (AES-GCM) |
| Master Key 加密 | ✅ | ✅ | — | ✅ |
| SSO / LDAP | ✅ | ✅ | ✅ | ❌ |
| RBAC 角色控制 | ✅ | ✅ | ✅ | ✅ (Admin/Root) |
| Guardrails | ❌ | **✅ (40+)** | ❌ | ❌ |
| PII 脱敏 | ❌ | ✅ | ❌ | ❌ |
| SOC2/HIPAA 合规 | ❌ | ✅ | ❌ | ❌ |

### 2.8 可观测性

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| 请求日志 | ✅ | ✅ | ✅ | ✅ |
| SSE 实时日志流 | ❌ | ❌ | ❌ | **✅ (独有)** |
| OpenTelemetry | ✅ | ✅ | ✅ | ❌ |
| Prometheus 指标 | ✅ | ✅ | ❌ | ❌ |
| Langfuse 集成 | ✅ | ✅ | ✅ | ❌ |
| 自定义 Webhook | ✅ | ✅ | ❌ | ✅ (Alert) |
| 告警规则引擎 | ❌ | ✅ | ❌ | ✅ |

### 2.9 Admin UI

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| Web 管理面板 | ✅ (React) | ✅ (Hosted) | ✅ (Svelte) | ✅ (Go template) |
| 仪表盘 | ✅ | ✅ | ✅ | ✅ |
| Channel 管理 | ✅ | ✅ | ❌ | ✅ |
| Token 管理 | ✅ | ✅ | ✅ | ✅ |
| Plan 管理 | ✅ | ✅ | ✅ | ✅ |
| User 管理 | ✅ | ✅ | ✅ | ✅ |
| Provider 管理 | ✅ | ✅ | ❌ | ✅ |
| 运行时配置 | ✅ | ✅ | ❌ | ✅ (热重载) |

### 2.10 部署与运维

| 功能 | LiteLLM | Portkey | Open WebUI | llmRx |
|---|---|---|---|---|
| Docker | ✅ | ✅ | ✅ | ✅ (~13MB scratch) |
| 数据库 | PostgreSQL | PostgreSQL | SQLite/PostgreSQL | **SQLite (零依赖)** |
| 镜像大小 | ~300MB | ~200MB | ~500MB | **~13MB** |
| 启动时间 | 30s+ | 10s+ | 15s+ | **<1s** |
| Terraform | ✅ | ❌ | ❌ | ❌ |
| Kubernetes | ✅ | ✅ | ✅ | ❌ |
| 诊断模式 | ❌ | ❌ | ❌ | **✅** |

---

## 3. llmRx 的独有优势

### 3.1 L1-L5 五层路由管线

业界最精细的路由实现：

| 层级 | 功能 | 业界对标 |
|---|---|---|
| L0 Combo | 每 token 虚拟模型池 | LiteLLM model group（但 llmRx 是 per-token） |
| L1 Static | 按模型名匹配 channel | 其他项目也有 |
| L2 Breaker | 熔断器：连续失败后排除 | LiteLLM 有基础版，Portkey 没有 |
| L3 Cost | 成本路由：cheapest/fastest/balanced | LiteLLM 有 cheapest，但无 balanced |
| L4 Intent | 意图分类路由（Rust native） | **业界独有** |
| L5 Thompson | Thompson Sampling 自适应权重 | **业界独有** |

**Thompson Sampling 的价值**：不需要手动调权重，系统自动学习每个 channel 的成功率。对新 channel 冷启动（Beta(1,1) 均匀先验），对稳定 channel 自动提升权重。

### 3.2 SQLite 零依赖

- **镜像 ~13MB**（LiteLLM ~300MB，Portkey ~200MB，Open WebUI ~500MB）
- **启动 <1s**（LiteLLM 30s+，Portkey 10s+）
- **无外部数据库**：不需要 PostgreSQL/Redis
- **单二进制部署**：`docker run -p 8787:8787 llmrx:local` 一条命令启动
- **内置诊断模式**：`-diagnose` 一键检查配置

### 3.3 组合模型 per-token 粒度

LiteLLM/Portkey 的 model group 是**全局**的——所有 token 共享同一组虚拟模型定义。llmRx 的 combo 是 **per-token** 的——每个 token 可以有独立的模型池和策略，适合多租户 SaaS 场景。

### 3.4 SSE 实时日志流

Admin UI 内置 SSE 实时日志推送，不需要额外部署 Grafana/Langfuse 等工具。其他项目都需要外部工具链。

### 3.5 Prompt Caching 折扣

内置 `CachedInputDiscount` 字段，自动计算缓存命中成本。支持 Anthropic/OpenAI GPT-5+ 等模型的 prompt caching 计费。其他项目需要手动配置。

### 3.6 告警规则引擎

内置告警规则（错误率、延迟、用量阈值）+ Webhook 推送 + 自动禁用。LiteLLM 没有告警功能。

---

## 4. llmRx 的主要差距

### 4.1 Provider 数量（最大差距）

llmRx 只有 9 个内置 provider，而 LiteLLM 有 100+，Portkey 有 45+。

**影响**：无法直接支持 Azure OpenAI、AWS Bedrock、Ollama 等常见场景。

**缓解**：llmRx 支持 OpenAI 兼容协议，大部分 provider 可以通过「OpenAI compatible」模式接入。但 Azure/Bedrock 等有特殊协议的 provider 不在此列。

### 4.2 API 端点覆盖

缺少 `/embeddings`、`/images/generations`、`/audio`、`/batches`、`/rerank`。

**影响**：不支持 embedding 搜索、图片生成、语音、批处理等场景。

### 4.3 Guardrails

Portkey 有 40+ 预置规则（内容安全、PII 检测、格式校验等），llmRx 完全没有。

**影响**：无法在网关层拦截敏感内容、检测 PII、校验输出格式。

### 4.4 可观测性

缺少 OpenTelemetry、Prometheus、Langfuse 集成。

**影响**：无法对接企业已有的监控栈（Grafana、Datadog、New Relic 等）。

### 4.5 自动重试 + 指数退避

LiteLLM/Portkey 都支持自动重试（最多 5 次 + 指数退避），llmRx 没有。

**影响**：上游偶发 5xx 时直接返回错误，不自动恢复。

### 4.6 SSO/LDAP

缺少企业级认证支持。

**影响**：大规模部署时需要外部认证层。

### 4.7 Kubernetes / Terraform

缺少云原生部署支持。

**影响**：在 K8s 集群中部署需要手动编写 Helm Chart 或 Deployment YAML。

---

## 5. 功能差距优先级排序

### P0 — 高价值 + 低成本

| 功能 | 原因 | 估算工作量 |
|---|---|---|
| 自动重试 + 指数退避 | 请求偶发失败时自动恢复，提升可用性 | 1-2 天 |
| `/embeddings` 端点 | 支持 embedding 搜索场景 | 1-2 天 |
| 请求超时控制 | 防止上游慢响应阻塞 | 0.5 天 |

### P1 — 高价值 + 中等成本

| 功能 | 原因 | 估算工作量 |
|---|---|---|
| Guardrails 基础版 | 内容安全 + PII 检测（可先做 5-10 个规则） | 3-5 天 |
| OpenTelemetry 集成 | 对接企业监控栈 | 2-3 天 |
| Azure OpenAI provider | 支持企业最常见的 Azure 部署 | 2-3 天 |
| Ollama 本地模型 | 支持私有化部署 | 1-2 天 |

### P2 — 中等价值 + 中等成本

| 功能 | 原因 | 估算工作量 |
|---|---|---|
| `/images/generations` 端点 | 支持图片生成 | 2-3 天 |
| `/audio` 端点 | 支持 TTS/STT | 3-5 天 |
| Parallel combo 模式 | 并发调用多个模型，第一个胜出 | 2-3 天 |
| Prometheus 指标 | 对接 Prometheus/Grafana | 1-2 天 |
| SSO/LDAP | 企业级认证 | 3-5 天 |

### P3 — 低价值 + 高成本

| 功能 | 原因 | 估算工作量 |
|---|---|---|
| AWS Bedrock provider | 支持 AWS 部署 | 5-7 天 |
| Kubernetes Helm Chart | K8s 部署支持 | 3-5 天 |
| Terraform 模块 | 云部署自动化 | 5-7 天 |
| SOC2/HIPAA 合规 | Enterprise 销售需要 | 10+ 天 |
| Langfuse 集成 | 对接 Langfuse 可观测性 | 2-3 天 |

---

## 6. 建议路线图

### 短期（1-2 个月）

1. **自动重试 + 指数退避**：在 `api/router.go` 的 `handleCombo` / `ChatCompletions` 中加入重试逻辑
2. **`/embeddings` 端点**：新增 Embedding handler，路由到支持 embeddings 的 channel
3. **请求超时控制**：`context.WithTimeout` 包裹 `prov.Chat` 调用
4. **Azure OpenAI provider**：新增 `azure_protocol.go`，复用 OpenAI 适配层

### 中期（2-4 个月）

5. **Guardrails 基础版**：实现 5-10 个预置规则（内容安全、关键词过滤、长度限制）
6. **OpenTelemetry 集成**：`otel` 包 + trace/span 自动注入
7. **Prometheus 指标**：`/metrics` 端点暴露请求数、延迟、错误率
8. **Parallel combo 模式**：`errgroup` + `context.WithCancel` fan-out
9. **Ollama 本地模型**：自动发现 + 适配

### 长期（4-6 个月）

10. **SSO/LDAP**：SAML 2.0 / OAuth 2.0 集成
11. **Kubernetes Helm Chart**：生产级 K8s 部署
12. **`/images`、`/audio` 端点**：多媒体 API 支持
13. **AWS Bedrock provider**：AWS 原生集成
14. **Terraform 模块**：AWS/GCP 一键部署

---

## 附录：数据来源

| 项目 | 数据来源 | 调研时间 |
|---|---|---|
| LiteLLM | GitHub README (2026-07-27) | 2026-07-27 |
| Portkey | GitHub README (2026-07-27) | 2026-07-27 |
| Open WebUI | GitHub README (2026-07-27) | 2026-07-27 |
| llmRx | 内部代码审查 (2026-07-27) | 2026-07-27 |

**注意**：本文档基于各项目的开源版本功能对比。企业版功能（如 Portkey 的 Guardrails、LiteLLM 的 SSO）可能需要付费授权。
