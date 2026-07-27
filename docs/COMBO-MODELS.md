# 组合模型 (Token Combo Models)

> 2026-07-27 · 设计文档 v0.2

把多个底层模型封装成一个**虚拟模型名**，按 token 粒度绑定，沿用现有 L1-L5 路由管线（或退化为串行 fallback）选择胜出 channel。

**变更记录**：

- v0.2（2026-07-27）：修正 6 处问题——N+1 查询、combo 名格式校验、`/v1/models` 行为、路由日志注入细节、串行模式失败处理分层、字段长度限制
- v0.1（2026-07-27）：初稿

---

## 目录

1. [背景与动机](#1-背景与动机)
2. [概念定义](#2-概念定义)
3. [四种路由模式](#3-四种路由模式)
4. [数据模型](#4-数据模型)
5. [请求生命周期](#5-请求生命周期)
6. [各层改动](#6-各层改动)
7. [路由管线扩展](#7-路由管线扩展)
8. [并发与一致性](#8-并发与一致性)
9. [API 设计](#9-api-设计)
10. [WebUI 设计](#10-webui-设计)
11. [测试策略](#11-测试策略)
12. [本轮范围与未来工作](#12-本轮范围与未来工作)

---

## 1. 背景与动机

### 1.1 现状

当前 llmRx 的 token 模型：

```go
type Token struct {
    ModelsWhitelist []string  // 白名单：允许请求的模型名列表
    // ... RPM, TPM, IPWhitelist, PlanID
}
```

`ModelsWhitelist` 是**允许列表**，只判断「这个 token 能不能请求 `gpt-4`」，**不**决定「请求 `gpt-4` 时路由到哪个 channel」。

channel 路由由全局 L1-L5 管线决定，**所有 token 共享同一份 channel 候选集与策略**。

### 1.2 问题

| 场景 | 当前缺陷 |
|---|---|
| 客户 A 想要「我自己的 `smart-1`，从 gpt-4o / claude-3.5 中二选一」 | 做不到；只能让客户用真实模型名，且路由策略全局唯一 |
| 客户 B 想要「我自己的 `premium-fallback`，优先 gpt-4o，失败走 claude-3.5」 | 做不到；channel 层没有 fallback 概念 |
| 客户 C 想要「`budget-tier` 走 cheap 系，`quality-tier` 走 premium 系」 | 只能用 token 名字区分 channel；一旦 channel 数量增加就难管理 |
| 按 token 维度给不同客户提供不同 model 池 | 必须克隆多份 channel，运营成本高 |

### 1.3 目标

为 token 引入**「组合模型」(Combo Model)** 概念：

> 在 token 维度上，定义一个虚拟模型名 → 一组底层模型的映射。请求该虚拟名时，沿用（或降级使用）现有 L1-L5 路由规则选择 channel。

差异化的部分（按 token 隔离）：

- 虚拟名 `combo.Name`
- 底层池 `combo.Models`
- 路由模式 `combo.Mode`（load_balance / serial / 未来的 parallel / intent）
- 可选的 L3 strategy 覆盖 `combo.Strategy`

共享的部分（全局不变）：

- L1 channel 候选集（由 `channel.Models` 与 `channel.Status` 决定）
- L2 熔断状态
- L5 Thompson 后验
- 模型/IP 白名单仍按 token 生效

---

## 2. 概念定义

### 2.1 组合模型（Combo Model）

**资源**：每 token 一组 `TokenComboModel` 记录。

```yaml
token_id: 42
name: "smart-1"           # 虚拟模型名
models:                   # 底层池
  - "gpt-4o"
  - "claude-3-5-sonnet"
  - "gemini-2.5-flash"
mode: "load_balance"      # 或 "serial"
strategy: ""              # 空 = 继承全局 L3；"cheapest" / "fastest" / "balanced" 覆盖
enabled: true
```

请求 `model: "smart-1"` 时，token=42 的持有者会得到「从 gpt-4o / claude-3-5 / gemini-2.5-flash 中按现有 L1-L5 选一个」。

### 2.2 与既有概念的关系

| 既有概念 | 关系 |
|---|---|
| `channel.Models []string` | 表达「channel 能服务哪些上游真实模型」。combo 解析后 L1 仍按 `channel.Models` 匹配 |
| `token.ModelsWhitelist []string` | 表达「token 允许请求的入口」。combo 名视为有效入口 |
| `channel.Priority` | 全局优先级，L3 cost.Sort 用 |
| `token.PlanID` | 计费，与 combo 无关 |
| `CostStrategy` | L3 全局策略；combo 可临时覆盖 |

### 2.3 命名空间

combo 名是 **token 私有的**：token A 和 token B 都可以有 `smart-1`，各自映射不同底层池。

combo 名**不得**与任何 `channel.Models` 里的真实模型名冲突（创建时校验）。

---

## 3. 四种路由模式

| 模式 | 行为 | 复杂度 | 本轮 |
|---|---|---|---|
| `load_balance` | 把底层池作为 channel 候选集，走 L1-L5 选一个最佳 | 低（直接复用 L1-L5） | ✅ |
| `serial` | 按顺序逐个调用底层模型，第一个返回 2xx 即停；其它作为 fallback | 中 | ✅ |
| `parallel` | 并发调用所有底层模型，第一个成功的响应作为结果 | 高（流式取消） | ⏳ |
| `intent` | L4 意图分类选一个；每个底层模型带自己的 intents 列表 | 中 | ⏳ |

本轮实现 `load_balance` + `serial`，其它模式预留 enum。

### 3.1 load_balance 模式详解

**L0 Combo 解析**：
1. middleware 把 token 的 `ComboModels` 注入到 `TokenInfo`
2. 命中 `info.ComboModels["smart-1"]` → 取出 `combo`
3. 把 `combo.Models` 作为 L1 的候选模型集合

**L1 Static（扩展）**：
- 当前：`Match(modelName)` — 返回 `channel.Models` 含 `modelName` 的所有 channel
- 扩展：`MatchAny([]string{...})` — 返回 `channel.Models` 与任一传入模型相交的 channel
- 行为等价于「combo 的底层池被展开成 L1 候选」

**L2 → L3 → L4 → L5**：与既有完全一致

**L3 strategy 覆盖**（仅当 `combo.Strategy != ""`）：
- 临时把 `CostRouter` 切到 `combo.Strategy` → 排序 → 还原
- 见 §8 关于并发安全的讨论

### 3.2 serial 模式详解

```
var lastChatErr error

for model in combo.Models:
    route, err = RouteWith(ctx, model, opts)   # L1-L5 选 channel
    if errors.Is(err, ErrNoChannel):
        continue                              # 该模型无 channel，静默跳过
    if errors.Is(err, ErrAllBroken):
        continue                              # channel 存在但全熔断，静默跳过
    if err != nil:
        continue                              # 其它路由错误，静默跳过

    resp, code, err = prov.Chat(req, route.KeyValue, route.Channel.BaseURL)
    if err == nil and 2xx:
        return route, resp, code

    // 上游实际返回错误（网络 / 5xx / 4xx）——记录到 lastChatErr
    router.RecordFailure(route.Channel.ID)    # 触发 L2 熔断累计
    lastChatErr = fmt.Errorf("model %s: status=%d err=%w", model, code, err)

if lastChatErr != nil:
    return nil, nil, 0, lastChatErr           # HTTP 502 + body 含错误
return nil, nil, 0, ErrNoChannel              # 全部模型无 channel 可用
```

**三层失败语义**（区分便于调试）：

| 失败类型 | 行为 | 日志级别 | 最终 502 body |
|---|---|---|---|
| `ErrNoChannel` | 该模型无可用 channel，跳过 | debug（静默） | 不包含 |
| `ErrAllBroken` | channel 存在但全熔断，跳过 | warning | 不包含 |
| 上游错误（网络/5xx/4xx）| RecordFailure，记录到 lastChatErr | error | 包含 |

行为特点：
- **第一次成功立即返回**：低延迟
- **失败累计**：上游错误触发的 L2 熔断在后续请求中生效
- **全部失败**：返回最后一个上游错误，body 包含错误详情（HTTP 502）
- **全部无 channel**：返回 `ErrNoChannel`（HTTP 503）

### 3.3 与 channel.Intents 的关系

`channel.Intents` 已经是 L4 的关键字段。combo 的 `intent` 模式（未来）将引入**模型级** intents：
- 一个 combo 可以为每个底层模型独立声明它支持哪些 intents
- 与 channel 级 intents 解耦

`load_balance` 模式**仍然**使用 `channel.Intents`（不变）。

---

## 4. 数据模型

### 4.1 Go struct

```go
// internal/model/types.go
type ComboMode string

const (
    ComboModeLoadBalance ComboMode = "load_balance"
    ComboModeSerial      ComboMode = "serial"
    // 预留
    // ComboModeParallel ComboMode = "parallel"
    // ComboModeIntent   ComboMode = "intent"
)

type TokenComboModel struct {
    ID        int64
    TokenID   int64
    Name      string               // 虚拟模型名
    Models    []string             // 底层池（至少 1 项）
    Mode      ComboMode            // load_balance | serial
    Strategy  CostStrategy         // "" = inherit
    Enabled   bool
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### 4.2 SQLite Schema

```sql
CREATE TABLE token_combo_models (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    token_id   INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    models     TEXT    NOT NULL DEFAULT '[]',  -- JSON
    mode       TEXT    NOT NULL DEFAULT 'load_balance',
    strategy   TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(token_id, name)
);
CREATE INDEX idx_combo_token ON token_combo_models(token_id);
```

### 4.3 Store 接口

```go
// internal/store/store.go

// 按 token 查询
GetComboModels(tokenID int64) ([]model.TokenComboModel, error)
GetComboModel(id int64) (*model.TokenComboModel, error)

// 全量查询（tokencache 热路径，避免 N+1 查询）
GetAllComboModels() ([]model.TokenComboModel, error)

// CRUD（含校验）
CreateComboModel(c *model.TokenComboModel) error
UpdateComboModel(c *model.TokenComboModel) error
DeleteComboModel(id int64) error
```

`GetAllComboModels()` 返回所有 token 的 combos，tokencache 在 `Reload()` 时调用一次，按 tokenID 分桶后赋值到 `TokenInfo.ComboModels`。这样无论有多少 token，reload 只触发一次 SQL 查询而非 N 次。

### 4.4 创建校验规则

`CreateComboModel` 在 `store` 层强制：

1. `combo.Name` 非空，**且**符合 `^[a-zA-Z0-9_-]{1,64}$`（纯字母数字下划线连字符，≤64 字符）
2. `len(combo.Models) >= 1`，**且** `len(combo.Models) <= 100`
3. `combo.Models` 中每一项符合 `^[a-zA-Z0-9._-]{1,128}$`（上游模型名通常较长，给 128 上限）
4. `combo.Name` 不与任何 `channel.Models` 中的真实模型名冲突
5. `combo.Mode` 是合法值（`load_balance` / `serial`）
6. `combo.Strategy` 合法（`""` / `cheapest` / `fastest` / `balanced`）

错误以 `errors.New` 或自定义 `ErrInvalidCombo` 返回。

### 4.5 字段长度汇总

| 字段 | 最大长度 | 说明 |
|---|---|---|
| `name` | 64 | combo 虚拟名 |
| `models` | 100 项 | 底层模型列表 |
| `models[i]` | 128 | 单个模型名 |
| `mode` | 32 | 枚举值 |
| `strategy` | 16 | 枚举值或空 |

---

## 5. 请求生命周期

### 5.1 非 combo 路径（不变）

```
POST /v1/chat/completions
   model: "gpt-4o"
   Authorization: Bearer sk-...

[Middleware] resolve token → TokenInfo
[Middleware] HasModelAccess("gpt-4o")       # 通过
[Middleware] HasIPAccess(clientIP)          # 通过
[API]        RouteWith(ctx, "gpt-4o", opts) # L1-L5
[API]        provider.Chat(...)
[API]        RecordSuccess/Failure
[API]        write response
```

### 5.2 combo 路径（新）

```
POST /v1/chat/completions
   model: "smart-1"
   Authorization: Bearer sk-42

[Middleware] resolve token → TokenInfo.ComboModels["smart-1"] = {Models: [...], Mode: "load_balance", Strategy: ""}
[Middleware] HasModelAccess("smart-1")       # 自动通过（combo 名有效）
[Middleware] HasIPAccess(clientIP)            # 通过
[API]        detect combo by req.Model
[API]        dispatch:
  - load_balance:  RouteWithModels(ctx, combo.Models, opts)  # L1-L5
  - serial:        for m in combo.Models: RouteWith + Chat, fallback
[API]        RecordSuccess/Failure
[API]        write response
```

### 5.3 流式与 combo

流式响应（`stream: true`）下，combo 模式的差异：

- **load_balance**：与现有 L1-L5 路径一致
- **serial**：每个底层模型都需要独立 stream；切换下一个时丢弃当前 stream

本轮**先实现非流式**，流式作为后续增强点（标记为 TODO）。

---

## 6. 各层改动

### 6.1 模型层

| 文件 | 改动 |
|---|---|
| `internal/model/types.go` | 新增 `ComboMode` 常量 + `TokenComboModel` struct |

### 6.2 存储层

| 文件 | 改动 |
|---|---|
| `internal/store/store.go` | 5 个 CRUD 接口方法 |
| `internal/store/sqlite.go` | migration + CRUD 实现 + 冲突校验 |
| `internal/store/sqlite_test.go` | CRUD 单元测试 |

### 6.3 中间件层

| 文件 | 改动 |
|---|---|
| `internal/middleware/auth.go` | `TokenInfo.ComboModels map[string]TokenComboModel`；`HasModelAccess` 允许 combo 名 |
| `internal/tokencache/tokencache.go` | `Reload()` 时加载每个 token 的 combos |

### 6.4 路由层

| 文件 | 改动 |
|---|---|
| `internal/router/static.go` | `MatchAny(models []string)` 新增 |
| `internal/router/engine.go` | `RouteOptions.ModelSet`、`RouteOptions.CostStrategy` 扩展；`RouteWithModels` 便捷方法 |
| `internal/router/cost.go` | `Sort` 接受可选 strategy 参数，避免全局修改 |

### 6.5 API 层

| 文件 | 改动 |
|---|---|
| `internal/api/router.go` | `ChatCompletions` 检测 combo → 分发到 `handleCombo` / `handleSerialCombo`；保留非 combo 路径 |

### 6.6 管理 API

| 文件 | 改动 |
|---|---|
| `internal/admin/handler.go` | 4 个 CRUD endpoints |

### 6.7 WebUI

| 文件 | 改动 |
|---|---|
| `internal/webui/combos.go`（新） | handlers: List / New / Edit / Create / Update / Delete |
| `internal/webui/handler.go` | 注册 6 个路由 |
| `internal/webui/templates/tokens/combos.html`（新） | 列表页 |
| `internal/webui/templates/tokens/combo_form.html`（新） | 新建/编辑表单 |
| `internal/webui/templates/tokens/list.html` | 添加「组合模型」链接 |
| `internal/webui/templates/tokens/form.html` | 编辑页加「组合模型」链接 |

---

## 7. 路由管线扩展

### 7.1 RouteOptions 扩展

```go
// internal/router/engine.go
type RouteOptions struct {
    Text         string                  // L4 输入
    CostStrategy model.CostStrategy      // 可选 L3 覆盖（空 = 用全局）
    ModelSet     []string                // 可选 L1 候选集（空 = 用 modelName 单个）
}
```

### 7.2 RouteWith 行为

```go
func (e *RouterEngine) RouteWith(ctx, modelName, opts) (*RouteResult, error) {
    // L1 解析
    var candidates []*model.Channel
    if len(opts.ModelSet) > 0 {
        candidates = e.static.MatchAny(opts.ModelSet)
    } else {
        candidates = e.static.Match(modelName)
    }
    ...
    // L3 cost：把 strategy 局部化
    if opts.CostStrategy != "" {
        e.cost.SetStrategy(opts.CostStrategy)
        defer e.cost.SetStrategy(savedStrategy)
    }
    candidates = e.cost.Sort(candidates)
    ...
}
```

### 7.3 路由日志

`RouterLog` 字段体现 combo 路径：

```
L0(combo=smart-1) → L1(static) → L2(breaker) → L3(cost=cheapest) → L5(thompson) → select=openai-official
```

非 combo 路径：

```
L1(static) → L2(breaker) → L3(cost) → L5(thompson) → select=openai-official
```

**注入方式**：`L0(combo=...)` 由 API 层的 `handleCombo` 拼接前缀，而非侵入 `RouteWith`：

```go
// api/router.go — handleCombo 内部
route, err := h.router.RouteWith(ctx, req.Model, router.RouteOptions{...})
// route.RouterLog = "L1(static) → L2(breaker) → ..."
// 在 route.RouterLog 前拼接 combo 前缀：
comboLog := fmt.Sprintf("L0(combo=%s) → %s", req.Model, route.RouterLog)
```

这样 `RouteWith` 保持纯净（不知道 combo 概念），combo 日志由调用方拼接。

---

## 8. 并发与一致性

### 8.1 CostRouter 并发

**问题**：`CostRouter` 当前用 `mu sync.RWMutex` 保护 `strategy` 字段，`SetStrategy` 是全局修改。

**风险**：若 combo 临时覆盖 strategy 走 `SetStrategy + defer SetStrategy`，两个请求并发时：
- 请求 A 把 strategy 改成 `cheapest`
- 请求 B 改成 `fastest`
- 请求 A defer 还原成「A 之前的」（即 `cheapest`），但 B 已经在用 `fastest` 了
- 结果：`fastest` 被错还原

**解决方案**：把 `Sort` 改为接受 strategy 参数：

```go
// internal/router/cost.go
func (r *CostRouter) SortWith(channels []*model.Channel, strategy model.CostStrategy) []*model.Channel {
    if strategy == "" {
        strategy = r.strategy  // fallback to global
    }
    // 用局部 strategy 排序
}
```

`Sort(channels)` 保留为「使用全局 strategy」的便捷方法（向后兼容）。

这样 combo 路径只需调用 `SortWith(channels, combo.Strategy)`，不需要修改全局状态。

### 8.2 tokencache 一致性

admin 修改 combo 后必须调用 `tokens.Reload()` 推到 in-memory cache，否则旧 combo 还会生效。

修改 admin handler 的 CreateComboModel/UpdateComboModel/DeleteComboModel 处理器：成功后调用 `h.tokens.Reload()`。

### 8.3 tokencache 批量加载（避免 N+1 查询）

`tokencache.Reload()` 当前只调用 `store.GetTokens()`。增加 combo 后不能对每个 token 逐个调用 `GetComboModels(tokenID)`——1000 个 token 会触发 1000 次 SQL 查询。

**解决方案**：新增 `store.GetAllComboModels()`，一次查询返回所有 combos：

```go
// tokencache.Reload() 内部
toks, _ := c.store.GetTokens()
combos, _ := c.store.GetAllComboModels()  // 单次 SQL，返回 []TokenComboModel

// 按 tokenID 分桶
comboMap := map[int64][]model.TokenComboModel{}
for _, c := range combos {
    comboMap[c.TokenID] = append(comboMap[c.TokenID], c)
}

for _, t := range toks {
    info := middleware.TokenInfo{...}
    if cmbs, ok := comboMap[t.ID]; ok {
        info.ComboModels = make(map[string]model.TokenComboModel, len(cmbs))
        for _, c := range cmbs {
            if c.Enabled {
                info.ComboModels[c.Name] = c
            }
        }
    }
    next[t.Key] = info
}
```

**SQL 实现**：

```sql
SELECT id, token_id, name, models, mode, strategy, enabled, created_at, updated_at
FROM token_combo_models
WHERE enabled = 1
ORDER BY token_id, id
```

单次查询，返回结果集按 token_id 分组，Go 侧 map 分桶。

### 8.4 启动时序

`main.go` 启动顺序不变：
1. Open SQLite
2. Load channels/keys 到 pool
3. Init RouterEngine
4. 启动 tokencache（首次 Reload，含 GetAllComboModels）
5. 启动 admin / webui

无新增竞态：combo 数据在 Reload 时原子性快照到 in-memory cache。

### 8.5 SQL 迁移

新表 `token_combo_models` 的迁移通过 `addTableIfMissing` 或 `CREATE TABLE IF NOT EXISTS` 模式（与 `providers` 表相同的模式），不需要 `addColumnIfMissing`。

---

## 9. API 设计

### 9.1 内部 API（store）

```go
// 读取
GetComboModels(tokenID int64) ([]model.TokenComboModel, error)
GetComboModel(id int64) (*model.TokenComboModel, error)
// 写入
CreateComboModel(c *model.TokenComboModel) error  // 校验 + 插入
UpdateComboModel(c *model.TokenComboModel) error  // 按 ID 更新
DeleteComboModel(id int64) error
```

### 9.2 内部 API（router）

```go
// 既有
RouteWith(ctx, modelName, RouteOptions) (*RouteResult, error)

// 扩展 RouteOptions
type RouteOptions struct {
    Text         string
    CostStrategy model.CostStrategy
    ModelSet     []string
}
```

### 9.3 管理 API

```
GET    /admin/api/v1/tokens/{id}/combos
POST   /admin/api/v1/tokens/{id}/combos
PUT    /admin/api/v1/combos/{id}
DELETE /admin/api/v1/combos/{id}
```

请求体：

```json
{
  "name": "smart-1",
  "models": ["gpt-4o", "claude-3-5-sonnet"],
  "mode": "load_balance",
  "strategy": "",
  "enabled": true
}
```

响应：`200 OK` + 新创建/更新的 combo 实体；`400` + `{"error": {"message": "..."}}`。

### 9.4 公共 API（OpenAI 兼容）

**完全不变**。客户端发 `model: "smart-1"`，llmRx 在内部解析为底层池，对客户端透明。

### 9.5 `/v1/models` 端点

现有 `GET /v1/models` 返回所有 `channel.Models` 的并集。combo 名需要出现在模型列表里，让客户端可以发现 `smart-1` 是可用的入口。

**行为扩展**：在现有 channel 模型列表之后，附加所有**已启用且所有者 token** 拥有的 combo 名：

```json
{
  "object": "list",
  "data": [
    {"id": "gpt-4o", "owned_by": "openai-official"},
    {"id": "claude-3-5-sonnet", "owned_by": "anthropic-hq"},
    {"id": "smart-1", "owned_by": "combo"}
  ]
}
```

- `owned_by = "combo"` 标记虚拟模型，便于客户端区分真实模型和组合模型
- 仅返回调用方 token 拥有的 combos（通过 `TokenInfo.ComboModels` 注入）
- 非 combo 路径的调用方（无 combo）看不到 combo 名——这是正确的，因为 combo 是 token 私有的

### 9.6 错误码

新增：

| 场景 | HTTP | code |
|---|---|---|
| 创建时 combo 名与真实模型名冲突 | 400 | `combo_name_conflict` |
| 创建时 models 列表为空 | 400 | `combo_models_empty` |
| 创建时 mode 非法 | 400 | `combo_mode_invalid` |
| 串行模式全部失败 | 502 | `combo_all_failed`（body 包含最后错误） |

---

## 10. WebUI 设计

### 10.1 路由

```
/admin/tokens/{id}/combos              列表
/admin/tokens/{id}/combos/new          新建表单
/admin/tokens/{id}/combos/{cid}/edit   编辑表单
POST   /admin/tokens/{id}/combos       提交新建
POST   /admin/tokens/{id}/combos/{cid} 提交编辑/删除（_method 字段）
```

### 10.2 列表页

表格列：名称 / 模式 / 底层模型数 / 策略 / 状态 / 操作

```
┌──────────┬──────────┬────────┬─────────┬──────┬──────────┐
│ 名称     │ 模式     │ 模型数 │ 策略    │ 状态 │ 操作     │
├──────────┼──────────┼────────┼─────────┼──────┼──────────┤
│ smart-1  │ 负载均衡 │ 3      │ 继承    │ 启用 │ 编辑/删除│
│ fallback │ 串行     │ 2      │ cheapest│ 启用 │ 编辑/删除│
└──────────┴──────────┴────────┴─────────┴──────┴──────────┘
```

新建按钮在右上角。

### 10.3 表单页

字段：

| 字段 | 控件 | 说明 |
|---|---|---|
| 名称 | text input | 必填，与真实模型名不可冲突 |
| 模式 | select | `load_balance` / `serial` |
| 底层模型 | textarea | 每行一个模型名；至少 1 行 |
| 策略 | select | `inherit` (空字符串) / `cheapest` / `fastest` / `balanced` |
| 启用 | checkbox | |

### 10.4 导航

- Token 列表页每行加 `组合模型` 链接
- Token 编辑页底部加「管理组合模型」链接
- Combo 列表页顶部加「← 返回 Token」链接

### 10.5 帮助

在 `internal/webui/templates/tokens/help.html` 加一节「组合模型」：

> 组合模型是 token 私有的虚拟模型名。请求该名时 llmRx 会在你配置的底层池中按现有规则（L1-L5 均衡 / 串行 fallback）选择 channel。
> 
> 适用场景：
> - 给不同客户分配不同的「等价模型池」
> - 用一个虚拟名作为兜底入口
> - 在不暴露真实模型名的情况下做容量调度

---

## 11. 测试策略

### 11.1 单元测试

| 层 | 测试 |
|---|---|
| `store` | `TestCreateComboModel` / `TestComboNameConflict` / `TestComboModeValidation` / `TestGetComboModels` / `TestUpdateComboModel` / `TestDeleteComboModel` |
| `middleware` | `TestTokenInfo_HasModelAccess_Combo` / `TestTokenInfo_ComboMapPopulated` |
| `router` | `TestStaticRouter_MatchAny` / `TestRouteWith_ModelSet` / `TestRouteWith_CostStrategyOverride` / `TestCostRouter_SortWith` |
| `api` | `TestChat_ComboLoadBalance` / `TestChat_ComboSerialFallback` / `TestChat_ComboSerialAllFailed` / `TestChat_ComboNotInWhitelist` / `TestChat_ComboDisabled` |
| `admin` | `TestAdmin_CreateCombo` / `TestAdmin_UpdateCombo` / `TestAdmin_DeleteCombo` / `TestAdmin_ListCombos` / `TestAdmin_CreateCombo_Conflict` |
| `webui` | `TestCombosPage_Renders` / `TestComboNewForm_Renders` / `TestComboEditForm_Renders` / `TestComboCreate` / `TestComboUpdate` / `TestComboDelete` |

### 11.2 端到端测试

```bash
# 1. 准备：token T1 = sk-xxx，combo "smart-1" = [gpt-4o, claude-3-5]
# 2. 启用 channel A (gpt-4o) 和 channel B (claude-3-5)
# 3. 验证：
curl -X POST localhost:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -d '{"model":"smart-1","messages":[{"role":"user","content":"hi"}]}'
# 期望：返回 200，请求日志中 router_path 包含 "L0(combo=smart-1)"
```

### 11.3 路由日志验证

`logs.router_path` 应包含 `L0(combo=smart-1)` 前缀，方便运营筛选。

### 11.4 性能影响

L0 仅在 combo 命中时执行（一次 map lookup），非 combo 路径零开销。

L1 `MatchAny` 与既有 `Match` 实现成本相当（仍是线性扫描 channels）。

---

## 12. 本轮范围与未来工作

### 12.1 本轮交付（P12）

- `load_balance` + `serial` 两种模式
- store CRUD + admin API + WebUI 子页
- 非流式请求
- 路由日志带 `L0(combo=...)` 标记
- 全套单元测试

### 12.2 未来迭代

- `parallel` 模式：并发 fan-out + 第一个成功胜出（需处理流式取消）
- `intent` 模式：每底层模型独立 intents 列表 + L4 选 model
- combo 级价格上限：每个 combo 可以设独立 budget
- combo 级 RPM/TPM：当前仅 token 级
- 组合模型使用统计：单独埋点
- Admin UI 增强：拖拽排序底层模型（serial 模式时顺序重要）

### 12.3 风险与决策记录

| 风险 | 决策 |
|---|---|
| combo 名与真实模型名冲突 | 拒绝创建（§4.4） |
| combo 名格式 | 正则 `^[a-zA-Z0-9_-]{1,64}$`，models 每项 `^[a-zA-Z0-9._-]{1,128}$`，models ≤100 项（§4.5） |
| combo 名进入白名单的语义 | combo 名视为「合法入口」，自动通过 `HasModelAccess` |
| L3 strategy 覆盖的并发安全 | `SortWith(strategy)` 不修改全局状态（§8.1） |
| tokencache N+1 查询 | 新增 `GetAllComboModels()` 单次批量查询（§8.3） |
| serial 模式三层失败区分 | `ErrNoChannel`/`ErrAllBroken` 静默跳过，上游错误记录到 lastChatErr（§3.2） |
| 串行模式全部失败 | 返回最后一个上游错误 + HTTP 502；全部无 channel 返回 `ErrNoChannel`（§3.2） |
| `/v1/models` 暴露 combo 名 | combo 名出现在模型列表，`owned_by="combo"` 标记虚拟（§9.5） |
| 路由日志可读性 | `L0(combo=...)` 由 API 层拼接，不侵入 `RouteWith`（§7.3） |
| 启动顺序 | 与现有相同，tokencache.Reload() 顺带 GetAllComboModels（§8.4） |

---

## 附录 A：变更文件清单

| 路径 | 类型 | 估算行数 |
|---|---|---|
| `internal/model/types.go` | 修改 | +20 |
| `internal/store/store.go` | 修改 | +15（含 GetAllComboModels） |
| `internal/store/sqlite.go` | 修改 | +200（含 GetAllComboModels SQL + 校验） |
| `internal/middleware/auth.go` | 修改 | +15 |
| `internal/tokencache/tokencache.go` | 修改 | +40（含批量加载分桶逻辑） |
| `internal/router/static.go` | 修改 | +20 |
| `internal/router/cost.go` | 修改 | +25（SortWith 新方法） |
| `internal/router/engine.go` | 修改 | +50 |
| `internal/api/router.go` | 修改 | +140（含 handleCombo + serial + L0 日志拼接） |
| `internal/admin/handler.go` | 修改 | +150 |
| `internal/webui/combos.go` | 新建 | +120 |
| `internal/webui/handler.go` | 修改 | +10 |
| `internal/webui/templates/tokens/combos.html` | 新建 | +60 |
| `internal/webui/templates/tokens/combo_form.html` | 新建 | +80 |
| `internal/webui/templates/tokens/list.html` | 修改 | +10 |
| `internal/webui/templates/tokens/form.html` | 修改 | +10 |
| `internal/webui/templates/tokens/help.html` | 修改 | +25 |
| 测试文件若干 | 修改/新建 | +550 |
| **合计** | | **≈1540 行** |

## 附录 B：数据库迁移示例

```sql
-- 旧库升级时会自动执行
CREATE TABLE IF NOT EXISTS token_combo_models (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    token_id   INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    models     TEXT    NOT NULL DEFAULT '[]',
    mode       TEXT    NOT NULL DEFAULT 'load_balance',
    strategy   TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(token_id, name)
);
CREATE INDEX IF NOT EXISTS idx_combo_token ON token_combo_models(token_id);
```

无 schema 兼容性破坏。新字段（`mode`, `strategy`, `enabled`）都有默认值，向后兼容。
