# P13+ 实现计划：5 大功能

> 2026-07-27 · 基于竞品调研的优先级排序

---

## 总览

| # | 功能 | 优先级 | 估算工作量 | 依赖 |
|---|---|---|---|---|
| 1 | 自动重试 + 指数退避 + 请求超时 | P0 | 2-3 天 | 无 |
| 2 | 100+ Provider 支持 | P0 | 1-2 天 | 无 |
| 3 | Guardrails 基础版 | P1 | 5-7 天 | 功能 1（重试交互） |
| 4 | Prometheus 指标 | P1 | 2-3 天 | 无 |
| 5 | Kubernetes Helm Chart | P2 | 3-5 天 | 功能 4（指标） |

---

## 功能 1：自动重试 + 指数退避 + 请求超时

### 1.1 现状分析

**当前问题**：
- 非流式请求**无超时**：`prov.Chat(r.Context(), ...)` 直接用 `r.Context()`，只有 chi 的全局 120s 和 `http.Client` 的 120s 硬编码
- 无自动重试：上游偶发 5xx 直接返回错误
- 流式有超时：`stream_timeout_sec` 配置（默认 5min），但非流式没有

**现有配置**：
- `stream_timeout_sec: 300`（仅流式）
- `breaker_max_failures: 5`（熔断器）
- `breaker_reset_timeout_ms: 30000`（熔断器重置）

### 1.2 架构设计

**方案：Decorator 模式** — 新增 `RetryingProvider` 包装现有 `Provider`：

```
internal/provider/retry.go    — RetryingProvider 结构体 + 指数退避逻辑
internal/provider/retry_test.go — 单元测试
```

**调用链变化**：
```
之前: prov.Chat(ctx, req, key, url)
之后: RetryingProvider{inner: prov}.Chat(ctx, req, key, url)
```

**重试策略**：
- 默认 `MaxRetries=0`（不重试，向后兼容）
- 可配置 `MaxRetries=3~5`
- 指数退避：`base_delay * 2^attempt`（base=500ms → 500ms, 1s, 2s）
- 可重试错误：5xx、timeout、connection reset、429（rate limit）
- 不可重试错误：4xx（除 429）、认证错误
- 429 特殊处理：读取 `Retry-After` header

**超时控制**：
- 新增 `request_timeout_sec` 配置（默认 60s）
- 在 `ChatCompletions` / `handleCombo` / `handleSerialCombo` 的 `prov.Chat` 调用前加 `context.WithTimeout`
- 流式请求保持现有 `stream_timeout_sec`

### 1.3 文件改动

| 文件 | 改动 | 行数 |
|---|---|---|
| `internal/provider/retry.go` | 新建：`RetryingProvider` + `isRetryable` + 指数退避 | +120 |
| `internal/provider/retry_test.go` | 新建：重试/超时/429 测试 | +80 |
| `internal/config/config.go` | `ServerConfig` 加 `RequestTimeoutSec`、`MaxRetries`、`RetryBaseDelayMs` | +10 |
| `internal/runtime/runtime.go` | `Defaults` 加对应原子字段 | +30 |
| `internal/api/router.go` | `providerFor` 返回 `RetryingProvider` 包装；3 处 `prov.Chat` 加 `context.WithTimeout` | +25 |
| **合计** | | **~265 行** |

### 1.4 配置示例

```yaml
# config.yml
server:
  request_timeout_sec: 60     # 非流式请求超时
  max_retries: 3              # 最大重试次数
  retry_base_delay_ms: 500    # 指数退避基数
```

---

## 功能 2：100+ Provider 支持

### 2.1 现状分析

当前 `providers.yaml` 有 9 个内置 provider。架构已支持三层加载（init() + YAML + DB），无需代码改动即可扩展。

**关键发现**：90% 的新 provider 是 OpenAI 兼容的，只需在 `providers.yaml` 加一个 YAML 条目。

### 2.2 扩展方案

**Phase 1（本 PR）**：扩展 `providers.yaml` 到 100+ 条目

新增 provider 列表（按类别）：

**国际主流**：
Together AI、Groq、Fireworks、Mistral、Cohere、Perplexity、xAI/Grok、Replicate、HuggingFace Inference、OpenRouter、Cerebras、SambaNova、DeepInfra、Lambda AI、Nebius、Novita AI

**国内厂商**：
百度文心、讯飞星火、百川、零一万物

**云厂商**：
Azure OpenAI（OpenAI 兼容端点）、AWS Bedrock（OpenAI 兼容端点）、Cloudflare Workers AI、Oracle OCI

**本地模型**：
Ollama、vLLM、LM Studio、Llamafile、Xinference

### 2.3 文件改动

| 文件 | 改动 | 行数 |
|---|---|---|
| `internal/provider/providers.yaml` | 从 9 个扩展到 100+ 个 | +350 |
| **合计** | | **~350 行** |

### 2.4 YAML 格式（每个 provider 4 行）

```yaml
- name: together
  display_name: Together AI
  protocol: openai
  base_url: https://api.together.xyz/v1
```

---

## 功能 3：Guardrails 基础版

### 3.1 现状分析

当前**完全没有任何内容级过滤**。现有的「白名单」是访问控制，不是内容安全。

### 3.2 架构设计

**新包**：`internal/guardrail/`

**请求流程中的拦截点**：
```
ChatCompletions()
  → [Input Guardrails: BeforeRequest]  ← 新增
  → RouteWith()
  → prov.Chat()
  → [Output Guardrails: AfterRequest]  ← 新增
  → writeJSON
```

**内置规则类型（Phase 1）**：
1. `regex_block` — 正则匹配阻断
2. `blocked_words` — 关键词/敏感词过滤
3. `content_length` — 最大/最小长度限制

**未来规则（Phase 2+）**：
4. `pii_detect` — PII 检测（邮箱、电话、身份证）
5. `regex_redact` — 正则替换脱敏
6. `prompt_injection` — 提示注入检测
7. `webhook` — 外部 HTTP 检查

### 3.3 数据模型

```go
// internal/model/guardrail.go
type GuardrailRule struct {
    ID          int64
    Name        string
    Type        GuardrailType    // "regex_block" | "blocked_words" | "content_length"
    Hook        string           // "input" | "output" | "both"
    OnFailure   string           // "deny" | "flag"
    Config      string           // JSON payload
    Priority    int              // 低 = 先执行
    Enabled     bool
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

### 3.4 文件改动

| 文件 | 改动 | 行数 |
|---|---|---|
| `internal/model/guardrail.go` | 新建：GuardrailRule + GuardrailType 常量 | +60 |
| `internal/guardrail/guardrail.go` | 新建：GuardrailEngine 接口 + BeforeRequest/AfterRequest | +80 |
| `internal/guardrail/checks.go` | 新建：3 个内置检查实现 | +120 |
| `internal/guardrail/guardrail_test.go` | 新建：单元测试 | +100 |
| `internal/store/sqlite.go` | 新增 guardrails + guardrail_events 表 + CRUD | +150 |
| `internal/store/store.go` | 新增 6 个接口方法 | +15 |
| `internal/api/router.go` | 在 ChatCompletions 中加 BeforeRequest/AfterRequest 钩子 | +30 |
| `internal/middleware/auth.go` | TokenInfo 加 GuardrailIDs 字段 | +5 |
| `internal/tokencache/tokencache.go` | Reload 时加载 guardrail 绑定 | +20 |
| `internal/admin/handler.go` | CRUD 4 endpoints + 事件查询 | +120 |
| `internal/webui/guardrails.go` | 新建：WebUI handlers | +100 |
| `internal/webui/templates/guardrails/` | 新建：列表 + 表单模板 | +120 |
| `internal/webui/handler.go` | 注册路由 | +10 |
| **合计** | | **~835 行** |

### 3.5 Config 示例

```yaml
# config.yml
guardrails:
  - name: "block_ssn"
    type: "regex_block"
    hook: "input"
    config: {"patterns": ["\\b\\d{3}-\\d{2}-\\d{4}\\b"]}
    on_failure: "deny"
```

---

## 功能 4：Prometheus 指标

### 4.1 现状分析

**已有数据**：`emitLog` 是所有请求的单一出口，已收集 model、status、duration、cost、tokens 等数据。**零 Prometheus 依赖**。

### 4.2 指标设计

**Counters（单调递增）**：
| 指标 | Labels | 说明 |
|---|---|---|
| `llmrx_requests_total` | `model`, `status`, `stream` | 请求总数 |
| `llmrx_billed_usd_total` | `model` | 总费用 |
| `llmrx_prompt_tokens_total` | `model` | 输入 token 总数 |
| `llmrx_completion_tokens_total` | `model` | 输出 token 总数 |
| `llmrx_upstream_errors_total` | `model`, `code` | 上游错误数 |
| `llmrx_rate_limit_blocks_total` | `reason` | 限流拒绝数 |
| `llmrx_retries_total` | `model` | 重试次数 |

**Histograms（分布）**：
| 指标 | Labels | 说明 |
|---|---|---|
| `llmrx_request_duration_seconds` | `model`, `stream` | 请求延迟 |

**Gauges（瞬时值）**：
| 指标 | 说明 |
|---|---|
| `llmrx_active_streams` | 当前活跃流式连接数 |
| `llmrx_channels_enabled` | 启用的 channel 数量 |
| `llmrx_tokens_active` | 活跃 token 数 |

### 4.3 架构设计

**Sidecar 模式**：独立端口暴露 `/metrics`，不与主 API 共用端口。

```
internal/observability/metrics.go   — Prometheus 指标定义
internal/observability/instrument.go — RecordRequest / StreamStart / StreamEnd 等函数
internal/observability/metrics_test.go — 测试
```

**Wiring**：
- `emitLog` 末尾调用 `observability.RecordRequest(...)`
- `streamChatCompletions` 加 `StreamStart()/defer StreamEnd()`
- `ratelimit` 拒绝时调用 `observability.RecordRateLimitBlock()`
- `provider/retry.go` 重试时调用 `observability.RecordRetry()`

### 4.4 文件改动

| 文件 | 改动 | 行数 |
|---|---|---|
| `internal/observability/metrics.go` | 新建：指标定义 | +80 |
| `internal/observability/instrument.go` | 新建：RecordRequest/StreamStart/StreamEnd | +60 |
| `internal/observability/metrics_test.go` | 新建：测试 | +50 |
| `internal/config/config.go` | `ServerConfig` 加 `MetricsAddr` | +3 |
| `internal/server/server.go` | 新增 `/metrics` 路由 + sidecar 服务器 | +40 |
| `internal/api/router.go` | `emitLog` 加 `observability.RecordRequest` | +5 |
| `internal/api/router.go` | `streamChatCompletions` 加 StreamStart/End | +5 |
| `internal/ratelimit/ratelimit.go` | 拒绝时加 `RecordRateLimitBlock` | +3 |
| `go.mod` | 加 `prometheus/client_golang` | +1 |
| `cmd/gateway/main.go` | 启动 metrics server | +10 |
| **合计** | | **~250 行** |

### 4.5 配置

```yaml
# config.yml
server:
  metrics_addr: "127.0.0.1:9090"   # "" = 禁用
  metrics_auth_token: ""            # "" = 无认证
```

---

## 功能 5：Kubernetes Helm Chart

### 5.1 现状分析

- `FROM scratch`，~13MB，无 shell
- 内置 `-healthcheck` 和 `HEALTHCHECK`
- 25s 优雅关闭，`terminationGracePeriodSeconds: 35`
- SQLite 单文件：只支持单副本写入
- 配置通过 `config.yml` + 环境变量

### 5.2 Helm Chart 结构

```
deploy/helm/llmrx/
  Chart.yaml
  values.yaml
  templates/
    _helpers.tpl
    deployment.yaml
    service.yaml
    configmap.yaml       — config.yml
    secret.yaml          — LLMRX_KEY_MASTER + API keys
    hpa.yaml             — 可选
    ingress.yaml         — 可选
    serviceaccount.yaml  — 可选
    NOTES.txt
  .helmignore
```

### 5.3 关键配置

```yaml
# values.yaml
replicaCount: 1  # SQLite 只支持单副本

image:
  repository: ghcr.io/sn0wfree/llmrx
  tag: "latest"

persistence:
  enabled: true
  size: 1Gi

config:
  server:
    trust_proxy_headers: true  # Ingress 后必须开启

secrets:
  existingSecret: ""  # 引用已有 Secret

autoscaling:
  enabled: false  # SQLite 限制

ingress:
  enabled: false
```

### 5.4 文件改动

| 文件 | 改动 | 行数 |
|---|---|---|
| `deploy/helm/llmrx/Chart.yaml` | 新建 | +15 |
| `deploy/helm/llmrx/values.yaml` | 新建 | +80 |
| `deploy/helm/llmrx/templates/_helpers.tpl` | 新建 | +20 |
| `deploy/helm/llmrx/templates/deployment.yaml` | 新建 | +80 |
| `deploy/helm/llmrx/templates/service.yaml` | 新建 | +25 |
| `deploy/helm/llmrx/templates/configmap.yaml` | 新建 | +30 |
| `deploy/helm/llmrx/templates/secret.yaml` | 新建 | +20 |
| `deploy/helm/llmrx/templates/ingress.yaml` | 新建 | +40 |
| `deploy/helm/llmrx/templates/hpa.yaml` | 新建 | +25 |
| `deploy/helm/llmrx/templates/NOTES.txt` | 新建 | +15 |
| `deploy/helm/llmrx/.helmignore` | 新建 | +5 |
| **合计** | | **~375 行** |

### 5.5 注意事项

- **SQLite 限制**：单副本写入，HPA 默认 `maxReplicas: 1`
- **未来多副本**：需迁移 PostgreSQL，或用 NFS 共享存储（性能差）
- **安全**：`securityContext.runAsUser: 1000`（binary 自动 drop privileges）

---

## 实施顺序

```
功能 1 (重试+超时)  ──┐
功能 2 (100+ Provider) ─┤── 并行，无依赖
功能 4 (Prometheus)  ──┘
                       │
                       ▼
功能 3 (Guardrails)  ──── 依赖功能 1（重试 + guardrail 交互）
                       │
                       ▼
功能 5 (K8s Helm)   ──── 依赖功能 4（Prometheus 指标）
```

**建议 PR 拆分**：
1. PR1：功能 1 + 功能 2（重试/超时 + 100+ provider）
2. PR2：功能 4（Prometheus）
3. PR3：功能 3（Guardrails）
4. PR4：功能 5（K8s Helm）

---

## 总工作量估算

| 功能 | 新文件 | 修改文件 | 总行数 |
|---|---|---|---|
| 1. 重试+超时 | 2 | 4 | ~265 |
| 2. 100+ Provider | 0 | 1 | ~350 |
| 3. Guardrails | 5 | 8 | ~835 |
| 4. Prometheus | 3 | 5 | ~250 |
| 5. K8s Helm | 11 | 0 | ~375 |
| **合计** | **21** | **18** | **~2,075 行** |
