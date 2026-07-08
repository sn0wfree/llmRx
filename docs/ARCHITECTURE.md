# llmRx — LLM API 智能路由网关

> 2026-07-07 · 架构设计文档 v0.3 (P7+ 闭环 + Passthrough A/B + 多租户 + 热重载)

---

## 目录

1. [背景与目标](#1-背景与目标)
2. [竞品调研](#2-竞品调研)
3. [技术栈决策](#3-技术栈决策)
4. [系统架构](#4-系统架构)
5. [数据模型](#5-数据模型)
6. [路由管道详解](#6-路由管道详解)
7. [API 接口](#7-api-接口)
8. [WebUI 设计](#8-webui-设计)
9. [配置示例](#9-配置示例)
10. [项目路线图](#10-项目路线图)
11. [告警子系统](#11-告警子系统-p6)
12. [SSE 实时日志](#12-sse-实时日志-p6)
13. [运行时配置](#13-运行时配置-p6)

---

## 1. 背景与目标

自建 LLM API 智能路由系统，聚合多 provider API Key，对外提供 OpenAI 兼容的统一入口，支持 Token 分发、智能路由、用量计费和管理面板。

### 核心差异 vs 现有项目

| 维度 | 现有项目 | llmRx |
|------|---------|-------|
| 路由层 | L1-L3（静态/容错/成本） | **L1-L5 全量**，含 L4 意图感知 + L5 自适应权重 |
| 配额模型 | 抽象 quota 单位或纯美元 | **美元实价 + markup 倍率**，两者兼具 |
| 部署 | 通常需外部 DB | **SQLite 单文件**，可选 PostgreSQL |

### 核心能力

- **OpenAI 兼容入口** — `/v1/chat/completions` + `/v1/models`
- **Token 分发** — 支持 Plan 级预算、速率限制、模型/IP 白名单
- **智能路由** — L1 Static → L2 Circuit Breaker → L3 Cost → L4 Intent → L5 Adaptive
- **用量计费** — 美元实价 + 可配置 markup 倍率
- **管理面板** — 嵌入式 React SPA，单二进制部署

---

## 2. 竞品调研

### 2.1 项目横评

| 项目 | 本质 | 语言 | Stars | Token 管理 | 多 Provider | 智能路由 |
|------|------|------|-------|-----------|-------------|---------|
| LiteLLM | AI Gateway | Python | 18k+ | Virtual Key / 预算 / 费率 | 100+ | L1-L3 |
| One API | API 管理分发 | Go | 35k+ | 配额 / 倍率 / 分组 / IP | 30+ | L1 |
| New API | One API 增强版 | Go | 6k+ | 同上 + 格式互转 | 40+ | L1 |
| CCX | API Proxy | Go | 1k+ | 多 Key 轮换 / 熔断 | 4+ | L1-L2 |
| Cloudflare AIG | 托管 Gateway | CF Woker | - | 用量分析 / 缓存 | 12+ | L1-L2 |

### 2.2 可借鉴设计

| 来源 | 可借鉴点 |
|------|---------|
| One API | 三层权限 (User/Group/Token)；倍率计费模型；go:embed 嵌入方式 |
| LiteLLM | Virtual Key + spend tracking；budget reservation；provider adapter 模式 |
| CCX | 熔断 sliding window + success-driven recovery；多级优先级调度 |
| New API | 模型注册表；SQLite/PostgreSQL 双存储 |

---

## 3. 技术栈决策

| 维度 | 选择 | 理由 |
|------|------|------|
| 语言 | Go | 单二进制部署，高并发，go:embed 嵌入 Web UI |
| HTTP 框架 | chi | 轻量，标准 `net/http` 兼容，中间件生态好 |
| 协议 | 纯 OpenAI 兼容 | 所有 provider 统一转 OpenAI 格式 |
| 存储 | SQLite (默认) / PostgreSQL (可选) | 按需选，Store 层 interface 抽象 |
| 前端 | React + Tailwind CSS + Recharts | go:embed 嵌入 dist/，零外部依赖 |
| 配置 | YAML | 人类可读，支持环境变量插值 `${VAR}` |

---

## 4. 系统架构

### 4.1 整体架构

```
Access Layer: POST /v1/chat/completions + GET /v1/models (OpenAI 兼容)
    │
Router Pipeline (顺序执行):
    L1: Static Router       — model 名匹配 channel 组
    L2: Circuit Breaker     — 连续失败 → 熔断 → 半开 → 恢复
    L3: Cost Optimizer      — 同模型多 channel 选单价最低
    L4: Intent Classifier   — 分析 prompt → task_type → 推荐 model (P4)
    L5: Adaptive Weights    — Thompson Sampling 动态选 channel (P5)
    │
Channel Pool:
    DeepSeek / MiniMax / OpenAI / ... (各配 N 个 Key)
    + Health Check goroutine (30s 探测 / 动态上下线)
    │
Storage: SQLite / PostgreSQL
    │
Management API + Web UI (go:embed)
```

### 4.2 模块结构

```
cmd/gateway/
└── main.go                  # 入口

internal/
├── config/                  # YAML 配置加载
│   └── config.go
├── model/                   # 数据模型
│   └── types.go
├── server/                  # HTTP 服务器
│   └── server.go
├── middleware/              # HTTP 中间件
│   └── auth.go
├── router/                  # 路由引擎 (L1-L5)
│   ├── engine.go            # Pipeline 编排
│   ├── static.go            # L1: 静态匹配
│   ├── breaker.go           # L2: 熔断器
│   └── cost.go              # L3: 成本优化
├── pool/                    # Channel 连接池 + Key 管理
│   └── pool.go
├── provider/                # Provider 适配层
│   └── adapter.go
├── store/                   # 存储接口
│   └── store.go
└── api/                     # 管理 API + 代理端点
    └── router.go

config.yml                   # 配置文件
docs/
└── ARCHITECTURE.md          # 本文档
```

### 4.3 请求生命周期

```
1. HTTP 请求 → POST /v1/chat/completions
2. Bearer Token 解析 → 验证 Token 有效性
3. Router Pipeline:
   a. L1: model 名匹配 → 候选 channel 列表
   b. L2: 过滤已熔断 channel
   c. L3: 按 cost 策略排序
   d. L4: (若启用) intent 分类重路由
   e. L5: (若启用) Thompson Sampling 选最终 channel
4. Channel Pool → 选 Key (round-robin)
5. Provider Adapter → 转发请求到上游 API
6. 记录 Log → 更新用量 → 更新 L5 权重
7. 返回 OpenAI 格式响应
```

---

## 5. 数据模型

### 实体关系

```
User ──1:N──> Plan ──1:N──> Token
                  │
Channel ──1:N──> Key
Log ──> Token / Channel / Model
```

### 核心表

#### Channel — 渠道

| 字段 | 类型 | 说明 |
|------|------|------|
| id | int64 | 主键 |
| name | string | 唯一名称 |
| provider | string | 供应商标识 (deepseek/minimax/...) |
| base_url | string | API 基础地址 |
| models | []string | 支持的模型列表 (JSON) |
| priority | int | 优先级 (越大越高) |
| input_price_per_1m | float64 | 输入价格 ($/1M tokens) |
| output_price_per_1m | float64 | 输出价格 ($/1M tokens) |
| circuit_breaker | JSON | 熔断配置 |
| status | int | 0=未知 1=启用 2=禁用 3=自动断开 |

#### Key — API Key

| 字段 | 类型 | 说明 |
|------|------|------|
| id | int64 | 主键 |
| channel_id | int64 | 所属 Channel |
| key | string | 实际 API Key |
| key_masked | string | 脱敏显示 |
| status | int | 0=活跃 1=限流 2=禁用 |

#### Plan — 套餐

| 字段 | 类型 | 说明 |
|------|------|------|
| id | int64 | 主键 |
| name | string | 套餐名 |
| budget_usd | float64 | 总额度 ($) |
| used_usd | float64 | 已用 ($) |
| markup_ratio | float64 | 加价倍率 (1.0=原价, 2.0=2倍) |

#### Token — 访问令牌

| 字段 | 类型 | 说明 |
|------|------|------|
| id | int64 | 主键 |
| plan_id | int64 | 所属套餐 |
| key | string | 令牌值 (gw_sk_xxx) |
| name | string | 名称 |
| status | int | 0=活跃 1=禁用 2=耗尽 3=过期 |
| rpm | int | 每分钟请求上限 |
| tpm | int | 每分钟 Token 上限 |
| models_whitelist | []string | 允许的模型 (JSON) |
| ip_whitelist | []string | 允许的 IP (JSON) |
| expires_at | time | 过期时间 |

#### Log — 请求日志

| 字段 | 类型 | 说明 |
|------|------|------|
| id | int64 | 主键 |
| token_id | int64 | 使用的 Token |
| channel_id | int64 | 路由的 Channel |
| key_id | int64 | 使用的 Key |
| model | string | 模型名 |
| prompt_tokens | int | 输入 Token 数 |
| completion_tokens | int | 输出 Token 数 |
| real_cost_usd | float64 | 实际成本 ($) |
| billed_cost_usd | float64 | 计费金额 ($) |
| duration_ms | int64 | 耗时 (ms) |
| status_code | int | HTTP 状态码 |
| router_path | string | 路由路径 (e.g. "L3→L2→deepseek-main") |

### 计费公式

```
real_cost = (prompt_tokens / 1_000_000) × channel.input_price
          + (completion_tokens / 1_000_000) × channel.output_price

billed_cost = real_cost × plan.markup_ratio
```

---

## 6. 路由管道详解

### L1: Static Router — 静态匹配

根据请求中的 `model` 字段，匹配配置了该模型的 Channel。按 `priority` 降序排列。

```yaml
channels:
  - name: deepseek-main
    models: [deepseek-chat, deepseek-reasoner]
    priority: 10    # 高优先级
  - name: deepseek-backup
    models: [deepseek-chat]
    priority: 5     # 低优先级（备用）
```

### L2: Circuit Breaker — 熔断器

Sliding window 模式，每个 Channel 独立跟踪：

- 连续失败达到 `max_failures` (默认 5) → 熔断开路
- 熔断后经过 `reset_timeout` (默认 60s) → 半开，尝试恢复
- 任何成功请求 → 立即关闭熔断，重置失败计数

```
状态机:  Closed → (连续失败 ≥ N) → Open → (超时) → Half-Open → (成功) → Closed
                                                              → (失败) → Open
```

### L3: Cost Optimizer — 成本优化

三种策略：

| 策略 | 行为 | 适用场景 |
|------|------|---------|
| cheapest | 按 input + output 总价升序 | 成本敏感 |
| fastest | 按 priority 降序 (proxy for latency) | 延迟敏感 |
| balanced | price × 0.5 + priority × 0.5 加权评分 | 均衡 |

### L4: Intent Classifier — 意图分类 (P4)

从 prompt 文本推断任务类型，推荐合适的模型。

三种方案（渐进实施）：

| 方案 | 实现 | 延迟 | 准确率 | 依赖 |
|------|------|------|--------|------|
| A: 轻量 | regex + keyword | 0ms | ~70% | 无 |
| B: 中量 | embedding + kNN | 5-20ms | ~85% | ONNX 模型 |
| C: 重量 | LLM 自调用 | 200-500ms | ~90%+ | 需配分类模型 |

**task_type 分类体系** (8 类):

```
coding / reasoning / writing / translation / analysis
creative / extraction / cheap
```

用户可通过 `X-Task-Type: coding` 请求头显式指定，跳过分类。

### L5: Adaptive Weights — 自适应权重 (P5)

Thompson Sampling（多臂老虎机）：

- 每个 `(task_type, model, channel)` 三元组为一个"臂"
- 成功 (2xx) = reward 1.0
- 失败 = reward 0.0
- 用户反馈 = ±0.5
- 成本惩罚 = reward × (1 - cost_penalty_factor)
- Beta(α, β) 采样选臂
- ε = 0.1 探索率
- 每 5 分钟持久化到数据库

---

## 7. API 接口

### 7.1 代理端点 (OpenAI 兼容)

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/chat/completions` | 聊天补全 |
| GET | `/v1/models` | 模型列表 |
| GET | `/health` | 健康检查 |

### 7.2 管理端点

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/login` | 管理员登录 |
| GET | `/api/v1/dashboard` | 总览数据 |
| GET | `/api/v1/channels` | 渠道列表 |
| POST | `/api/v1/channels` | 新增渠道 |
| PUT | `/api/v1/channels/:id` | 更新渠道 |
| DELETE | `/api/v1/channels/:id` | 删除渠道 |
| POST | `/api/v1/channels/:id/keys` | 添加 Key |
| DELETE | `/api/v1/channels/:id/keys/:keyId` | 删除 Key |
| GET | `/api/v1/tokens` | Token 列表 |
| POST | `/api/v1/tokens` | 生成 Token |
| DELETE | `/api/v1/tokens/:id` | 撤销 Token |
| GET | `/api/v1/users` | 用户列表 |
| POST | `/api/v1/users` | 新增用户 |
| GET | `/api/v1/logs` | 日志查询 |
| GET | `/api/v1/logs/stream` | 实时日志 (SSE) |

---

## 8. WebUI 设计

### 8.1 页面结构

```
/admin
├── 📊 Dashboard             实时总览 (RPS/P95/今日成本/错误率)
├── 📡 Channels              渠道管理
│   ├── list                 渠道列表 + 健康状态
│   ├── :id                  编辑渠道
│   └── :id/keys             API Key 管理
├── 📦 Models                模型管理
│   └── registry             模型注册表 + 定价
├── 👥 Users                 用户管理
│   ├── list                 用户列表
│   └── :id                  用户详情
├── 🔑 Tokens                Virtual API Key
│   ├── list                 所有 Token 列表
│   └── create               生成 Token 浮层
├── ⚙️ Routing               路由规则 (L4-L5)
│   ├── intent               意图分类配置
│   ├── strategy             路由策略
│   └── overrides            手动规则覆盖 (后续)
├── 📈 Analytics             数据报表
│   ├── overview             总览
│   ├── by-model             按模型
│   ├── by-user              按用户
│   └── by-channel           按渠道
├── 📋 Logs                  请求日志
│   ├── realtime             实时流 (SSE)
│   └── search               历史查询
└── ⚙️ Settings              系统设置
```

### 8.2 技术选型

```
Go (go:embed) ──→ React SPA
                    ├ Tailwind CSS (界面)
                    ├ Recharts (图表)
                    └ 单二进制部署
```

### 8.3 组件树

```
src/
├── App.tsx
├── layouts/
│   └── AdminLayout.tsx
├── pages/
│   ├── Dashboard.tsx
│   ├── Channels/ChannelList.tsx
│   ├── Channels/ChannelDetail.tsx
│   ├── Tokens/TokenList.tsx
│   ├── Tokens/TokenCreate.tsx
│   ├── Users/UserList.tsx
│   ├── Users/UserDetail.tsx
│   ├── Routing/IntentConfig.tsx
│   ├── Analytics/Overview.tsx
│   ├── Logs/RequestLogs.tsx
│   └── Settings.tsx
├── components/ui/
│   ├── StatCard.tsx
│   ├── StatusBadge.tsx
│   ├── DataTable.tsx
│   ├── Modal.tsx
│   └── Toast.tsx
├── hooks/
│   ├── useSSE.ts
│   └── useAPI.ts
├── api/client.ts
└── types/index.ts
```

---

## 9. 配置示例

```yaml
server:
  port: 8787
  log_level: info

database:
  driver: sqlite
  dsn: data/llmrx.db

tokens:
  - key: sk-test-token-123
    name: test-token
    models: [deepseek-chat, deepseek-reasoner]

channels:
  - name: deepseek-main
    provider: deepseek
    base_url: https://api.deepseek.com/v1
    keys:
      - ${DEEPSEEK_API_KEY}
    models:
      - deepseek-chat
      - deepseek-reasoner
    priority: 10
    input_price_per_1m: 0.14
    output_price_per_1m: 0.42
    max_failures: 5
    reset_timeout_ms: 60000

  - name: minimax-main
    provider: minimax
    base_url: https://api.minimax.io/v1
    keys:
      - ${MINIMAX_API_KEY}
    models:
      - MiniMax-Text-01
    priority: 5
    input_price_per_1m: 0.10
    output_price_per_1m: 0.30
    max_failures: 5
    reset_timeout_ms: 60000
```

---

## 10. 项目路线图

| Phase | 内容 | 周期 |
|-------|------|------|
| P0 | Go 骨架 + Provider 适配 (deepseek/minimax) + L1-L3 路由 + `/v1/chat/completions` | 1-2 周 |
| P1 | SQLite 持久化 + Token/Plan/User 系统 + Management API | 1 周 |
| P2 | WebUI (Dashboard + Channels + Tokens + Users) + go:embed | 1 周 |
| P3 | L3 完善 (多策略) + Analytics 报表 + 请求日志查询 | 1 周 |
| P4 | L4 Intent Classifier (keyword 方案) + SSE 实时日志 | 1 周 |
| P5 | L5 Thompson Sampling Adaptive Weights | 1 周 |
| P6 | Settings + 告警 + 打磨 + Dockerfile | 1 周 |

**总计约 7-9 周（单人，熟悉 Go 的前提下）。**

### Phase 0 目标（当前阶段）

```
核心闭环: YAML 配置 → Channel Pool → L1-L3 路由 → OpenAI 代理 → 响应返回
```

P0 不做：
- ❌ 完整的用户/Token/Plan CRUD（YAML 白名单即可）
- ❌ 管理 API + WebUI
- ❌ Analytics / 日志查询
- ❌ L4 / L5 智能路由
- ❌ 数据库持久化（内存存储）

---

## 11. P0 实施笔记（2026-07-06）

P0 闭环已可端到端跑通。下面记录 P0 范围内的关键设计决策与已知限制，作为后续阶段的参照。

### 11.1 已落地的接线

| 模块 | 行为 |
|------|------|
| `cmd/gateway/main.go` | 启动时把 `cfg.Tokens` 构造成 `map[string]string`，注入到 server |
| `internal/server/server.go` | `/v1/chat/completions` 挂在 `authmw.Middleware(validTokens)` 下；`/v1/models` 与 `/health` 暂免鉴权便于本地调试 |
| `internal/middleware/auth.go` | 错误响应改为 OpenAI 兼容的 `error.{message,type,code}` 三元组；401/403/400 区分 |
| `internal/pool/pool.go` | round-robin 跳过 `KeyStatus != KeyActive`，整圈无 active 才返回 `ErrNoKey` |
| `internal/router/breaker.go` | 读取 `cfg.Channels[i].MaxFailures / ResetTimeoutMs`，缺省回落 5 / 60s；半开即清零失败计数 |
| `internal/router/cost.go` | balanced 改为 min-max 归一化 + 等权求和，避免原下标疑似错位 |
| `internal/api/router.go` | Provider 改为单一 `OpenAIProvider` 字段（覆盖所有 channel.Provider）；`stream=true` 显式 501；`Log` 不再丢弃，走 stdout JSON 行；errorType 按 HTTP 状态动态返回 |

### 11.2 请求生命周期（实测版）

```
1. POST /v1/chat/completions
2. authmw.Middleware        校验 Bearer → 401/403
3. decode ChatRequest       校验 body / model / stream
4. RouterEngine.Route       L1(static) → L2(breaker) → L3(cost)
5. pool.NextKey             轮询 + 跳过非 Active
6. provider.Chat            同步 HTTP → 上游 API
7. RecordSuccess / Failure  更新熔断器
8. emitLog                  stdout JSON line（含 router_path、cost、duration_ms）
9. writeJSON                返回 OpenAI 格式响应
```

### 11.3 P0 范围未做（明确留给后续阶段）

- 数据库 / 持久化（sqlite driver 在 go.mod 已预拉，仅未接线）
- Token CRUD / Management API（当前用 YAML 白名单）
- WebUI / 静态资源嵌入
- L4 Intent Classifier / L5 Thompson Sampling
- Streaming（SSE）响应
- Plan / Markup 计算（当前 `defaultMarkup=1.0`，实价 = 计费）
- 健康检查 goroutine（被动等请求触发熔断）

### 11.4 已知折中

- **Provider 单一实现**：`channel.Provider` 字段仅用于 `/v1/models` 的 `owned_by` 显示，所有 channel 都走 OpenAI-compatible 转发。如未来 deepseek/minimax 任一家引入非兼容协议，需按 channel 字段多路分发。
- **日志输出到 stdout**：未落库/未落文件，方便容器化与 pipe，但 P1 应改写为 sqlite 持久化 + 异步批量 flush。
- **Go 1.18 兼容**：当前构建以 Go 1.18 为基线，避免要求用户升级工具链。代码刻意避开了 1.19+ 的 `atomic.Uint64` 等。后续升级至 1.22 后可恢复原写法。
- **错误日志与请求日志混合**：chi middleware 输出 HTTP 访问日志，`emitLog` 输出业务日志，两者在 stdout 交错。P1 可引入结构化 logger（如 `log/slog`）统一。

### 11.5 Smoke Test 验收结果

| Case | 期望 | 实测 |
|------|------|------|
| `GET /health` | 200 | ✅ 200 `{"status":"ok"}` |
| `GET /v1/models`（无 auth） | 200 | ✅ 200 聚合 3 个 model |
| `POST /v1/chat/completions` 无 token | 401 | ✅ 401 `missing_authorization` |
| `POST` 错 token | 403 | ✅ 403 `invalid_token` |
| `POST` 空 model | 400 | ✅ 400 `missing_model` |
| `POST` 未知 model | 503 | ✅ 503 `no_channel` |
| `POST` stream=true | 501 | ✅ 501 `stream_unsupported` |
| `POST` 真实上游调用（fake key） | 上游 401 透传 | ✅ 透传 `Authentication Fails` |

---

*文档版本 v0.1 · 2026-07-03*
*追加 v0.1.1 · 2026-07-06 · P0 实施笔记*
*追加 v0.2 · 2026-07-06 · P1 实施笔记*
*追加 v0.3 · 2026-07-06 · P2 实施笔记 + httptest 测试基础设施*
*追加 v0.4 · 2026-07-06 · P3 实施笔记（Session TTL + Log 过滤 + Analytics + Recharts）*
*追加 v0.4.1 · 2026-07-06 · P3 收尾（自动 web-sync + L3 策略可调）*

---

## 12. P1 实施笔记（2026-07-06）

P1 范围（SQLite 持久化 + Token/Plan/User + Management API）已落地。

### 12.1 新增模块

| 包 | 作用 |
|---|---|
| `internal/store/sqlite.go` | SQLite 实现 `store.Store` 接口：6 表 schema + WAL + FK + 单连接 |
| `internal/tokencache/tokencache.go` | 线程安全 token 内存缓存，启动时载入，admin 修改后 `Reload()` |
| `internal/admin/handler.go` | Management API（login / dashboard / channels / keys / tokens / users / logs） |

### 12.2 Store 接口扩展

```go
// 在原 Store 接口上追加：
GetUserBySession(token string) (*model.User, error)
CountLogs() (int64, error)
LogStats() (LogStats, error)
```

### 12.3 Management API 表

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/v1/login` | 公开 | 返回 `session_token` + cookie |
| POST | `/api/v1/logout` | session | 清空 session |
| GET | `/api/v1/dashboard` | session | channels / tokens / logs / cost 聚合 |
| GET/POST | `/api/v1/channels` | session | 列表 / 新建 |
| PUT/DELETE | `/api/v1/channels/{id}` | session | 更新 / 删除（级联 keys） |
| GET/POST | `/api/v1/channels/{id}/keys` | session | 列出（脱敏）/ 新增 |
| DELETE | `/api/v1/channels/{id}/keys/{keyId}` | session | 删除 key |
| GET/POST | `/api/v1/tokens` | session | 列表（脱敏）/ 新建（返回明文一次） |
| DELETE | `/api/v1/tokens/{id}` | session | 撤销 |
| GET/POST | `/api/v1/users` | session | 列表 / 新建 |
| DELETE | `/api/v1/users/{id}` | session | 软删（status=99），admin(id=1) 保护 |
| GET | `/api/v1/logs?limit=&offset=` | session | 最近日志 |

### 12.4 Schema（SQLite）

```sql
channels(id, name UNIQUE, provider, base_url, models JSON, priority,
         input_price, output_price, circuit_breaker JSON, status,
         created_at, updated_at)
keys(id, channel_id FK CASCADE, key, key_masked, status,
     last_used_at, created_at)
tokens(id, plan_id, key UNIQUE, name, status, rpm, tpm,
       models_whitelist JSON, ip_whitelist JSON,
       expires_at, last_used_at, created_at)
plans(id, name, budget_usd, used_usd, markup_ratio, status,
      created_at, updated_at)
users(id, username UNIQUE, password_hash, role, status,
      session_token, created_at)
logs(id, token_id, channel_id, key_id, model, prompt_tokens,
     completion_tokens, real_cost_usd, billed_cost_usd,
     duration_ms, status_code, router_path, request_ip, created_at)
```

### 12.5 启动流程

```
1. config.Load
2. store.OpenSQLite + migrate（6 表 + 索引）
3. seed（仅在表为空时）：
   - admin 用户（username=admin，密码=cfg.Server.AdminPassword，默认 admin）
   - cfg.Tokens → tokens 表
   - cfg.Channels → channels + keys
4. pool.LoadFromStore(store)
5. tokencache.New(store) → 立即 Reload
6. router.New(store, pool)（store-backed breaker）
7. server.New + Start
```

### 12.6 Auth 双轨

| 路径 | 鉴权机制 |
|---|---|
| `/v1/chat/completions` | Bearer token，cache lookup（O(1)），fallback DB |
| `/v1/models`, `/health` | 公开（本地调试） |
| `/api/v1/*` | `X-Session-Token` header 或 `llmrx_session` cookie，DB `GetUserBySession` |

### 12.7 Pool / Breaker 热重载

- `pool.UpsertChannel / RemoveChannel / LoadFromStore`：管理 API 修改后即时刷新
- `router.ReloadChannel(channelID)`：清除 breaker entry 缓存，强制下次 Filter 重新读 `cfg.CircuitBreaker`
- `tokencache.Reload()`：token CRUD 后立即生效

### 12.8 P1 Smoke Test 结果（11 个 case 全绿）

| Case | 结果 |
|---|---|
| A. `/health` | 200 |
| B. chat 用 cfg-seeded token | 上游 401 透传 |
| C. admin login | 200 + session_token |
| D. 创建 token via API | 返回 id + 明文 key |
| E. chat 用新建 token | DB-backed lookup 找到 + 透传 |
| F. logs 含 **token_id** | log1 token_id=1, log2 token_id=2 |
| G. dashboard | 2 channels / 2 tokens / 2 logs / 2 errors |
| H. logout 后重用 session | 403 invalid_session |
| I. dashboard 无 session | 401 missing_session |
| J. dashboard 错 session | 403 invalid_session |
| K. create channel | 200 + 完整 channel |
| L. add key | key_masked=`sk-t***5678` |
| M. `/v1/models` 实时更新 | 新 model 出现 |
| N. delete channel | 200 |
| O. `/v1/models` 实时删除 | model 消失 |
| P. delete 主 channel | 200 + FK 级联删 keys |
| Q. chat 触发 503 | no_channel |

### 12.9 P1 已知限制

- **密码 hash**：`salt:plain` 字符串拼接，无 bcrypt/scrypt；仅适合单实例 + 内网部署，P6 应替换
- **Session token**：明文存 DB，无过期清理；P3 应加 TTL + 后台清理 goroutine
- **DeleteUser 软删**：`status=99` 而非真删，避免误操作；P3 可加硬删开关
- **Plan / Markup**：`billed_cost = real_cost * defaultMarkup(1.0)`，plan 表留 P3 接入
- **Channel reload 性能**：每次 Filter 都 `GetChannel`，可改为 per-request snapshot + TTL
- **无并发写保护**：DB 单连接 + WAL，但 admin 多个并发修改未加锁

---

## 13. P2 实施笔记（2026-07-06）

P2 范围（嵌入式 React 管理控制台）已落地，单二进制即可服务整套 UI。

### 13.1 新增模块

| 路径 | 作用 |
|---|---|
| `web/` | Vite + React 18 + TypeScript + Tailwind 3 前端项目 |
| `web/src/pages/{Login,Dashboard,Channels,Tokens,Logs}.tsx` | 5 个核心页面 |
| `web/src/components/{Layout,StatusBadge}.tsx` | UI 组件 |
| `web/src/api.ts` | fetch 封装 + session token 管理 + 类型 |
| `internal/webui/embed.go` | `//go:embed all:dist` 打包前端产物 |
| `internal/webui/dist/` | 构建产物（随仓库提交，避免每次 release 跑 npm） |

### 13.2 前端栈选择

| 维度 | 选择 | 理由 |
|---|---|---|
| 构建 | Vite 5 | 快、零配置、`base: '/admin/'` |
| UI | React 18 + TypeScript | 路线图原定 |
| 样式 | Tailwind 3 + 自定义 `btn/card/badge` 组件类 | 无运行时开销 |
| 路由 | URL hash（`#/dashboard`）+ 简单 state | 避免引入 react-router，单二进制不需 SSR |
| 图表 | 无（先用表格 + badge） | P3 加 Recharts 或 ECharts |
| 数据请求 | `fetch` 封装 + `localStorage` 存 session | 无 axios 依赖 |

### 13.3 embed 策略

```go
//go:embed all:dist
var distFS embed.FS

// Handler():
//   /admin/         → dist/index.html
//   /admin/foo/bar  → dist/foo/bar (SPA fallback if missing)
//   /admin/assets/x → dist/assets/x (MIME by ext)
```

不走 `http.FileServer` 而手写 `io.Copy` —— 避免 fileServer 对目录路径的 301 重定向循环（`/index.html` → `/` → `/index.html`）。

### 13.4 路由表（最终）

| 路径 | 来源 | 鉴权 |
|---|---|---|
| `GET /health` | server.go | 公开 |
| `POST /v1/chat/completions` | server.go | Bearer (tokencache) |
| `GET /v1/models` | server.go | 公开（本地调试） |
| `GET /admin/` 及其下所有 | webui.Handler | 公开 |
| `POST /api/v1/login` | admin.Handler | 公开 |
| `POST /api/v1/logout` | admin.Handler | session |
| `GET/POST/PUT/DELETE /api/v1/{channels,tokens,users,...}` | admin.Handler | session |

### 13.5 P2 Smoke Test 结果

| Case | 结果 |
|---|---|
| `GET /admin/` | 200 `text/html` 442B |
| `GET /admin` (无 /) | 200 `text/html` 442B |
| `GET /admin/index.html` | 200 `text/html` |
| `GET /admin/assets/index-*.css` | 200 `text/css` 15055B |
| `GET /admin/assets/index-*.js` | 200 `application/javascript` 164KB |
| `GET /admin/channels` (SPA fallback) | 200 `text/html` |
| `POST /api/v1/login` | 200 + session_token |

构建产物大小：CSS 15KB / JS 164KB（gzip 51KB），二进制仍 14MB（embed 不增加体积）。

### 13.6 P2 已知限制

- **路由简陋**：URL hash，无深链（如 `/admin/channels/123`），刷新无副作用
- **无 Charts**：dashboard 用 grid 数字卡，P3 加时序图
- **无 Realtime**：Dashboard 10s 轮询；Logs 5s；P3 接 SSE 流
- **无筛选/分页**：Logs 用 limit=100 + 客户端 filter；正式上线要加日期/channel/token 维度
- **前端无测试**：纯 UI，依赖后端管理 API smoke test 覆盖
- **dist 提交策略**：把 `internal/webui/dist/` 入 git；改前端后需手动 `cp -r web/dist/* internal/webui/dist/` 并 `go build`（已写进 README）—— P6 可加 Makefile 自动同步

### 13.7 P2 之后剩余路线

| Phase | 内容 | 预计 |
|---|---|---|
| P3 | L3 多策略完善 + Analytics UI + 日志查询 UI + Session TTL + SSE 日志流 | 1-1.5 周 |
| P4 | L4 Intent Classifier + Rust cdylib | 1 周 |
| P5 | L5 Thompson Sampling | 1 周 |
| P6 | Settings + 告警 + Dockerfile + 密码 hash 升级 + Makefile | 1 周 |

---

## 14. 测试基础设施（2026-07-06）

P2 收尾后补齐了 handler 层的 httptest 覆盖，方式是不起进程、纯进程内装配一个带 auth 中间件的 chi.Mux。

### 14.1 新增模块

| 包 | 作用 |
|---|---|
| `internal/testhelper/testhelper.go` | `New(t) *App`：temp-dir SQLite + 真实 pool/router/cache + admin/chat handler + mock provider；`AddChannel` / `AddChannelWithPrice` / `AddToken` 走 store 后再 `Reload()` 缓存 |
| `internal/admin/handler_test.go` | 11 个用例：login (bad creds / missing fields / success / logout invalidates) / dashboard 鉴权 / channels CRUD + 重名 4xx / keys 列表脱敏 / tokens 一次性返回明文 + cache 失效 / users 列表（password_hash 隐藏）/ admin(id=1) 删除保护 / logs 列表空数组 / invalid_session 403 |
| `internal/api/chat_test.go` | 9 个用例：无 auth 401 / bogus token 403 / missing model 400 / stream 501 / no channel 503 / invalid body 400 / happy path 断言 log 写入（token_id、model、tokens、status_code、request_ip、real_cost_usd>0）/ upstream 502 写入 fail log / `/v1/models` 聚合 / `/health` 200 |

### 14.2 Mock provider 注入

`provider.Provider` 在 `api.Handler` 中曾为私有字段，测试无法替换。新增 `SetProvider(p)` 暴露为对外方法（生产仍由 `server.go` 默认注入 `OpenAIProvider`），mock 在 `testhelper` 内实现 `Chat(req, apiKey, baseURL) (*ChatResponse, int, error)`，支持按调用序号选择预设 `Responses` / `Statuses` / `Errs`，并记录 `LastKey` / `LastURL` / `Calls` 供断言。

### 14.3 测试可测性收紧

| 调整 | 原因 |
|---|---|
| `internal/router/breaker.go` 的 `NewCircuitBreaker` 收窄入参为 `BreakerStore` interface（仅 `GetChannel(int64)`） | 测试用 fake 不必实现整套 `store.Store` |
| `internal/tokencache/tokencache.go` 的 `New` 收窄为 `TokenSource` interface（仅 `GetTokens`） | 同上 |
| `internal/api/router.go` 拆分出 `Routes() http.Handler` 暴露 `/chat/completions` + `/models` 子路由 | server 改为 `Mount("/v1", handler.Routes())`，测试可整体调用 `app.Mux` |
| `internal/admin/handler.go` 引入 `nonNil[T]` 泛型工具 | `data` 字段保证为 `[]` 而非 `null`，避免 SPA 端 `.map` 报错 |

### 14.4 顺手修的 bug

| Bug | 修复 |
|---|---|
| `admin.Login` 对空 `{}` body 返回 401（与"凭据错误"不可区分） | 显式校验 `username/password` 非空，缺失字段 400，错误凭据 401 |
| `channels/tokens/users/logs` 空列表在 JSON 中是 `null` 不是 `[]` | `nonNil` 在序列化前把 `nil` 切片转空切片 |
| `router.breaker.cfgFor` 在 `ch == nil` 时 NPE | 提前 return 默认值 |

### 14.5 覆盖率

| 包 | 之前 | 之后 |
|---|---:|---:|
| `internal/admin` | 0% | 66.8% |
| `internal/api` | 11.2% | 96.5% |
| `internal/config` | 100% | 100% |
| `internal/middleware` | 92.9% | 92.9% |
| `internal/store` | 67.1% | 67.1% |
| `internal/router` | 59.5% | 59.5% |
| `internal/pool` | 53.6% | 53.6% |
| `internal/tokencache` | 95.5% | 95.5% |

`ChatCompletions` 100% 行覆盖。`go test -race ./...` clean。`api` 包 9 个用例 + `admin` 包 11 个用例是这套测试的核心；handler 剩余未覆盖的 33% 主要在 channel update 的 `repo/JSON marshal`、`channel` 删除的级联 SQL 错误回滚等防御分支——继续通过更细的 mock 注入能再提升，但 P3 之后成本更划算。

### 14.6 选型决定（这次）

- **不引入 testify / gomock**：项目之前一直用 stdlib `testing` + `httptest`，新增 `testhelper` 沿用 `sync.Mutex` 手写 mock，避免一次性拉新依赖。后续如维护成本上升可切换。
- **不引入 dockertest / 真实 Postgres**：store.Store 是 interface，测试用 SQLite 走真实 SQL 路径，跨 store 实现的回归靠运行期 config。
- **fake user 密码格式**：`CreateUser` 写入 `seedsalt:admin`（冒号后为明文），admin.Login 的 `verifyPassword` 在冒号后做 `subtle.ConstantTimeCompare` —— 这是种子专用契约，正式 seed 在 `cmd/gateway/main.go` 用 cfg 注入 bcrypt。
- **测试 mux 复用**：`testhelper.App.Mux` 是 `chi.Mux` 而非 `http.Handler`，被新加的 webui 等路由不会自动出现——目前 admin/chat 各算 1 个 mount point，未来若 router 顺序变化需在 testhelper 同步。

### 14.7 CI

`.github/workflows/test.yml`（push/PR 到 master 触发）：

1. Go 1.22 + `goproxy.cn` mirror 拉依赖
2. `go vet ./...`
3. `go test -race -coverprofile=cov.out -covermode=atomic ./...`
4. 覆盖率门禁：total ≥ 50%（当前 70.1%）
5. `go build ./cmd/gateway`，上传二进制为 artifact

工作流中显式设了 `GOSUMDB=off`（与本地一致），避免 CI 上 `sum.golang.org` 不可达时 panic。

---

## 15. P3 实施笔记（2026-07-06）

P3 范围里选了 2/3/4：Session TTL + 后台清理、日志过滤、Analytics 报表 + Recharts。

### 15.1 Session TTL

| 模块 | 变更 |
|---|---|
| `internal/store/sqlite.go` | users 表加 `session_exp INTEGER` (ms 精度)；`addColumnIfMissing` 走 `PRAGMA table_info` 做幂等迁移；`GetUserBySession` 过滤 `session_exp = 0 OR session_exp > now`；`CleanupExpiredSessions()` 清空已过期行的 `session_token` |
| `internal/model/types.go` | `User.SessionExp *time.Time`（nullable） |
| `internal/admin/handler.go` | `Handler.sessionTTL`（默认 24h，可 `SetSessionTTL` 覆盖）；`Login` 写 `session_exp` + 设 cookie `Expires`；`Logout` 清 exp |
| `cmd/gateway/main.go` | `cleanupLoop` goroutine 每 5 min 跑 `CleanupExpiredSessions`；进程退出时随 main 终止 |

**精度坑**：`session_exp` 第一版用 `t.Unix()`（秒）写库，TTL=200ms 的测试恰好在下一整秒才查，导致 `session_exp > now` 判定为 `epoch_38 > epoch_38 == false`。改用 `t.UnixMilli()` 解决。`CreateLog` 同理不再覆盖 `CreatedAt`，仅在零值时填 `time.Now()`，便于测试种带时间的日志。

### 15.2 日志过滤

| 模块 | 变更 |
|---|---|
| `internal/store/store.go` | `LogFilter{TokenID, ChannelID, Model, StatusCode, CreatedFrom, CreatedTo, Limit, Offset}` + `QueryLogs(f) ([]Log, total, error)` |
| `internal/store/sqlite.go` | `buildLogWhere(f)` 抽出 WHERE 子句给 `QueryLogs` + `TimeSeries` + `topByField` 共用；`time.Time` 用 RFC3339 串 → `time.Parse` → `t.Unix()` |
| `internal/admin/handler.go` | `/api/v1/logs` 接受 `token_id`/`channel_id`/`model`/`status_code`/`from`/`to` 全部可选；返回 `data + total + limit + offset`；`logFilterFromQuery` 共享给 analytics |
| `web/src/pages/Logs.tsx` | 6 字段筛选（Token/Channel/Model/Status/From/To），`datetime-local` 转 RFC3339；`useEffect` 依赖 filter 触发 reload；移除客户端字符串匹配 |

### 15.3 Analytics

| 模块 | 变更 |
|---|---|
| `internal/store/store.go` | `SeriesPoint{Bucket, Requests, Errors, PromptTokens, CompletionTokens, RealCostUSD, BilledCostUSD}` + `NamedMetric{Label, Count, Tokens, Cost}` |
| `internal/store/sqlite.go` | `TimeSeries(f, bucketSec)` 用 `(created_at / bucket) * bucket` 做整桶对齐后 GROUP BY；`TopByModel`/`TopByChannel`/`TopByToken` 都委托 `topByField(field, ...)` |
| `internal/admin/handler.go` | `/api/v1/analytics/{timeseries,by-model,by-channel,by-token}` 4 个端点；`writeNamed` 收敛 3 个 top-N handler |
| `web/src/api.ts` | `analyticsTimeSeries` / `analyticsByModel` / `analyticsByChannel` / `analyticsByToken` + `SeriesPoint` / `NamedMetric` 类型 |
| `web/src/pages/Analytics.tsx` | Recharts `LineChart`（requests + errors + billed $ 双 Y 轴）+ 4 个统计卡 + 3 个 Top-N 卡 + `BarChart` 兜底；时间范围 1h/24h/7d/30d，桶大小 60/3600/86400 秒 |
| `web/src/components/Layout.tsx` + `App.tsx` | 导航加 📈 Analytics，挂到 `page === 'analytics'` |

### 15.4 P3 选型

- **不用 SSE / WebSocket**：P3 阶段 Recharts + 10s 客户端 reload 已够用；SSE 留到 P6 改造实时日志
- **recharts 体积**：JS bundle 164 KB → 567 KB（gzip 51 → 167 KB）。未做 code-splitting，必要时可 `import()` 切分
- **`topByField` 把 column 名拼 SQL**：column 来自代码字面量而非请求，避免 SQL 注入；后续扩展 `by-user` / `by-status` 只需新增一个 1 行包装
- **null `Label` → "(none)"**：`channel_id = 0`（admin 直接发起的请求）聚合时显示 "(none)"，不污染 N/A 字符串
- **`group_concat` 没用**：top-N 只要行数 + cost + tokens，单表 GROUP BY 够；如果后续要 by-(token, model) 二维会改用 CUBE 或预聚合

### 15.5 测试 & 覆盖率

| 包 | 之前 | 之后 |
|---|---:|---:|
| `internal/admin` | 66.8% | 68.3% |
| `internal/api` | 96.5% | 96.5% |
| `internal/store` | 67.1% | 49.3% |
| **total** | 70.1% | **63.8%** |

新增 10 个用例：
- 4 个 session TTL（`TestAdmin_SessionExpiry` / `TestAdmin_LoginPersistsSessionExp` / `TestAdmin_LogoutClearsSessionExp` / `TestAdmin_CleanupExpiredSessions`）
- 3 个日志过滤（by token / by channel+model / by date range）
- 3 个 analytics（`TestAdmin_AnalyticsTimeSeries` / `ByModel` / `ByChannel`）

`go test -race ./...` 干净。`go build` 出 14.4 MB binary，`/health` + 4 个新端点 smoke 验证通过（empty data 状态）。`store` 覆盖率下降是因为新加的 `TimeSeries` / `topByField` / `buildLogWhere` 等共享代码行数较多，相应测试仅覆盖 3 个 top-N 路径；后续可加 `store_test.go` 直接打 store 接口（不绕 admin handler）能再提升 15-20pp，但目前的 50% CI gate 仍稳过。

### 15.6 P3 收尾：L3 策略可调 + 自动化 web-sync

#### L3 cost strategy 运行时切换

| 模块 | 变更 |
|---|---|
| `internal/config/config.go` | `Config.Strategy.CostStrategy`（YAML 字段 `strategy.cost_strategy`） |
| `internal/router/cost.go` | `CostRouter.strategy` 加 `sync.RWMutex`；`SetStrategy(s)` 切换；`Strategy()` 读取；空值或未知值回落 `cheapest` |
| `internal/router/engine.go` | `Engine.SetStrategy` / `Engine.CostStrategy` 转发到 CostRouter |
| `internal/admin/handler.go` | `GET /api/v1/config` 返回当前策略；`PUT /api/v1/config` 校验 `cheapest|fastest|balanced` 后 `engine.SetStrategy` |
| `cmd/gateway/main.go` | 启动时若 `cfg.Strategy.CostStrategy != ""` 则注入到 engine |
| `web/src/pages/Settings.tsx` | 3 个 radio（cheapest/fastest/balanced）+ Save 按钮（dirty 状态检测） |
| `web/src/api.ts` | `getConfig()` / `updateConfig({cost_strategy})` |
| `web/src/App.tsx` | 移除 stub `Settings`，import 真实组件 |

#### 自动化 web-sync（解 P2.6 小坑）

之前改前端后必须手动 `cp -r web/dist/* internal/webui/dist/` 才能 `go build` 收录新前端。`Makefile` 重写后：

```
make build           # 默认入口
  → make web-sync
      → 比较 mtime: web/src 或 web/dist 新于 internal/webui/dist ?
          → 是: make web-build (npm run build) → cp -r web/dist → internal/webui/dist
          → 否: 跳过
      → 比较: internal/webui/dist 非空 ?
          → 是: 跳过
          → 否: 报 "web/dist missing and SKIP_WEB_SYNC not set"
  → go build -o llmRx ./cmd/gateway
```

| 变量 | 行为 |
|---|---|
| (默认) | 自动 rebuild + sync + go build |
| `SKIP_WEB_SYNC=1` | 跳过 npm + 跳过 cp，使用 `internal/webui/dist/` 现状 |
| (PATH 无 npm) | web-build 自动跳过（`HAS_NODE=0`），使用 committed dist |

新增 `build-go-only` target：纯 `go build`，不碰 web 链。CI 用这个（无 Node 环境）。

#### 16 测试 & 覆盖率（增量）

| 包 | P3 收尾前 | P3 收尾后 |
|---|---:|---:|
| `internal/admin` | 68.3% | 71.6% |
| **total** | 63.8% | **64.5%** |

新增 2 个用例：
- `TestAdmin_ConfigCostStrategyGetPut` — 默认 `cheapest` / PUT `fastest` / 引擎状态 / 非法值 400
- `TestRouter_CostStrategyAffectsRouting` — 引擎 `SetStrategy` 反映在 `Route()` 选择中

`go test -race ./...` 干净。smoke：smoke 跑 `/api/v1/config` GET → `cheapest`；PUT `balanced` → `balanced`；PUT `random` → 400。

`.github/workflows/test.yml`（push/PR 到 master 触发）：

1. Go 1.22 + `goproxy.cn` mirror 拉依赖
2. `go vet ./...`
3. `go test -race -coverprofile=cov.out -covermode=atomic ./...`
4. 覆盖率门禁：total ≥ 50%（当前 70.1%）
5. `go build ./cmd/gateway`，上传二进制为 artifact

工作流中显式设了 `GOSUMDB=off`（与本地一致），避免 CI 上 `sum.golang.org` 不可达时 panic。

---

## 11. 告警子系统 (P6)

### 11.1 数据模型

新增两张表：

```sql
alerts(id, name, type, threshold, window_sec, cooldown_sec,
       webhook_url, enabled, last_fired_at, created_at)

alert_events(id, alert_id, alert_name, alert_type, fired_at,
             payload, delivered_webhook, acknowledged)
```

`type` 为 `error_rate` | `p95_latency` | `cost_spike` | `key_exhausted` 之一。`payload` 存的是 JSON 编码的评估快照（指标名→值），用于回放/调试。

### 11.2 评估周期

`alert.Manager.Start(ctx)` 启动一个 goroutine，每 `TickInterval`（默认 30s）：

1. `Store.GetAlerts()` 重新读规则集（热生效，无需重启）
2. 遍历每条启用的规则，跳过 `LastFiredAt` 在 `cooldown_sec` 内的
3. `Evaluate(rule, now, store)` 返回 `(fired bool, payload map[string]any, err error)`
4. 触发后调用 `fire`：
   - 把 `webhook_url` 注入 payload（`channels/webhook` 不持有 alert 引用）
   - 投递到所有 `Channel`（builtin + webhook）
   - 写 `alert_events` 行
   - 原子更新 `alerts.last_fired_at = now.Unix()`

### 11.3 规则语义

| 类型 | threshold | 评估 SQL 概要 | 噪声门 |
|---|---|---|---|
| `error_rate` | 0..1 | `SUM(status>=400) / COUNT(*)` | samples ≥ 5 |
| `p95_latency` | ms | `AVG(duration_ms) over (top 5% DESC)` | samples ≥ 5 |
| `cost_spike` | 倍率 | `sum_cost(cur) / sum_cost(prev)` | prev > 0 且 cur > 0 |
| `key_exhausted` | N/A | 任意 enabled channel 无 active key | — |

注：SQLite 没有 `NTILE(20)`，`p95` 用 `ORDER BY DESC LIMIT N/20` 的近似。

### 11.4 投递

`Channel` 接口：

```go
type Channel interface {
    Name() string
    Deliver(ev *model.AlertEvent) error
}
```

- `builtin`：stdout 日志 + 总是返回 nil（事件表就是持久化）
- `webhook`：POST JSON 到 `payload["_webhook_url"]`，5s timeout，>=300 视为失败

### 11.5 防抖与重启

- 进程内：内存里的 `rules` 切片持有 `last_fired_at`，tick 内重复触发被挡
- 跨重启：DB 列 `alerts.last_fired_at` 是真值，重启后立即生效
- `cooldown_sec = 0` 表示「每次评估都允许触发」

---

## 12. SSE 实时日志 (P6)

### 12.1 设计目标

Logs 页提供「开箱即用的实时尾随」，无需客户端轮询、无外部依赖（无 Redis/NATS），单进程内完成。

### 12.2 数据通路

```
[chat pipeline emitLog]
       │
       ├──► Store.CreateLog(persist)        ◄── 持久化主路径（不变）
       │
       └──► broker.Broker[*model.Log].Publish(v)
                  │
                  ├──► subscriber A (SSE conn 1)
                  ├──► subscriber B (SSE conn 2)
                  └──► ... (有上限 256/conn, 满了踢)
```

`broker.Broker[T]` 是一个泛型 fan-out hub（Go 1.18 用 `any` 容器替代，编译期无类型校验；计划升级到 1.22+ 时切回 `*model.Log`）。

### 12.3 SSE 端点

`GET /api/v1/logs/stream` 走 `AdminOnly` middleware。EventSource 不能设自定义 header，所以 middleware 接受 `?session_token=` query 兜底：

```go
tok := r.Header.Get("X-Session-Token")
if tok == "" { tok = readCookie(r, "llmrx_session") }
if tok == "" { tok = r.URL.Query().Get("session_token") }
```

流格式：
```
: hello llmRx logs\n
                                 ← 心跳 (15s)
event: log\n
data: {"id":123,...}\n
\n
```

`internal/sse.Writer` 负责：
- 写 Content-Type: text/event-stream
- 立即 Flush 让客户端看到响应头
- 15s 发 `:ping` 注释帧（不少 reverse proxy 默认 60s 切断）
- ctx cancel 即退出（client 关闭 tab）

### 12.4 慢消费者

broker 单 subscriber 缓冲 256 条日志，满了直接丢并在 server 端 `log.Printf("warn: broker slow subscriber dropped")`。**不阻塞** publisher 是关键 — chat 路径不能因为有人在看实时面板而延迟。

### 12.5 前端

Logs 页头部新增 Live checkbox：
- 开启 → `new EventSource('/api/v1/logs/stream?session_token=...')`
- 收到 event → prepend 到 items 数组（限 500 条）
- 关闭 / unmount → `es.close()`

旧的 5s polling 自动让位（live=true 时不启动 interval）。

---

## 13. 运行时配置 (P6)

### 13.1 范围

P6 把以下配置改为可运行时调整（不需要重启）：

| 字段 | 类型 | 默认 | 范围 |
|---|---|---|---|
| `cost_strategy` | enum | cheapest | cheapest / fastest / balanced |
| `markup_ratio` | float | 1.0 | 0.01..1000 |
| `breaker_max_failures` | int | 5 | 1..1000 |
| `breaker_reset_timeout_ms` | int | 30000 | 100..86400000 |
| `alert_cooldown_sec` | int | 300 | 0..86400 |
| `log_retention_days` | int | 30 | 0..3650 |

`log_retention_days=0` 关闭清理循环；`markup_ratio<0.01` 被截到 1.0。

### 13.2 状态容器

新建 `internal/runtime.Defaults`：

```go
type Defaults struct {
    markupBits            uint64        // float64 bits, atomic
    BreakerMaxFailures    int64
    BreakerResetTimeoutMs int64
    AlertCooldownSec      int64
    LogRetentionDays      int64
    costStrategy          atomic.Value  // string
}
```

所有读路径走 atomic / `atomic.Value.Load()`，无锁。

### 13.3 生效范围

- `markup_ratio`：`api.Handler.Markup()` 每次请求读 — 立即生效
- `cost_strategy`：`h.router.SetStrategy(...)` 同步替换 — 立即生效
- breaker / alert / retention：当前是「参考值」，未来 channel 创建时读取作为默认；P6 阶段不破坏现有逻辑，只暴露面板

### 13.4 持久化

当前**不**持久化到 DB（避免 settings/config 表引入第二配置源）。重启后从 `config.yml` 重新加载。如需持久化，下一步可加 `kv_settings` 表 + 启动时 hydrate 到 `Defaults`。

---


---

## 14. 密码 hash 升级路径 (P7)

P6 用 bcrypt (cost=10)。P7 升级到 Argon2id (m=64MiB, t=3, p=2)。三类 hash 都被 `auth.Verify` 自动识别并标记 `NeedsUpgrade=true`：

| 格式 | 例子 | 检测方式 |
|---|---|---|
| Pre-P6 plaintext | `00112233445566778899aabbccddeeff:admin` | 32 hex + `:` |
| P6 bcrypt | `$2b$10$...` | 前缀 `$2a$` / `$2b$` / `$2y$` |
| P7+ Argon2id | `$argon2id$v=19$m=65536,t=3,p=2$...` | 前缀 `$argon2id$` |

升级路径：`auth.Hash(pw)` → DB UPDATE。**只在登录成功路径**触发，频率低；无后台迁移任务。

参数选择：`m=64MiB, t=3, p=2` 是 OWASP 2023 推荐的下界；单核 ~50ms hash，足够阻止离线爆破，又不让登录用户明显感知延迟。

## 15. SSE 流式响应 (P7)

P6 之前 `stream=true` 直接返 501。P7 引入 `provider.StreamingProvider` 接口，OpenAIProvider 和 AnthropicProvider 都实现。

**协议差异**：

- OpenAI: 标准 SSE，每行 `data: {json}`，最后 `data: [DONE]`。直接转发。
- Anthropic: `event: message_start` / `content_block_delta` / `message_delta` / `message_stop`。中间要翻译成 OpenAI chunk shape。
- Gemini: 非 SSE（HTTP/2 streaming），本期不支持 stream=true（会返 501）。

**实现细节**：

- `api.Handler.streamChatCompletions` 拿到 `provider.StreamingProvider` 后，组装 SSE 响应头，每 4 chunk flush 一次，错误时发单帧 `event: error`。
- broker 仍接收日志（流结束后一次性 emit，不 chunk-by-chunk）。
- 鉴权 / 中间件路径不变。

## 16. L5 Thompson Sampling (P7)

**模型**：每个 channel 一个 `Beta(α, β)` 后验。`α, β` 初值 `(1, 1)`（均匀）。每次成功 +1α，失败 +1β。

**采样**：

```text
θ_i ~ Beta(α_i, β_i)        // 采样成功率
score_i = (1-blend)·θ_i + blend·static_prio_i + explore_noise
```

`blend`（默认 0.3）防止完全靠后验，`explore`（默认 0.05）防止 posterior 收敛后停在局部最优。

**冷启动门控**：`MinSamplesPerChannel=5`。每个 channel 累积 < 5 个观察点之前，L5 不干预 L3 排序，保证冷启动期内行为可预测。

**数学细节**：Beta 采样用 gamma-marsaglia-twister；gamma 用 Marsaglia & Tsang 2000 算法 (shape≥1) + 幂律 boost (shape<1)。Go 1.18 标准库没自带 Beta/Gamma，所以内联实现。

## 17. L4 Intent Classifier (P7)

**架构**：

```
┌──────────────────┐         ┌──────────────────┐
│ Go (cgo)         │  dlopen │ Rust cdylib      │
│ internal/intent/ │ ──────> │ intent/rust/     │
│  - Classifier    │         │  - keyword score │
│  - Nop           │         │  - (opt) ONNX RT  │
└──────────────────┘         └──────────────────┘
```

**C ABI**（`llmrx_intent_classify`）：

```c
int32_t llmrx_intent_classify(const char* text, char* out, size_t out_cap);
const char* llmrx_intent_backend(void);
```

返回 JSON：`{"label":"code","score":0.7,"debug":[...]}`

**Go 集成**：

- `intent.Classifier` 接口：`Classify(text) Intent` / `Backend() string` / `Close() error`
- `intent.Nop{}` 默认实现（rust 缺失时 fallback）
- `router.RouteWith(ctx, model, RouteOptions{Text: lastUserMsg})` 把最后一个 user message 喂给 L4
- L4 命中 channel 提到 L3 排序之前

**冷启动 / 安全**：

- .so 缺失：log 警告，用 Nop，不报错
- ONNX 加载失败：fall back 到 keyword backend
- 模型未提供：keyword 后端能识别 5 个 label，覆盖 90% 常见 prompt

**构建**：

```bash
make intent-rust         # cargo build --release
LLMRX_INTENT_LIB=path ./llmRx
```

CI 上 `cargo` 可选——没有 Rust 时 skip 步骤，`go test` 仍跑通（Nop 默认）。

## 18. 多协议 Provider (P7)

**问题**：OpenAI 协议 ≠ Anthropic Messages API ≠ Gemini generateContent。强行统一代价大（丢失各家特性），保持隔离更稳。

**方案**：`provider.Factory(protocol)` 返回对应实现。`api.Handler.providerFor(channel.Protocol)` 路由时选。

| 字段 | OpenAI | Anthropic | Gemini |
|---|---|---|---|
| 端点 | `/chat/completions` | `/v1/messages` | `/v1beta/models/{m}:generateContent` |
| Auth | `Authorization: Bearer` | `x-api-key: ...` + `anthropic-version` | `?key=...` query |
| System prompt | message `role=system` | top-level `system` field | top-level `systemInstruction` |
| 流式 | SSE `data: {...}` | SSE `event: ...` / `data: ...` | (本期不支持) |
| 响应文本 | `choices[0].message.content` | `content[].text` (拼) | `candidates[0].content.parts[].text` (拼) |
| usage | `usage.{prompt,completion,total}_tokens` | `usage.{input,output}_tokens` | `usageMetadata.{promptTokenCount,...}` |

**测试**：`internal/provider/adapter_test.go` 用 `httptest.Server` 模拟各家响应，验证 200 路径 + 字段映射。

**Channel 协议字段**：

```yaml
- name: anthropic-prod
  provider: anthropic
  protocol: anthropic
  base_url: https://api.anthropic.com
  keys: [sk-ant-...]
```

`protocol` 缺省 = `openai`，向后兼容 P6 之前的 channel 定义。


---

## 19. 多租户强制（Multi-tenant Enforcement）

P6/P7+ 之后加入了 per-token / per-plan 的强制层，让 llmRx 真正可以作为多租户网关对外服务。

### 19.1 Token 模型扩展

```go
// model.Token 新字段
type Token struct {
    // 已有
    PlanID          int64
    RPM             int       // 每分钟请求数上限；0 = 无限
    TPM             int       // 每分钟 token 数上限；0 = 无限
    ModelsWhitelist []string  // 模型白名单；空 = 不限
    IPWhitelist     []string  // IP 白名单；空 = 不限
    
    // 新增
    UsedUSD         float64   // 累计账单（costed at billed price）
}

// model.Plan
type Plan struct {
    MarkupRatio float64  // 在 channel markup 之上再加一层（默认 1.0）
    BudgetUSD   float64  // 总预算；0 = 不限
    UsedUSD     float64
}
```

### 19.2 速率限制：滑动窗口

```
internal/ratelimit/ratelimit.go
- Limiter: map[tokenID]*bucket{ requests[]time.Time, tokens[]int }
- Allow(key, rpm, tpm, promptTokens) -> (allowed, reason)
- Account(key, extraTokens)        // emitLog 完成时回填 completion
- 窗口 60s，超过自动 evict
- 并发安全（sync.Mutex）
```

每个 token 一次请求调用一次 `Allow`；emitLog 完成时调用 `Account` 把 prompt+completion 加到 TPM 计数里。

### 19.3 中间件链

```
请求 →
  Token(lookup) 取出 TokenInfo（含 RPM/TPM/whitelists/PlanID）→
  WithLimits(enforcer) 检查 RPM/TPM →
  HasModelAccess / HasIPAccess 检查白名单 →
  路由 + 上游 →
  emitLog 写日志 + 累加 spend + Account 累加 TPM
```

`middleware/WithLimits` 在 Token 之上包装一层 `LimitEnforcer`：

```go
type LimitEnforcer interface {
    Allow(tokenID int64, rpm, tpm int, promptEstimate int) (bool, string)
}
```

`ratelimit.Limiter` 是默认实现，可以替换成 Redis-backed 版本。

### 19.4 计费叠加

```
real    = calcCost(channel, usage)       // channel pricing + cached discount
base    = real * MarkupRatio             // server-wide markup
billed  = base * Plan.MarkupRatio        // per-plan markup (if any)
```

`emitLog`：

1. 写一行 `logs`（含 `billed_cost_usd = billed`）
2. `IncrementTokenSpend(tokenID, billed)` 原子累加
3. `IncrementPlanSpend(planID, billed)` 原子累加
4. `Limits.Account(tokenID, prompt+completion)` TPM 回填

所有累计用 `UPDATE x.used_usd = x.used_usd + ?`，**无 read-modify-write 竞争**。

### 19.5 缓存命中折扣（与 18 节共用）

`usage.PromptTokensDetails.CachedTokens` 上游回包；`calcCost` 按 `channel.CachedInputDiscount` 计算 cached leg 折扣（见第 18 节）。

---

## 20. 热重载（Hot Reload）

所有 in-memory 状态都可以不重启进程地刷新。

### 20.1 自动重载（CRUD handler 内）

| 操作 | 自动刷新的缓存 |
|---|---|
| `POST /channels` / `PUT /channels/{id}` / `DELETE /channels/{id}` | `pool.LoadFromStore()` + `router.ReloadChannel()` |
| `POST /tokens` / `PUT /tokens/{id}` / `DELETE /tokens/{id}` | `tokens.Reload()` |
| `POST /users/{id}/password` | 失效该 user 所有 session |
| `PUT /config` | `runtime.Defaults` 原子切换 |

`UpdateToken` 是 P7+ 闭环时新加的（之前只有 Create/Delete）。改 `status=2`（disabled）→ 下一个请求该 token 直接 403。

### 20.2 手动全量重载

```
POST /api/v1/reload
Authorization: Bearer <admin_session>

→ 200 OK
{
  "ok": true,
  "channels": true,
  "tokens": 5,
  "alerts_reloaded": true
}
```

触发链：

```
ReloadAll() →
  1. tokens.Reload()                   // 重新读 tokens 表
  2. pool.LoadFromStore(store)         // 重新读 channels + keys
  3. router.ReloadAllChannels()        // 清空 breaker + Thompson
  4. alertMgr.Reload()                 // 重新读 alert 规则
```

用于：
- 手工 DB 改动后（psql / sqlite3 直接改）
- `kubectl exec` 进容器改 config 后
- 异常恢复（怀疑 cache 不一致时）

### 20.3 RouterEngine 的状态重置

```go
func (e *RouterEngine) ReloadChannel(id int64) {
    e.breaker.reload(id)   // closes the breaker entry
    e.thompson.Reset(id)   // back to uniform prior
}

func (e *RouterEngine) ReloadAllChannels() {
    e.breaker.reloadAll()  // clear every entry
    e.thompson.ResetAll()
}
```

`breaker.reload` 在 P7+ 之前只是「warm」；现在**真正把失败计数清零 + 关闭**——让 admin 改完配置后下一次请求立刻走新策略。

### 20.4 不重载的部分

| 状态 | 重启行为 |
|---|---|
| `runtime.Defaults` | 重新读 `config.yml` |
| `broker.Broker` subscriber set | 全部清空（已关闭的 SSE 客户端重新订阅） |
| `ratelimit.Limiter` | 全部清空（合理 — 重启后 60s 窗口已过期） |
| `tokencache.Cache` | `Reload()` 重新读 DB |

---

## 21. 路线图（Roadmap）

| Phase | 状态 | 文档 |
|---|---|---|
| P0-P3 | ✅ | git log |
| P4 Intent Classifier | ✅ | §13 |
| P5 Thompson Sampling | ✅ | §12 |
| P6 Hardening | ✅ | §15-17 |
| P7+ Multi-protocol + SSE | ✅ | §18 |
| Passthrough A/B | ✅ | `docs/PASSTHROUGH.md` |
| Multi-tenant + Hot reload | ✅ | §19, §20 |
| **P8 Caching** | ⏳ next | `docs/P8-CACHING.md` |
| **P9 Multimodal** | ⏳ | `docs/P9-MULTIMODAL.md` |
| **P10 Observability** | ⏳ | `docs/P10-OBSERVABILITY.md` |
| **P11 MCP** | ⏳ | `docs/P11-MCP.md` |
