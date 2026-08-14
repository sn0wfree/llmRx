# 智能路由：补业界差距

> 状态：**全部实施完成** (2026-08-14，4 个 commit 已合入分支 feat/smart-routing-gaps)
> 前置：v1 智能路由已完成 (见 [AUTO-ROUTING.md](AUTO-ROUTING.md))
> 调研背景：见 [RESEARCH-ROUTING.md](RESEARCH-ROUTING.md)

## 0. 概述

v1 智能路由已完成 model=auto 闭环（分类 -> tier -> TS 臂采样 -> 失败转移 -> fallback 安全网）。
本文档规划 4 个业界已有但 llmRx 缺失的能力，按 ROI 排序实施：

| # | 功能 | 优先级 | 估算 | 业界参照 |
|---|---|---|---|---|
| 1 | Prober 集成进 L2 熔断管线 | P0 | ~50 行 | LiteLLM 后台 ping + one-api 探活 |
| 2 | Latency-Aware Routing (延迟感知) | P1 | ~200 行 | LiteLLM latency-based / OpenRouter p50-p99 |
| 3 | Context Window Fallback (上下文升级) | P1 | ~100 行 | LiteLLM context_window_fallbacks |
| 4 | Session Affinity (会话亲和) | P2 | ~230 行 | OpenRouter session stickiness / LiteLLM session_affinity |

总计 ~580 行新代码 + 测试，拆 4 个 commit。

---

## 1. Prober 集成进 L2 熔断管线

### 1.1 问题

prober 已存在（后台 ping `/v1/models`，exponential backoff，real-traffic observer），
但只在 API 层 `model="auto"` 时短路（`api/router.go:876`）。普通路由不受益。

### 1.2 方案

将 prober 注入 `CircuitBreaker.Filter()`，统一在 L2 过滤：

- 新增 `HealthChecker` 接口: `Healthy(channelID int64) bool`
- `CircuitBreaker` 增加 `healthChecker` 字段 + `SetHealthChecker()`
- `Filter()` 在 breaker 检查后增加 prober 检查
- **安全网**: prober 过滤后候选为空 -> 回退到不过滤 (prober 可能 stale)
- 移除 `api/router.go:876-888` 的 auto-only short-circuit (L2 现在统一处理)

### 1.3 改动文件

| 文件 | 改动 |
|---|---|
| `internal/router/breaker.go` | `HealthChecker` 接口 + `Filter()` 增加检查 + 安全网 |
| `internal/router/engine.go` | `SetHealthChecker()` 委托方法 |
| `internal/server/server.go` | prober 创建后注入 router |
| `internal/api/router.go` | 移除 auto-only probe short-circuit |
| `internal/router/breaker_test.go` | 新增 prober 过滤 + 安全网测试 |
| `internal/api/combo_test.go` | 调整 probe 测试适配 L2 行为 |

### 1.4 验收

- 非 auto 请求也跳过 prober 标记不健康的 channel
- 全不健康时安全网放行 (不阻塞流量)
- `go test -race ./...` 全绿

---

## 2. Latency-Aware Routing (延迟感知)

### 2.1 问题

`duration_ms` 记录在日志表和 p95 告警中，但不参与路由决策。L3 只按价格排序，
L5 只按质量采样。慢 channel 和快 channel 被同等对待。

### 2.2 方案

跟踪 per-channel EWMA 延迟，新增 `latency` / `cost_latency_blend` 排序策略。

**EWMA 选择理由**: O(1) 内存，无需环形缓冲，亚毫秒更新。对长尾不敏感
(p95 更好但需 256-1024 样本环形缓冲)；v1 先 EWMA，后续可加 percentile。

### 2.3 新增包 `internal/router/latency/`

```go
type Tracker struct {
    mu    sync.RWMutex
    ewmas map[int64]float64  // channelID -> EWMA ms
    alpha float64            // default 0.1
}
func (t *Tracker) Record(channelID int64, durationMs float64)
func (t *Tracker) LatencyMS(channelID int64) float64  // 0 = unknown
func (t *Tracker) Snapshot() map[int64]float64
func (t *Tracker) Reset()
```

### 2.4 新增策略

- `LatencyStrategy`: 按 EWMA 升序排列，未知延迟 (0) 保持原序 (末尾)
- `CostLatencyBlendStrategy`: `score = cost_weight * cost_norm + latency_weight * latency_norm`
  (默认 0.7/0.3)

### 2.5 改动文件

| 文件 | 改动 |
|---|---|
| `internal/router/latency/tracker.go` | 新文件: EWMA Tracker |
| `internal/router/latency/tracker_test.go` | 新文件: 单测 |
| `internal/router/engine.go` | 增加 latency tracker 字段 + `RecordLatency()` |
| `internal/router/strategy.go` | `LatencyStrategy` + `CostLatencyBlendStrategy` |
| `internal/router/strategy_test.go` | 策略单测 |
| `internal/router/stage.go` | L3 传入 latency tracker 给 blend 策略 |
| `internal/config/config.go` | `latency_routing` 配置段 |
| `internal/api/router.go` | 成功后调用 `RecordLatency()` |
| `internal/admin/handler.go` | state 端点增加 latency 字段 |
| `config.yml` | 配置示例 |

### 2.6 配置

```yaml
latency_routing:
  enabled: false
  alpha: 0.1              # EWMA smoothing factor
  strategy: latency       # latency | cost_latency_blend (when enabled, overrides cost_strategy)
  cost_weight: 0.7        # for blend mode
  latency_weight: 0.3
```

### 2.7 验收

- per-channel EWMA 可在 admin 端点查看
- `latency` 策略按 EWMA 升序排列
- `cost_latency_blend` 策略正确混合
- 冷启动 (无延迟数据) 保持 cost 序
- `go test -race ./...` 全绿

---

## 3. Context Window Fallback (上下文升级)

### 3.1 问题

`FilterContextCandidates` 过滤掉 context 不足的模型，但如果全被过滤则返回原列表
(宁可试也不报错)。不会自动升级到更大 context 的模型，导致超长 prompt 可能失败。

### 3.2 方案

Combo 配置增加 `context_fallback` 字段，当 tier 候选模型的 context 窗口都装不下
prompt 时，自动从 context_fallback 列表中按 context_window 升序选择能装的模型。

### 3.3 改动文件

| 文件 | 改动 |
|---|---|
| `internal/model/types.go` | `TokenComboModel` 增加 `ContextFallback []string` |
| `internal/store/sqlite.go` | combo 存取增加 context_fallback |
| `internal/store/storetest/suite.go` | 测试数据覆盖 context_fallback |
| `internal/router/auto/pool.go` | `SelectContextFallback()` |
| `internal/router/auto/decision.go` | `ContextEscalation bool` 字段 |
| `internal/api/router.go` | `autoAttemptOrder` 增加升级逻辑 |
| `internal/webui/templates/tokens/combo_form.html` | 表单字段 |
| `internal/router/auto/pool_test.go` | 单测 |
| `internal/api/auto_combo_test.go` | 端到端测试 |

### 3.4 配置示例

```yaml
- name: auto
  mode: auto
  tiers:
    simple:   {models: [deepseek-chat, gpt-4o-mini]}
    standard: {models: [deepseek-chat, gpt-4o]}
    complex:  {models: [gpt-4o, claude-3-5-sonnet]}
    agentic:  {models: [claude-3-5-sonnet]}
  fallback: [deepseek-chat]
  context_fallback: [gpt-4o, claude-3-5-sonnet]  # 超长时自动升级
```

### 3.5 验收

- tier 候选全被 context 过滤时自动升级到 context_fallback
- 日志 cause=context_escalation
- combo 表单可配 context_fallback
- 非 auto 模式零回归
- `go test -race ./...` 全绿

---

## 4. Session Affinity (会话亲和)

### 4.1 问题

每次请求独立分类+采样，多轮对话会打到不同 model/channel，破坏 prompt cache
(Anthropic cache_control、OpenAI GPT-5+ prompt cache)。

### 4.2 方案

新增 `internal/router/session/` 包，为多轮对话保持 model+channel 一致性。

**Session ID 来源** (优先级递减):
1. `X-Session-ID` 请求头
2. body `session_id` 字段
3. hash(system prompt + 首条 user message) 自动派生

**行为**:
- 首次请求: 正常分类+采样，成功后创建 pin (TTL 3600s)
- 后续请求: 命中 pin 且 channel健康 -> 跳过分类，直接用 pinned model
- pin 过期或 channel 不健康 -> 重新分类+采样
- 响应头 `X-llmRx-Session-Pinned: true/false`

### 4.3 新增包 `internal/router/session/`

```go
type Pin struct {
    Model     string
    ChannelID int64
    Tier      string
    ExpiresAt time.Time
}
type Store struct {
    mu    sync.RWMutex
    pins  map[string]*Pin
    ttl   time.Duration
    maxN  int
}
func (s *Store) Get(sessionID string) (*Pin, bool)
func (s *Store) Set(sessionID, model string, channelID int64, tier string)
func (s *Store) Delete(sessionID string)
func (s *Store) Cleanup()  // background goroutine
func (s *Store) Snapshot() map[string]Pin
func (s *Store) Count() int
```

### 4.4 改动文件

| 文件 | 改动 |
|---|---|
| `internal/router/session/store.go` | 新文件: session pin store |
| `internal/router/session/store_test.go` | 新文件: 单测 |
| `internal/config/config.go` | `session_affinity` 配置段 |
| `internal/api/router.go` | `handleAutoCombo`/`handleStreamAutoCombo` 入口检查 + 成功后 pin |
| `internal/server/server.go` | 创建 session store + 注入 handler |
| `internal/admin/handler.go` | state 端点增加 session count |
| `config.yml` | 配置示例 |

### 4.5 配置

```yaml
session_affinity:
  enabled: false
  ttl_sec: 3600
  max_entries: 10000
```

### 4.6 验收

- 多轮请求 pin 同一 model
- TTL 过期后重新分类
- channel 不健康时重新分类
- session count 可在 admin 端点查看
- 非 auto 模式零回归
- `go test -race ./...` 全绿

---

## 5. 实施顺序

```
Commit 1: Prober L2 集成     ✅ 811ea07  (~50 行, 最小改动最高 ROI)
Commit 2: Latency-Aware      ✅ b42c59a  (~200 行, 中等复杂度)
Commit 3: Context Fallback    ✅ 4125aee  (~100 行, 防止超长失败)
Commit 4: Session Affinity    ✅ 0d9d0b1  (~230 行, 提升缓存命中)
```

## 6. 风险与注意

- **Prober L2**: prober TTL (60s) 与 breaker 恢复窗口 (60s) 叠加可能过于保守 -> 安全网兜底
- **Latency**: EWMA 对长尾不敏感 -> v1 先 EWMA，后续可加 percentile
- **Context Fallback**: context_fallback 中的模型可能在 tier 候选列表中 -> 去重逻辑复用现有 fallback 去重
- **Session Affinity**: pin 的 channel 下线后需重新分类 -> 健康检查兜底
- **非 auto 零回归**: 所有新功能默认关闭，配置开启后才生效

## 7. 演进兼容

| 能力 | 默认 | 对现有行为的影响 |
|---|---|---|
| Prober L2 | 自动生效 (prober 已运行) | 所有路由受益，安全网兜底 |
| Latency | 默认关闭 | `latency_routing.enabled: false` 时零影响 |
| Context Fallback | 可选配置 | 无 context_fallback 时行为不变 |
| Session Affinity | 默认关闭 | `session_affinity.enabled: false` 时零影响 |
