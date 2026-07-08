# llmRx 架构演进方案 v2

> 2026-07-08 · 纯 Go 架构 · 三个数据流 · 渐进迁移

---

## 目录

1. [决策摘要](#1-决策摘要)
2. [数据流](#2-数据流)
   - [整体架构图](#21-整体架构图)
   - [Flow 1：消费者用 llmRx Token](#22-flow-1消费者用-llmrx-token)
   - [Flow 2：管理员配置上游 Provider Key](#23-flow-2管理员配置上游-provider-key)
   - [Flow 3：BYOK 消费者用上游 Token（暂缓）](#24-flow-3byok-消费者用上游-token暂缓)
3. [三个数据流对比](#3-三个数据流对比)
4. [阶段计划](#4-阶段计划)
5. [BYOK 接口预留清单](#5-byok-接口预留清单)
6. [关键技术决策](#6-关键技术决策)
7. [详细设计索引](#7-详细设计索引)

---

## 1. 决策摘要

### 核心原则

> **Go 负责 LLM 代理，Python 不参与。**
> 内部管理面用 Go html/template + Tailwind CDN + HTMX CDN 实现。

| 维度 | 决策 |
|------|------|
| 架构 | 纯 Go（单进程 :8787） |
| 部署 | 单二进制、零 Python 依赖、零 npm 依赖 |
| 前端 | html/template（Go 标准库）+ Tailwind CDN + HTMX CDN |
| 数据库 | SQLite + WAL（已实现，需调优） |
| 加密 | AES-256-GCM（沿用现有） |
| 同步 | SQLite 为权威 + config.yml 备份 |
| 会话 | Cookie + SQLite session_token 字段 |

### 三个数据流

| 流 | 调用方 | Token 类型 | 入口 | 状态 |
|----|--------|-----------|------|------|
| **Flow 1** | 消费者（内部）| `sk-customer-xxx` | `/v1/chat/completions` | ✅ 实施 |
| **Flow 2** | 管理员（内部）| session cookie | `/admin/*` | ✅ 实施 |
| **Flow 3** | 消费者（白名单 IP）| `sk-openai-xxx`（上游 key）| `/v1/chat/completions` | ⏸️ 暂缓（仅留接口） |

---

## 2. 数据流

### 2.1 整体架构图

```
┌──────────────────────────────────────────────────────────────────────────┐
│                         llmRx System (单 Go 进程 :8787)                  │
│                                                                          │
│  ┌──────────────────┐                            ┌──────────────────┐   │
│  │ 消费者 (内部)    │                            │ Admin (内部)     │   │
│  │ sk-customer-xxx  │                            │ cookie session   │   │
│  └────────┬─────────┘                            └─────────┬────────┘   │
│           │ Flow 1                                       │ Flow 2      │
│           │ POST /v1/chat/completions                    │ /admin/*    │
│           │ Bearer sk-customer-xxx                       │             │
│           ▼                                             ▼              │
│  ┌──────────────────────────────────────────────────────────────┐       │
│  │ Chi Router                                                    │       │
│  │ ├── /v1/*           → apiHandler (Flow 1 + Flow 3)            │       │
│  │ └── /admin/*        → webui Handler (Flow 2)                 │       │
│  └─────────────────────────┬────────────────────────────────────┘       │
│                            │                                            │
│  ┌─────────────────────────▼────────────────────────────────────┐       │
│  │ 中间件链                                                      │       │
│  │ ├── WithLimits (token 验证 + RPM/TPM)                         │       │
│  │ ├── SessionMiddleware (admin 鉴权)                            │       │
│  │ ├── Logger / Recoverer / RealIP / Timeout / CORS              │       │
│  └─────────────────────────┬────────────────────────────────────┘       │
│                            │                                            │
│  ┌─────────────────────────▼────────────────────────────────────┐       │
│  │ 业务层                                                        │       │
│  │                                                               │       │
│  │ ┌─────────────────┐  ┌─────────────────┐  ┌────────────────┐ │       │
│  │ │ Flow 1          │  │ Flow 3 (BYOK)   │  │ Flow 2         │ │       │
│  │ │ Token Cache     │  │ BYOK Handler    │  │ admin handlers │ │       │
│  │ │   ↓             │  │   ↓             │  │   ↓            │ │       │
│  │ │ Limiter         │  │ Whitelist Check │  │ Channels CRUD  │ │       │
│  │ │   ↓             │  │   ↓             │  │ Tokens CRUD    │ │       │
│  │ │ Router L1-L5    │  │ Provider Detect │  │ Plans CRUD     │ │       │
│  │ │   ↓             │  │   ↓             │  │ Users CRUD     │ │       │
│  │ │ Pool.NextKey    │  │ TestKey         │  │ BYOK Admin     │ │       │
│  │ │   ↓             │  │   ↓             │  │ Alerts/Logs    │ │       │
│  │ │ Provider Call   │  │ AutoCreate      │  │ Config/Reset   │ │       │
│  │ └────────┬────────┘  └──────┬──────────┘  └───────┬────────┘ │       │
│  │          │                  │                     │          │       │
│  │          └──────────────────┼─────────────────────┘          │       │
│  │                             ▼                                │       │
│  │                    ┌────────────────────┐                    │       │
│  │                    │ In-Memory State    │                    │       │
│  │                    │ ├── tokencache     │                    │       │
│  │                    │ ├── pool (channels │                    │       │
│  │                    │ │  + byok_channels)│                    │       │
│  │                    │ ├── limiter        │                    │       │
│  │                    │ ├── breaker        │                    │       │
│  │                    │ ├── router L1-L5   │                    │       │
│  │                    │ ├── runtime        │                    │       │
│  │                    │ ├── broker (SSE)   │                    │       │
│  │                    │ └── secrets        │                    │       │
│  │                    └────────┬───────────┘                    │       │
│  └─────────────────────────────┼────────────────────────────────┘       │
│                                │                                        │
│  ┌─────────────────────────────▼────────────────────────────────┐       │
│  │ SQLite (WAL, 调优)                                            │       │
│  │ ├── channels         ←── Flow 2 写                            │       │
│  │ ├── byok_channels    ←── Flow 3 写 (接口预留, 暂不实现)       │       │
│  │ ├── keys             ←── Flow 2 写 (加密)                     │       │
│  │ ├── tokens           ←── Flow 2 写                            │       │
│  │ ├── plans            ←── Flow 2 写                            │       │
│  │ ├── users            ←── Flow 2 写                            │       │
│  │ ├── alerts           ←── Flow 2 写                            │       │
│  │ ├── logs             ←── Flow 1/3 写 (30 天保留)              │       │
│  │ ├── runtime_settings ←── Flow 2 写                            │       │
│  │ └── session_tokens   ←── Flow 2 写                            │       │
│  └──────────────────────────────────────────────────────────────┘       │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
                  │                                          │
                  │ HTTPS                                    │ HTTPS
                  ▼                                          ▼
        ┌──────────────────┐                        ┌──────────────────┐
        │ OpenAI           │                        │ Anthropic        │
        │ api.openai.com   │                        │ api.anthropic    │
        └──────────────────┘                        └──────────────────┘
            (Flow 1 + Flow 3 上游调用)
```

---

### 2.2 Flow 1：消费者用 llmRx Token

**调用方**：内部消费者（开发者 / 内部系统）
**Token**：`sk-customer-xxx`（llmRx 内部生成，由 admin 分配）
**入口**：`POST /v1/chat/completions`，`Authorization: Bearer sk-customer-xxx`

**时序图**：

```
Consumer                 Go Gateway                      Upstream LLM
   │                          │                                │
   │ POST /v1/chat/completions │                                │
   │ Bearer sk-customer-xxx    │                                │
   ├─────────────────────────→│                                │
   │                          │ [1] tokencache.Lookup()        │
   │                          │     → token {id:42, plan:1}    │
   │                          │ [2] check status/expiry/ip/wl  │
   │                          │ [3] ratelimit.Allow(rpm,tpm)   │
   │                          │ [4] RouterEngine.RouteWith()   │
   │                          │     L1: 找支持 gpt-4 的 channel│
   │                          │     L2: 过滤熔断              │
   │                          │     L3: 成本排序               │
   │                          │     L4: 意图识别               │
   │                          │     L5: Thompson 采样          │
   │                          │     → channel: deepseek-main   │
   │                          │ [5] pool.NextKey(channel_id)   │
   │                          │     → key: sk-deep-xxx (加密)  │
   │                          │     secrets.Decrypt()          │
   │                          │                                │
   │                          │ POST /chat/completions         │
   │                          │ Bearer sk-deep-xxx             │
   │                          ├───────────────────────────────→│
   │                          │                                │
   │                          │ 200 OK + OpenAI 格式响应        │
   │                          │←───────────────────────────────┤
   │                          │ [6] emitLog()                  │
   │                          │     - 计算 cost                │
   │                          │     - 写 logs 表               │
   │                          │     - 累加 plan.used_usd        │
   │ 200 OK + OpenAI 格式      │                                │
   │←─────────────────────────┤                                │
```

**关键点**：
- Token 由 admin 预创建并分配给用户
- 消费者不感知上游 provider
- 限流、按 plan 计费、model 白名单都在此流程

---

### 2.3 Flow 2：管理员配置上游 Provider Key

**调用方**：内部管理员
**鉴权**：Session cookie
**入口**：`/admin/*`

**时序图**：

```
Admin                    Go Gateway                       SQLite
   │                          │                              │
   │ POST /admin/login        │                              │
   │ username + password      │                              │
   ├─────────────────────────→│                              │
   │                          │ [1] auth.Verify(hash, pwd)    │
   │                          │ [2] store.GetUserByName()     │
   │                          ├─────────────────────────────→│
   │                          │←─────────────────────────────┤
   │                          │ [3] 生成 session_token        │
   │                          │     写 user.session_token     │
   │                          ├─────────────────────────────→│
   │                          │←─────────────────────────────┤
   │ Set-Cookie: llmrx_session│                              │
   │ 303 /admin/dashboard     │                              │
   │←─────────────────────────┤                              │
   │                          │                              │
   │ GET /admin/channels      │                              │
   │ Cookie: llmrx_session    │                              │
   ├─────────────────────────→│                              │
   │                          │ [4] SessionMiddleware 验证    │
   │                          │     → user {id:1, role:0}     │
   │                          │ [5] 渲染 channels/list.html   │
   │ 200 HTML                 │                              │
   │←─────────────────────────┤                              │
   │                          │                              │
   │ POST /admin/channels/1/keys                              │
   │ Cookie + form: key=sk-upstream-xxx                       │
   ├─────────────────────────→│                              │
   │                          │ [6] ChannelKeyCreate handler  │
   │                          │ [7] secrets.Encrypt(plain)    │
   │                          │     → ciphertext              │
   │                          │ [8] store.CreateKey()         │
   │                          ├─────────────────────────────→│
   │                          │     key_ciphertext, key_masked│
   │                          │←─────────────────────────────┤
   │                          │ [9] TriggerReload() (goroutine)│
   │                          │     - tokencache.Reload()     │
   │                          │     - pool.LoadFromStore()    │
   │                          │     - breaker.ReloadAll()     │
   │                          │ [10] SyncConfigYAML() (异步)  │
   │ 303 /admin/channels/1/keys                               │
   │←─────────────────────────┤                              │
```

**关键点**：
- 写 SQLite → 触发 reload（goroutine）→ 内存状态更新
- 上游 key 在 DB 中加密存储，UI 只显示掩码
- 异步 sync config.yml（备份）

---

### 2.4 Flow 3：BYOK 消费者用上游 Token（暂缓）

**调用方**：白名单 IP 内的消费者
**Token**：`sk-openai-xxx`（上游 OpenAI 提供的 key）
**入口**：`POST /v1/chat/completions`，`Authorization: Bearer sk-openai-xxx`

**完整时序**（暂不实施）：

```
Consumer                 Go Gateway                      Upstream LLM
   │                          │                                │
   │ POST /v1/chat/completions │                                │
   │ Bearer sk-openai-xxx      │                                │
   ├─────────────────────────→│                                │
   │                          │ [1] tokencache.Lookup() miss   │
   │                          │ [2] BYOK 路径启用?             │
   │                          │ [3] CheckWhitelist(client_ip)  │
   │                          │     → 在白名单                  │
   │                          │ [4] max_keys_per_ip 未超限     │
   │                          │ [5] DetectProvider()           │
   │                          │     sk- + 长度 > 40 → openai   │
   │                          │ [6] TestKey(provider, key)     │
   │                          │                                │
   │                          │ GET /v1/models                 │
   │                          │ Bearer sk-openai-xxx           │
   │                          ├───────────────────────────────→│
   │                          │←───────────────────────────────┤
   │                          │     200 OK → 验证通过           │
   │                          │ [7] AutoCreateChannel()        │
   │                          │     secrets.Encrypt(key)       │
   │                          │     store.CreateBYOK()         │
   │                          │ [8] pool.LoadByokChannels()    │
   │                          │ [9] RouterEngine.RouteWith()   │
   │                          │     L1 含 byok_channels        │
   │                          │     → 选该 byok channel        │
   │                          │ [10] pool.NextKey()            │
   │                          │     → 拿刚才的 key             │
   │                          │                                │
   │                          │ POST /chat/completions         │
   │                          │ Bearer sk-openai-xxx           │
   │                          ├───────────────────────────────→│
   │                          │←───────────────────────────────┤
   │                          │ [11] emitLog(is_byok=true)     │
   │ 200 OK + OpenAI 格式      │                                │
   │←─────────────────────────┤                                │
```

**状态**：⏸️ 暂缓实施，但接口预留（详见 [5. BYOK 接口预留清单](#5-byok-接口预留清单)）

---

## 3. 三个数据流对比

| 维度 | Flow 1 | Flow 2 | Flow 3 |
|------|--------|--------|--------|
| **Token 类型** | `sk-customer-xxx` (llmRx 内部) | session cookie (web 鉴权) | `sk-openai-xxx` (上游 key) |
| **调用方** | 消费者 (内部) | 管理员 (内部) | 消费者 (白名单 IP) |
| **入口** | `/v1/chat/completions` | `/admin/*` | `/v1/chat/completions` |
| **鉴权方式** | `Authorization: Bearer` | Cookie | `Authorization: Bearer` |
| **写 SQLite 表** | `logs` (累积) | `channels`/`tokens`/`users`/... | `byok_channels` + `logs` |
| **核心步骤数** | 6 | 10 | 11 |
| **加密对象** | 无 (明文 token) | 上游 key (admin 配置) | 上游 key (自动加密) |
| **持久化** | 否 (一次性调用) | 是 (管理数据) | 是 (byok channel 30 天) |
| **状态** | ✅ 实施 | ✅ 实施 | ⏸️ 暂缓（仅留接口）|

---

## 4. 阶段计划

### 阶段总览

| 阶段 | 主题 | 工作量 | 依赖 |
|------|------|--------|------|
| **Phase 0** | 脚手架 + 模板基础设施 | 1-2 天 | — |
| **Phase 1** | 管理面核心 CRUD | 3-4 天 | Phase 0 |
| **Phase 1.5** | BYOK 暂缓（仅接口预留）| 0.5 天 | Phase 1 |
| **Phase 2** | 剩余管理面（Alerts/Analytics/Logs/Config/Dashboard）| 2-3 天 | Phase 1 |
| **Phase 3** | 清理（web/ + webui/ + testhelper/）| 1 天 | Phase 2 |
| **Phase 4**（并行）| SQLite 调优 | 0.5 天 | 任何时候 |
| **总计** | | **~8-11 天（2-3 周）** | |

### Phase 0 — 脚手架 + 模板基础设施

**目标**：双模板系统跑通，登录可用，base layout 渲染。

详见下文（参考原 Phase 0 设计）。

### Phase 1 — 管理面核心 CRUD

**目标**：Channels / Tokens / Plans / Users 全部从 JSON API 扩展为 HTML 页面。

| 模块 | 模板 | 路由 |
|------|------|------|
| **Channels** | list + form + keys | `GET/POST /admin/channels` |
| **Tokens** | list + form | `GET/POST/PUT/DELETE /admin/tokens` |
| **Plans** | list + form + detail | `GET/POST/PUT/DELETE /admin/plans` |
| **Users** | list + form + password | `GET/POST/DELETE /admin/users` |

### Phase 1.5 — BYOK 接口预留（暂缓实施）

**目标**：保留 4 处接口，确保未来 1-2 天可补完 BYOK。

详见 [5. BYOK 接口预留清单](#5-byok-接口预留清单)。

### Phase 2 — 剩余管理面

| 模块 | 模板 | 特点 |
|------|------|------|
| **Dashboard** | index.html | 统计卡片 + Chart.js |
| **Logs** | index.html | 表格 + 筛选 + HTMX SSE 实时刷新 |
| **Alerts** | list + events | CRUD + 事件 + ACK |
| **Analytics** | dashboard | 4 个图表 + 时间范围 |
| **Config** | yaml.html | `<textarea>` YAML 编辑器 |
| **Effective** | effective.html | 只读表格 |
| **Secrets Rotate** | rotate.html | 表单 + 警告 |

### Phase 3 — 清理

| 删除项 | 行数 | 前提 |
|--------|------|------|
| `web/` (React SPA) | ~2978 | Phase 1-2 全部页面已验证 |
| `internal/webui/embed.go` | ~63 | 新 webui 替代 |
| `internal/testhelper/` | ~241 | 无测试依赖 |
| `package.json` 等 | — | 无用 |

### Phase 4 — SQLite 调优（与 Phase 1-3 并行）

详见 [`docs/SQLITE-TUNING.md`](./SQLITE-TUNING.md)。

---

## 5. BYOK 接口预留清单

> **状态**：⏸️ 暂缓实施，但保留 4 处接口，确保未来 1-2 天可补完。

### 5.1 Store 接口扩展

**位置**：`internal/store/store.go`

```go
type Store interface {
    // ... 现有方法 ...

    // BYOK 接口（暂未实现，返回 ErrNotImplemented）
    CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error)
    ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error)
    GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error)
    DeleteBYOKChannel(ctx context.Context, id int64) error
}
```

### 5.2 Router L1 钩子

**位置**：`internal/router/engine.go`

```go
type RouterEngine struct {
    // ... 现有字段
    extraChannels []func() []*model.Channel  // BYOK 通道源（v1 空）
}

func (e *RouterEngine) RegisterExtraChannels(src func() []*model.Channel) {
    e.extraChannels = append(e.extraChannels, src)
}

// L1 步骤：
func (e *RouterEngine) staticMatch(model string) []*model.Channel {
    chans := e.static.Match(model)  // 现有
    for _, src := range e.extraChannels {
        chans = append(chans, src()...)
    }
    return chans
}
```

### 5.3 Middleware 未知 Token 处理

**位置**：`internal/middleware/auth.go`

```go
type WithLimitsConfig struct {
    LookupToken      func(key string) (*model.Token, bool)
    Limits           func() *runtime.Limits
    UnknownTokenHook func(w http.ResponseWriter, r *http.Request)  // 新增，默认 nil
}

// 在 middleware 中：
if !found {
    if config.UnknownTokenHook != nil {
        config.UnknownTokenHook(w, r)  // BYOK 路径（v1 不注册）
        return
    }
    http.Error(w, "invalid token", 401)  // 默认行为
}
```

### 5.4 Config `byok:` 段

**位置**：`config.yml` + `internal/config/config.go`

```yaml
# config.yml
byok:
  enabled: false  # 暂未实现，必须保持 false
  # 以下配置项预留（暂不使用）
  # whitelist_ips: []
  # max_keys_per_ip: 5
  # ttl_days: 30
```

```go
// internal/config/config.go
type BYOKConfig struct {
    Enabled bool `yaml:"enabled"`
    // 后续字段预留
}

type Config struct {
    // ... 现有字段
    BYOK BYOKConfig `yaml:"byok"`
}
```

### 5.5 未来补完路径

| 项 | 工作量 |
|----|--------|
| `model.BYOKChannel` 类型定义 | 0.5 天 |
| BYOK 后端逻辑（detect/whitelist/test/create）| 1.5 天 |
| Admin UI（`/admin/byok`）| 1 天 |
| 测试 | 0.5 天 |
| **总计** | **3-4 天** |

---

## 6. 关键技术决策

### 6.1 端口与进程

| 决策 | 选择 | 理由 |
|------|------|------|
| 进程数 | 单 Go 二进制 | 简化部署、零依赖 |
| 端口 | `:8787` | 保持现状 |
| API 入口 | `/v1/*` | OpenAI 兼容 |
| Admin 入口 | `/admin/*` | 隔离 |

### 6.2 前端栈

| 技术 | 用途 | 加载方式 |
|------|------|---------|
| html/template | 模板渲染 | Go 标准库 |
| Tailwind CSS | 样式 | CDN |
| HTMX 1.9 | 动态交互 | CDN |
| htmx-sse | SSE 扩展 | CDN |
| Chart.js（Phase 2）| 图表 | CDN |

### 6.3 数据存储

| 决策 | 选择 | 理由 |
|------|------|------|
| 数据库 | SQLite + WAL | 单文件、零依赖、已实现 |
| 同步模式 | SQLite 为权威 | 单一数据源 |
| config.yml | 启动 seed + 备份 | 不参与运行时 |
| 加密 | AES-256-GCM | 沿用现有 secrets.Manager |

### 6.4 会话与鉴权

| 维度 | 方案 |
|------|------|
| Admin 鉴权 | Session cookie + SQLite `users.session_token` 字段 |
| 消费者鉴权 | `Authorization: Bearer sk-customer-xxx` |
| 密码存储 | argon2id 哈希（沿用现有）|
| CSRF（v1） | SameSite=Strict cookie |
| CSRF（v2） | Token |

### 6.5 自动化机制

| 机制 | 触发 | 动作 |
|------|------|------|
| Reload | 任何写 SQLite 后 | 重建内存状态（goroutine）|
| Auto-fill | 创建 channel/token/plan/user | 补全默认字段 |
| Sync config.yml | 写 channels/tokens 后 | 异步备份到 YAML |
| Seed | 启动时空 DB | 从 config.yml 导入 |

### 6.6 错误与日志

| 维度 | 方案 |
|------|------|
| 日志位置 | SQLite `logs` 表 + stderr |
| 保留期 | 30 天（可配置）|
| 清理 | 每日 0 点 goroutine |
| 错误响应 | OpenAI 兼容格式 |

---

## 7. 详细设计索引

| 主题 | 文档 |
|------|------|
| **BYOK 设计** | [`docs/BYOK.md`](./BYOK.md) |
| **SQLite 调优** | [`docs/SQLITE-TUNING.md`](./SQLITE-TUNING.md) |
| **架构总览** | [`docs/ARCHITECTURE.md`](./ARCHITECTURE.md) |
| **运维指南** | [`docs/OPERATIONS.md`](./OPERATIONS.md) |
| **配置持久化** | [`docs/PASSTHROUGH.md`](./PASSTHROUGH.md) |

---

## 附录 A：技术栈对比（v1 vs v2）

| 维度 | v1（React + Go）| v2（纯 Go + html/template）|
|------|-----------------|------------------------------|
| 进程数 | 1 | 1 |
| 端口 | :8787 | :8787 |
| 前端构建 | npm + Vite + TS | 无（CDN）|
| 前端代码 | ~2978 行 | ~1500 行（模板）|
| 模板引擎 | React | html/template |
| CSS | Tailwind JIT | Tailwind CDN |
| JS 交互 | React 状态 | HTMX |
| 部署大小 | 大（含 node_modules）| 小（无前端资源）|
| 迭代速度 | 中（编译两套）| 快（单 Go 编译）|
| 类型安全 | TS | Go 模板类型 |

## 附录 B：术语表

| 术语 | 含义 |
|------|------|
| **Flow 1** | 消费者用 llmRx 内部 token 调用 LLM |
| **Flow 2** | 管理员在 web UI 配置管理数据 |
| **Flow 3 / BYOK** | 消费者自带上游 key 走 llmRx 代理 |
| **Provider** | 上游 LLM 服务商（OpenAI/Anthropic/Gemini）|
| **Channel** | llmRx 中的上游配置单元 |
| **Token** | llmRx 分发给消费者的访问凭证 |
| **Plan** | 计费套餐（绑定 budget / markup / quota）|
| **WAL** | SQLite Write-Ahead Logging 模式 |
