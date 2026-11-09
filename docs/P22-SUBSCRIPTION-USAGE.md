# P22 — 厂商侧订阅用量监控

状态：已实施（一期）

## 1. 背景

P21 实现了「网关本地计量」的渠道配额（`Channel.PlanQuota` + `usage.Tracker`），
但它回答的是「**我们**观察到消耗了多少」，不是「**厂商说**还剩多少」。两者会分叉：

- 同一个上游账号可能同时被别的工具/团队使用；
- 厂商的计费单位往往不是 token（积分、credits、请求数）；
- 套餐窗口是厂商定义的（GLM Coding Plan 是 5h 动态刷新 + 每周双上限）。

P22 的目标：直接向厂商查询订阅消耗，在独立页面展示，并让路由在厂商数据可用时优先采用。

## 2. 各厂商 API 能力矩阵（调研结论）

| 厂商 | 源头接口 | 认证 | 能拿到什么 | 结论 |
|---|---|---|---|---|
| **OpenRouter** | `GET {base}/api/v1/key` | Bearer（普通 key） | `limit` / `limit_remaining` / `usage`，`usage_daily/weekly/monthly`，`budgets[]`（含 `reset_interval` + `resets_at`） | ✅ **已接入**，多窗口完整 |
| **智谱 GLM Coding Plan** | `GET bigmodel.cn/api/paas/v4/monitor/usage/quota/limit`（另有 `credit-usage/activity`、`token-usage/activity`、`token-usage/usage-detail`） | **Authorization: Bearer**（Cookie 单独使用会被拒） | 用额度/已用/重置时间（字段名待实测确认） | ⚠️ **实验性**：接口非公开，字段靠启发式识别 |
| DeepSeek / Moonshot / SiliconFlow | 仅余额接口 | Bearer | 剩余金额（无订阅窗口语义） | 由 P14 覆盖，无需重复 |
| OpenAI / Anthropic 官方 | org usage report | **admin key**（与推理 key 不同） | 按日 token 汇总 | 未接入，`custom` 探针可临时顶替 |
| one-api / new-api 中转 | `/api/user/self` | 系统访问令牌 | 额度 / 已用 / 请求数 | 未接入，`custom` 探针可临时顶替 |

> 智谱接口路径来自其仪表盘自身的 JS bundle（`claude-usage` chunk），认证方式经实测确认：
> 带 Cookie 返回「Header中未收到Authorization参数」，带 `Authorization: Bearer` 才进入令牌校验。

> 结论：**订阅用量没有行业统一接口**。因此 P22 的核心不是「写死几个厂商」，
> 而是「一个窗口模型 + 可插拔解析器」，让确定能拿到的（OpenRouter）开箱即用，
> 拿不到的（智谱）能通过配置工作，未知厂商能用通用探针兜底。

## 3. 数据模型

### 3.1 渠道配置 `Channel.UsageProbe`

```go
type UsageProbe struct {
    Kind        string // openrouter_key | zhipu_web | custom | ""
    CookieCipher string // AES-GCM 密封，json:"-" 永不序列化
    CookieMasked string // UI 展示（sid=***abcd）
    EndpointURL  string // 覆盖默认端点（custom 必填）
    JSONPath     string // custom 的取值路径
    WindowName   string // custom 的单窗口名
    Unit         string // usd | tokens | credits | requests
    IntervalSec  int    // 0 = 用全局默认
}
```

存储：`channels.usage_probe`（配置）+ `channels.usage_state`（最近报告 JSON）+
`channels.usage_checked_at` / `channels.usage_error`（P14 同款语义：失败保留上次成功值）。

### 3.2 窗口模型 `subusage.UsagePoint`

一个报告是**多个窗口**，不是单个数字——这是真实套餐的形状：

```go
type UsagePoint struct {
    Window    string    // key_limit | daily | weekly | monthly | rolling_5h | credits
    Scope     string    // account | key | plan | free_models
    Used      float64
    Limit     float64   // 0 = 不限/未知
    Remaining float64   // -1 = 厂商没报
    Unit      string
    ResetsAt  time.Time // 零值 = 不重置（一次性额度）
}
```

`Ratio()` **优先用 `Remaining`** 而不是 `Used/Limit`：部分厂商两个数字存在滞后差异，
`remaining` 才是真正决定还能不能用完的量。

### 3.3 历史表 `channel_usage_points`

```sql
(id, channel_id, ts, source, win, used, limit_val, remaining, unit, resets_at)
```

窗口名用 `win` 而不是 `window`——后者是 PostgreSQL 保留字。历史写入有 10 分钟去重窗口，
避免「探测频率」变成「表增长频率」。

## 4. 解析器

`internal/subusage` 提供注册表（`RegisterParser` / `LookupParser`），内置三个：

### `openrouter_key`（生产就绪）
`GET {base}/api/v1/key` → 映射为：
- `data.limit` + `limit_remaining` + `usage` → `key_limit`
- 每个 `budgets[]` → 按 `reset_interval` 映射为 `daily|weekly|monthly`（带 `resets_at`）
- `usage_daily/weekly/monthly` → 当对应预算不存在时的「仅用量」窗口
- `free_model_daily_requests` → `daily` / scope `free_models` / unit `requests`

### `zhipu_web`（**实验性**）
GLM 无公开 API，因此：
1. 请求 `EndpointURL`（默认 `https://bigmodel.cn/api/paas/v4/monitor/usage/quota/limit`），
   认证用 **`Authorization: Bearer <渠道 API Key>`**；若运营者额外提供了 Cookie 则一并发送；
2. 先按 JSON 解析，失败则在 HTML 里找 `__NEXT_DATA__` / `__INITIAL_STATE__` / `window.__NUXT__` 内嵌 JSON；
3. 遍历整棵 JSON 树，按字段族（`used/usedCredits/usedQuota`、`total/limit/quota`、
   `remaining/left/available`、`resetAt/resetTime/...`）识别窗口。

因为第 3 步是启发式的，`Report.Experimental = true`，UI 显示「实验性」徽章；
识别失败时**不报错**而是返回 `Warn` + 原始响应前 512 字节，运营者据此调整
`EndpointURL` 或改用 `custom`。

### `custom`
`JSONPath`（`a.b.0.c`）+ `WindowName` + `Unit`，把任意厂商的接口塞进同一模型。
路径可指向对象（读 `used/total/remaining`）或单个数字（视为用量）。

## 5. 轮询与恢复

`internal/subusage/monitor.go` 完全对齐 P14 balance monitor 的形状：

- `Config{Interval, Timeout, Jitter}`，首轮即 jitter（避免多副本同时打厂商）；
- `Sweep` 遍历 enabled 且有 `Kind` 的渠道；支持 `IntervalSec` 单渠道覆盖
  （按 `usage_checked_at` 判断，不必为每个渠道开 goroutine）；
- 失败：只写 `usage_error` + `usage_checked_at`，**保留上次成功报告**；
- 历史：每窗口一行，10 分钟内相同读数跳过；
- 保留：每 sweep 内联 prune（默认 90 天）。

## 6. 路由联动

`usage.Tracker` 实现 `SourceRatio`：

```go
func (t *Tracker) SourceRatio(ch, now) (ratio float64, ok bool)
```

取**最紧的存活窗口**的消耗比；以下情况 `ok=false` 回退到本地计量：

- 渠道没配置探针 / 没有缓存报告；
- 报告里所有窗口的 `ResetsAt` 都已过期（陈旧数据不能继续压制渠道）；
- 厂商只报用量不报上限。

`quotaGuard` 优先用 `SourceRatio`，并把来源写进 RouterPath：

| RouterPath 标记 | 含义 |
|---|---|
| `L2.5(quota)` | 按**本地**计量剔除 |
| `L2.5(quota,src)` | 按**厂商报告**剔除 |

这个区分很重要：本地计数很低却仍被剔除时，只有一个解释——账号在校外也被消耗。

## 7. API 与页面

| 端点 | 说明 |
|---|---|
| `GET /admin/api/v1/subscriptions` | 所有探针渠道 + 各窗口 ratio/exhausted + 本地估算对照 |
| `POST /admin/api/v1/subscriptions/{id}/refresh` | 立即探测（502 表示上游失败，错误已持久化） |
| `GET /admin/api/v1/subscriptions/{id}/history?days=N` | 单渠道历史 |
| `GET /admin/api/v1/subscription-parsers` | 已注册解析器 + 说明 |

页面 `/admin/subscription-quota`（侧边栏「监控 → 订阅用量」）：

- 每渠道一张卡：`厂商权威` / `本地估算` / `无数据` 徽章，实验性徽章 + 警告；
- 每窗口一行：已用 / 上限 / 剩余 / 进度条 / 重置倒计时（`3d 4h`、`22h 13m`）；
- 窗口按「越先卡住越靠前」排序（5h → key_limit → 日 → 周 → 月 → 余额）；
- 底部对照 P21 本地估算，差异即「账号被其他工具占用」的信号；
- `立即刷新` 走 HTMX 单卡替换。

渠道表单新增「源头用量探针」区：类型 / 间隔 / 端点覆盖 / Cookie（write-only，加密存储）/ JSONPath / 窗口名 / 单位。

## 8. Cookie 安全

- Cookie 是**可选**的：智谱接口走 `Authorization: Bearer`，默认直接用渠道 API Key；
  仅当接口要求浏览器会话时才填 Cookie。
- 只接受 `usage_probe_cookie` 表单字段，经 `secrets.Manager.Encrypt`（AES-256-GCM）后存
  `usage_probe` JSON 的 `CookieCipher`；
- 该字段 `json:"-"`，**任何 API 都不会回显**；表单回填的是 `CookieMasked`；
- 未配置 master key 时保存 Cookie 会直接报错（不会退化成明文存储）；
- Admin PATCH 接口不能设置 Cookie（必须走 UI/服务端密封），并会保留已存值。

## 9. 修掉的一个既有缺陷

实现过程中发现：`main()` 在 `server.New()` **之后**才调用 `SetBalanceMonitor` /
`SetUsageMonitor`，而 webui 的 handler 在 `registerRoutes`（`New` 内部）就已构造完毕，
于是它拿到的永远是 `nil`——P14 渠道列表里的「刷新余额」按钮在生产环境实际是失效的
（单测直接注入 handler，掩盖了这点）。

修法：`Server` 保存 `webUI *webui.Handler`，两个 setter 在赋值后回推给 webui。

## 10. 测试

- `internal/subusage`：OpenRouter 全字段（预算/无限额/免费层/耗尽/401）、
  智谱嵌套窗口与内嵌 JSON 与「形状变更只 warn 不 err」、Cookie 缺失/被拒、
  custom 对象与数字与路径缺失、`Ratio` 优先 remaining、monitor 落库/失败保留/
  去重/单渠道间隔/并发/Start-Stop。
- `internal/store`：`usage_probe` 往返、状态写入的失败保留语义、历史 append/read/prune。
- `internal/router`：厂商数据优先剔除（`L2.5(quota,src)`）、窗口过期回退本地、
  无探针时保持 P21 行为。
- `internal/admin`：列表（未配置探针的渠道不出现）、刷新 503、历史、解析器清单、配置校验。
- `internal/webui`：页面全渲染路径、空状态、实验性徽章、`filter=all`、倒计时格式化、窗口排序。

## 11. 已知限制

- **GLM Cookie 方案会因仪表盘改版失效**，是刻意接受的技术债；`Warn` + 原始响应留证。
- Cookie 会话本身会过期，需要运营者定期更新。
- 厂商接口有速率限制，故默认间隔 1h；`IntervalSec` 不建议低于 5 分钟。
- 本地估算与厂商口径的「差多少算异常」尚未告警化（可在 P14 的 alert 体系加一条
  `usage_drift` 规则）。
- OpenAI / Anthropic 的 admin-key 用量报告未内置；需要时可实现一个解析器接入同一框架。
