# P21 — 渠道用量监控 + 上游套餐配额 + 配额感知路由

状态：已实施（一期）

## 1. 背景与目标

P17 之前 llmRx 已有 per-request 用量日志（`model.Log`：channel / token / 四类 token / 成本），
但缺三块能力：

1. **渠道维度的 token 用量跟踪与展示** —— 现有 Analytics 只有请求数 / 合并 token / 成本，
   没有 prompt / completion / cached / reasoning 的拆分视图。
2. **上游套餐（code plan / token plan）配额概念** —— 订阅制渠道（GLM Coding Plan、
   Claude 编程套餐、按量 token 包）有窗口化配额，网关完全不感知，配额将耗尽的渠道照样被打满。
3. **配额感知的路由分配** —— L1-L5 均不参考用量状态，无法"多用包内额度、避开将耗尽的渠道"。

P21 目标：监控页展示各渠道 token 用量 + 套餐配额进度；路由在配额耗尽时自动避开、
紧张时降权，实现更智能的调用分配。

## 2. 设计决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 配额归属 | 挂在 channel 上（`PlanQuota` 子结构） | 一个渠道对应一个上游账号/套餐；Key 级聚合过于细碎 |
| 计量指标 | `tokens`（prompt+completion）/ `prompt_tokens` / `usd` | 覆盖 GLM（prompt 计数）、Claude（总 token）、按量美元三类 |
| 窗口语义 | `monthly`（自然月）/ `daily` / `rolling`（滚动 N 小时） | 编程套餐是 5h 滚动窗口；token 包多为月度 |
| 计数口径 | tokens = prompt+completion；reasoning 是 completion 子集不重复计；cached 含在 prompt 内 | 与上游账单口径一致 |
| 状态存放 | 进程内 `usage.Tracker`，JSONL 日志为唯一事实源，重启回放恢复 | 不引入新的持久化；恢复按最宽窗口边界一次扫描 |
| 路由联动 | 硬约束（≥100% 剔除）+ 软偏好（`quota_aware` 策略，可选启用） | 硬约束自动生效保护账号；软偏好不改变既有策略行为 |
| MCP / 缓存命中 | 不计入渠道配额 | MCP 工具调用是外部成本，不走渠道 LLM 额度；缓存命中没有上游调用 |

## 3. 实现

### 3.1 数据模型（`internal/model/types.go`）

```go
type PlanQuota struct {
    Kind        string  // "code" | "token" | ""（展示徽章）
    Metric      string  // tokens | prompt_tokens | usd
    Limit       float64 // 0 = 不跟踪
    Window      string  // monthly | daily | rolling
    WindowHours int     // rolling 窗口的小时数（1..720）
}
```

- `Channel.PlanQuota`；`Validate()` 校验枚举；`Tracked()` 判定是否参与跟踪。
- 存储：`channels.plan_quota` JSON 文本列（`internal/store/sqlite.go` 迁移链，PG 同一 DDL 经
  RewriteDDL）。CRUD 全链路读写。
- YAML seed：`ChannelConfig` 新增 `plan_kind` / `plan_quota_metric` / `plan_quota_limit` /
  `plan_quota_window` / `plan_quota_window_hours`（`cmd/gateway/main.go: seedChannels`）。

### 3.2 用量跟踪器（新包 `internal/usage`）

`Tracker` 每渠道维护：

- **calendar 窗口**（daily/monthly）：`periodKey + 累计值`，周期翻转自动清零；读取时校验
  periodKey 与当前周期一致，防止上个周期的残留值泄漏。
- **rolling 窗口**：5 分钟桶的 map 环（保留 48h），`Usage` 时按窗口起点求和；桶粒度带来
  ≤5min 的边界误差。
- 计数字段：requests / prompt / completion / cached / reasoning / real USD。

API：

```go
Observe(channelID, prompt, completion, cached, reasoning, realUSD, at)
Usage(ch, now) WindowUsage   // 按渠道声明的 metric/window 解释
UsageRatio(ch, now) float64  // 路由热路径
Snapshot(channels, now)      // 监控页
RecoverRecent(trk, logStore, store)  // 启动恢复：最宽窗口边界起 ScanSince 回放
```

- 喂入点：`api.LogEmitter`（`SetUsageTracker` + `observeUsage`），Emit 是唯一计量咽喉点；
  nil 安全（最小部署/测试可关闭）。
- 恢复：`RecoverRecent` 计算所有 tracked 渠道中最宽的窗口起点（月初 / 当日零点 /
  now-N h），`logstore.ScanSince` 流式回放。日志保留期之外的窗口部分恢复（文档化取舍）。

### 3.3 logstore 扩展（`internal/logstore`）

- **meta v2**：`metaGroup` 增加 Prompt/Completion/Cached/Reasoning/Errors；
  `fileMeta.MetaVersion=2`；`loadMeta` 拒绝旧版本触发重扫重建（一次性）。
- **`ScanSince(since, fn)`**：新 Driver 原语，流式输出 `CreatedAt >= since` 的行（无 top-K
  截断），三个后端（JSONL / SQLite legacy / Postgres）各自实现；Manager 先 Flush 异步队列。
- **`DailyChannelStats(days)`**：JSONL 后端从 meta 直接回答 per-(day, channel) 汇总
  （O(files)，免行扫描）；其他后端返回 `ErrDailyChannelStatsUnavailable`，调用方回退 ScanSince。

### 3.4 路由联动（`internal/router`）

管线顺序：L1 static → L2 breaker → **L2.5 quota guard（新）** → L3 cost → L4 intent → L5 thompson。

- **L2.5 quota guard**：`UsageRatio(ch, now) >= 1` 的候选剔除；全部耗尽时保留原样
  （上游节流响应好过硬路由失败）。产生新 slice（static 索引不可原地改）。LogParts 记
  `L2.5(quota)`。
- **运行时开关**：`quota_guard_enabled`（默认开）、`quota_soft_threshold`（默认 0.8），
  经 `runtime.Defaults` 原子读写 + 快照持久化（`quota_guard` 用指针三态，兼容旧快照）。
- **`quota_aware` 策略**：`QuotaAwareStrategy{Provider, Latency, SoftThreshold}`，
  score = 0.45×priceNorm + 0.25×latNorm + 0.30×quotaPressure；pressure = clamp(ratio,0,1)，
  超 soft 线后斜率翻倍（耗尽 ≈ 1.5）；未跟踪渠道取中性 0.5。运行时
  `cost_strategy: quota_aware` 即可切换（`PUT /admin/api/v1/config` 白名单已扩展）。

### 3.5 Admin API（`internal/admin/user-usage.go`）

| 端点 | 内容 |
|---|---|
| `GET /admin/api/v1/usage/overview?range=24h\|7d\|30d` | per-channel 请求/四类 token/双成本/错误率 + 实时配额 gauge（used/ratio/window/reset_at）+ 汇总 |
| `GET /admin/api/v1/usage/timeseries?range=…` | per-channel token 时序（24h 按小时行扫描；7d/30d 按日，meta 优先） |
| `GET /admin/api/v1/usage/plans` | 下游 Plan 预算进度卡（used/budget/绑定 token 数） |

聚合逻辑共享于 `internal/usage/aggregate.go`（webui 与 admin 复用）：
`AggregateChannelUsage` / `ChannelBuckets` / `ParseRange`。7d/30d 走 meta（O(files)），
24h 与非 JSONL 后端走 ScanSince 行扫描。

### 3.6 监控页（`/admin/channel-monitor`）

- 顶部汇总卡：请求 / prompt（含 cached）/ completion / 双成本 / 错误率。
- 渠道明细表：四类 token、双成本、错误率、**配额进度条**（绿 <80% / 黄 ≥80% / 红 ≥100%）、
  code/token plan 徽章、余额列。
- 趋势图：按渠道堆叠的 token 曲线（canvas 内联 JS，沿用 Analytics 模式），24h 逐小时 /
  7d·30d 逐日，图例可勾选。
- 下游 Plan 预算卡。
- 侧边栏「监控」区新增「渠道监控」入口。

渠道表单（Channels → 编辑）新增「上游套餐配额」区：类型 / 指标 / 上限 / 窗口 / 滚动小时数，
留空上限即停止跟踪（与余额清零语义一致）。

## 4. 不计入配额的流量（口径说明）

| 流量 | 是否计入 | 原因 |
|---|---|---|
| 聊天补全（含流式） | ✅ | 主路径，emitter 观测 |
| 4xx/5xx 失败请求 | ✅（requests 计数，token 为 0） | token 指标不受影响，请求数可见 |
| 响应缓存命中 | ❌ | 无上游调用，不消耗渠道额度 |
| MCP 工具调用 | ❌ | 外部工具成本，不来自渠道 LLM 额度 |

## 5. 测试

- `internal/usage`：三种 metric 口径、月/日窗口翻转防泄漏、滚动窗口截断、恢复回放、
  并发（-race）、WindowRange、PlanQuota.Validate。
- `internal/router`：guard 剔除耗尽渠道（RouterPath 出现 L2.5(quota)）、全耗尽回退、
  运行时开关关闭、未跟踪渠道不受影响、quota_aware 排序（剩余配额优先/耗尽殿后/中性 tie）。
- `internal/logstore`：meta v2 拆分与 per-group errors、v1 meta 触发重建、ScanSince
  边界过滤/跨天/提前停止。
- `internal/admin`：overview/timeseries/plans 端到端（httptest + 真实 logstore）、
  plan_quota CRUD 回环与非法值 400。
- `internal/webui`：监控页全渲染路径冒烟（含模板 helper 运行期错误）。

## 6. 已知取舍 / 后续

- 恢复依赖日志保留期：monthly 窗口跨过 retention（默认 30d）时按残余日志恢复，可能低估。
- rolling >48h 的窗口只按保留的 48h 尾巴计算（`rollingRetention`），文档化而非硬拒绝。
- SQLite legacy / Postgres 后端的 7d/30d 监控聚合走行扫描（meta 仅 JSONL 有）；量大时可
  为 PG 增加物化视图。
- Plan（下游）仍是终身累计预算，无周期重置；如需月度重置可在 Plan 上复用 PlanQuota 的
  窗口语义。
- Prometheus 指标目前无 channel 维度（P10 设计如此），需要时可在 emitter 补一个
  per-channel 计数器。
