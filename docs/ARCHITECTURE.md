# llmRx — LLM API 智能路由网关

> 2026-07-03 · 架构设计文档 v0.1

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
  rate_limit: 1000
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

*文档版本 v0.1 · 2026-07-03*
