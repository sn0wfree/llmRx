# 调研：自动规划模型路由

> 状态：调研完成（2026-08-02）。本文是"现状盘点 + 业界调研"的存档，
> 供后续设计讨论引用。实施规划见 [AUTO-ROUTING.md](AUTO-ROUTING.md)。

## 1. 现有路由实现盘点（llmRx）

### 1.1 L1-L5 管线（`internal/router/`）

| 文件 | 职责 |
|---|---|
| `stage.go` | 管线核心：`RouteContext`、`RoutingStage` 接口、`routeWithPipeline()`(37-94)、L4 分区 `splitByIntent`(116-130)、五个 stage |
| `engine.go` | `RouterEngine` 装配（static/breaker/cost/thompson/intent/pool/store），入口 `Route/RouteWith`(175-184)，`RecordSuccess/RecordFailure`(186-200)，运行时开关，Thompson Save/Load(138-146) |
| `static.go` | L1：model 名匹配 channel，按 priority 降序，`Reload()` 原子换快照 |
| `breaker.go` | L2：连续失败 ≥ max_failures(默认5) → Open，超时 60s 半开恢复 |
| `cost.go` | L3：`SortWith` 按策略排序，默认 cheapest |
| `strategy.go` | `CheapestStrategy`(总价升序) / `FastestStrategy`(priority 降序) / `BalancedStrategy`(price×0.5+prio×0.5) |
| `thompson/` | L5：`Sampler`，per-channel Beta(α,β)，`score=(1-blend)·θ+blend·static+explore`，blend=0.3/explore=0.05，冷启动门控 minSamples=5 |

调用链：`/v1/chat/completions`(api/router.go:455) → token 校验 → combo/auto 分叉(501-524) → guardrail(528) → 缓存(534-579) → `RouteWith(ctx, model, {Text})`(586) → L1 匹配 → L2 过滤 → L3 排序 → L4 intent 前移 → L5 采样 → `Candidates[0]`。

### 1.2 关键现状与弱点

1. **Thompson 按 channel 学，不按 model**；无任务类型维度；`docs/ARCHITECTURE.md` L5 设计稿（(task_type,model,channel) 三元组）与实现有偏差。
2. **重试不重路由**：`RetryingProvider`（provider/retry.go）同 channel 指数退避，流式不重试；唯一跨 channel 失败转移是 **serial combo**（api/router.go:827-880）。
3. **modelmeta 不参与路由**：`internal/modelmeta/` 有 LiteLLM 同款价格/context 表（103 模型），只用于 `/v1/models` 展示；L3 用 channel 表自带 `InputPrice/OutputPrice`。
4. **breaker 单维**：无 429/5xx 分桶、无滑动窗口失败率、无主动健康检查。
5. **`model="auto"`** 只解析到 token 的默认 combo（`findDefaultCombo`），无真正自动选择。
6. **L4 intent**：Rust 关键词 5 类（code/chat/summary/translate/math），命中即前移；`X-Task-Type` 头未实现。
7. **combo 两种 mode**：`load_balance`（池化走 L1-L5）、`serial`（顺序尝试首 2xx 胜）。

### 1.3 可复用资产

- `Channel.InputPrice/OutputPrice`（model/types.go:85-86）——成本排序数据已齐
- `modelmeta` 价格表（格式与 LiteLLM 同源）——context 预算/价格归一化可直接启用
- `ComboMode`（model/types.go:57-66）——扩展 `auto` 模式即可挂进现有入口
- `RouteOptions.Text`（engine.go:168-172）——请求文本已进路由上下文，分类器输入现成
- 路由路径日志 `"L1(static) → L2(breaker) → L3(cost) → L5(thompson) → select=..."`（stage.go:96-105）——cause= 决策日志可挂同款格式
- serial combo 的失败转移语义（api/router.go:827-880）——auto 池模型级 fallback 可复用

## 2. 业界调研

### 2.1 LiteLLM Router（策略全景）

来源：https://docs.litellm.ai/docs/routing

| 策略 | 机制 | 关键参数 |
|---|---|---|
| `simple-shuffle`（默认） | 按 rpm/tpm 或 weight 加权随机 | weight/rpm/tpm |
| `usage-based-routing` v2 | 过滤将超限 deployment，选当分钟 TPM 最低（生产用 Redis INCR） | tpm/rpm |
| `latency-based-routing` | 窗口平均响应时间选最低，`lowest_latency_buffer` 防单点打爆 | ttl/buffer |
| `least-busy` | 在途并发最少 | — |
| `cost-based-routing` | 健康 → 过滤超限 → 查 `model_prices_and_context_window.json`（查不到按 $1 哨兵）→ 选最便宜 | in/out 价可覆盖 |
| 自定义策略 | `CustomRoutingStrategyBase` 接口注入 | — |

**cooldown**：429 → 立即冷却 5s；当分钟失败率 >50% → 冷却 5s；不可重试错误(401/404/408) → 5s。默认 `allowed_fails=3`。粒度是 deployment（≠ model group）。到期自动渐进放量、计数清零。全组冷却报 "No deployments available, Try again in 60 seconds"。生产多实例用 Redis 存 cooldown。

**组合性**：Routing Groups（每 model_name 独立策略，支持热更新）；`order` 分阶 + `enable_weighted_failover`（失败后同组内按权重重挑，排除集累积，max_fallbacks=5）；Routing Plugins v1.92+（插件收窄候选/发 signals，顺序：插件 → auto-router → 健康过滤 → 策略 → 出站）。

**RetryPolicy**：按异常类型分别配重试（RateLimitErrorRetries=3、Auth=0、ContentPolicy=3）；fallback 分三类：`fallbacks`(429/5xx)、`context_window_fallbacks`(超长升级)、`content_policy_fallbacks`。

**背景健康检查**：后台循环 ping（默认 300s），失败剔除；429/408 可忽略（transient）；检查失败与真实失败共享计数器；计数器 TTL 必须 > 检查间隔；全不健康时安全网绕过过滤继续发。

### 2.2 LiteLLM 三代"自动路由"（核心参照）

- **Semantic Auto Router**（已弃用）：embedding 匹配 utterances，延迟 100-500ms。
- **Adaptive Router**（BETA，bandit）：7 类任务 × 模型维护 `quality_mean/samples`（Postgres 持久化），quality/cost 权重（如 0.7/0.3）多臂老虎机；冷启动用 quality_tier；格上限 200 观测无衰减；`GET /adaptive_router/{name}/state` 暴露状态。来源：https://docs.litellm.ai/docs/adaptive_router
- **Auto Routing v2 / complexity router**（最新 v1.94+，2026-07）：分类器三层——① 启发式 scorer（**7 维**：tokenCount、codePresence、reasoningMarkers、technicalTerms、simpleIndicators、multiStepPatterns、questionComplexity，亚毫秒）② LLM 分类器（小模型 + structured output，超时/失败回落启发式）③ 关键字规则（可升级 tier、可嵌语义匹配）；tier 值可单模型/随机池/**Thompson-sampling 池**；`session_affinity` 默认开（TTL 3600s 保 prompt cache 与多轮一致性）；决策日志带 `cause=`（complexity_scorer/literal_keyword_match/semantic_keyword_match/llm_classifier/session_affinity_pin）。来源：https://docs.litellm.ai/docs/proxy/auto_routing

### 2.3 OpenRouter

来源：https://openrouter.ai/docs/features/model-routing 、https://openrouter.ai/docs/guides/routing/provider-selection

- **auto-beta**（当前）：轻量分类器归入 ~30 种任务类型 → 查**近 7 天社区 spend share 排名**按真实花费份额排序候选 → `cost_quality_tradeoff`(0-10，默认9) 或 `cost_tier`(low/medium/high/xhigh/max 成本百分位带) 对候选池做成本天花板过滤 → 按 spend-share 序 primary+fallbacks → 分类/排名不可用优雅降级默认集。官方 benchmark：τ-bench 精度翻倍。
- **Session stickiness**：system+首条 user 哈希做会话指纹，provider 报 cache usage 即钉住 model+provider；显式 `session_id` 首次成功即钉；5 分钟不活动过期。
- **Provider 层**（给定 top providers 内选）：默认 price-based load balancing——剔除 30s 内 outage 的 → 低价格候选按**价格倒数平方加权随机**（$1:$2:$3 → 9:4:1，防最便宜被打爆）→ 其余作 fallback。`sort`(price/throughput/latency)、`max_price`(硬性)、`require_parameters`、`partition:"none"`。
- **性能阈值**：`preferred_min_throughput`/`preferred_max_latency` 支持 p50/p75/p90/p99 四档，滚动 **5 分钟窗口百分位**；不达标**降级到列表末尾而非排除**（与 max_price 硬行为区分）。

### 2.4 Routing as a Service（参考不参考）

- **NotDiamond**（闭源）：信号 = 查询复杂度+模型能力+成本+延迟，三档 tradeoff；`trainCustomRouter` 上传 prompt+评分 CSV（25-100 条）聚类学习；RoRF = Routing on Random Forests（2024-09）。核心 router 未开源。
- **Martian**（闭源）：LLM broker，质量分+实时延迟/可用性+价格。
- **AkashML**：去中心化竞价市场 + 地理就近路由（80+ 数据中心），市场定价非内容感知。
- **RouteLLM**（开源学术，Apache-2.0，Python）：Chatbot Arena 人类偏好数据训练强/弱模型二选一路由器，MF+BPR 偏好分 + threshold 校准；**成本降 2 倍质量不降，换模型迁移性好**。论文：https://arxiv.org/abs/2406.18665 代码：https://github.com/lm-sys/RouteLLM

### 2.5 三维权衡的工程要点

- **延迟预估**：p50 排序信号、p95/p99 熔断/降级信号；EWMA O(1) 内存，百分位用环形缓冲/近似分位数（t-digest）；均值易被长尾污染。
- **context 预算**：`enable_pre_call_checks` 路由前过滤 context_window < 本次 token 数的；超限走 `context_window_fallbacks` 小→大升级。OpenRouter 带 max_tokens 只路由能产出该长度的端点。
- **价格归一化**：$/1M tokens 的 in/out 两列分开；未知模型 $1 哨兵防无限选中；估算单次成本 = in×in_price + est_out×out_price（out 按历史 out/in 比预估）。

### 2.6 失败信号全集

429（立即冷却、指数退避、尊重 Retry-After）、错误类型分桶滑动窗口（401/404/408/400/内容策略各独立阈值+TTL）、分钟级失败率 >50% 冷却、主动健康检查（429/408 视为瞬时可忽略）、成功率窗口自动禁用渠道（one-api：`METRIC_QUEUE_SIZE=10` + 阈值 0.8）、模型停服检测（OpenRouter 30s outage 窗口）、非 HTTP 信号（内容策略违规、context 超限预拦截、流式中断、thinking 块不兼容）。

### 2.7 可观测

决策日志一行带 `cause=`；状态端点暴露内部估计（`/adaptive_router/{name}/state`）；响应头 `x-litellm-adaptive-router-model`/`x-litellm-model-id`；**Traffic mirroring**（silent_model 影子请求，异步不阻塞主链路，`metadata["is_silent_experiment"]=True` 剥离日志 ID）；网关开销头 `x-litellm-overhead-duration-ms`（4 实例 1k RPS P95 开销 8ms）；network_mock 无上游压测。

### 2.8 开源 Go 可借鉴

- **one-api**（Go/Gin，MIT，36k★）：成功率队列自动禁用渠道、周期性渠道探活、Redis 多机共享——与 llmRx 形态最接近。https://github.com/songquanpeng/one-api
- **new-api**：one-api 活跃 fork。https://github.com/Calcium-Ion/new-api
- **Envoy AI Gateway**（Go，CNCF）：控制面/数据面分层参考。https://github.com/envoyproxy/ai-gateway
- **Portkey Gateway**（TS）：load balancing/canary/fallback 配置模型。https://github.com/Portkey-AI/gateway
- **LiteLLM**（Python）：`router.py` 是路由逻辑全集，无官方 Go 实现。

### 2.9 可直接抄进 Go 的设计要点

- 核心结构：`Deployment{ID, Group, Provider, Weight, RPM, TPM, MaxInputTokens, ContextWindow, CostIn/OutPerMTok, Order, CooldownUntil, FailCounters, LatencyStats, HealthState}`；请求侧 `RoutingContext{Messages, EstimatedTokens, SessionID, Metadata, Signals}`。
- 过滤流水线固定顺序：硬过滤（cooldown/健康检查/context 预算/region/能力/rpm-tpm 余量）→ 软评分（cost/latency/weight/TS 采样）→ 加权随机 + 有序 fallback。全 O(n)，n<50 无需索引。
- 加权随机：前缀和数组 + 二分，O(log n)；TS 每臂 Beta(α,β) O(1) 采样；冷启动只在 tier 内采样防权重坍缩。
- 滑动窗口：分钟计数 = 时间戳+计数器 / Redis INCR+EXPIRE 60；失败窗口 = `map[errorType]→(count, windowStart)` + cooldown TTL；延迟百分位 = 环形缓冲 256-1024 样本；成功率 = 定长队列滚动平均。
- 熔断状态 `CooldownUntil` 时间戳即可（无定时器）；**安全网：全不健康时绕过过滤继续发**。
- 决策日志 `cause=` 一行 + 响应头回实际模型。

## 3. 与现有能力的映射

| 业界能力 | llmRx 现状 | 差距 |
|---|---|---|
| cost-based-routing + 价格表 | L3 cheapest + channel 价格 | ✅ 已具备 |
| 分阶 order / weighted failover | serial combo / load_balance combo | 部分具备 |
| 失败分桶冷却（429/5xx） | breaker 单维连续失败 | ❌ 需增强 |
| 健康检查路由 | prober（流量观察） | 部分具备 |
| 复杂度分类 → tier 池 | L4 intent（5 类关键词） | ❌ 需新增 |
| quality bandit（task×model） | L5 Thompson（channel 级） | ❌ 维度不足 |
| session affinity | 无 | ❌ 需新增 |
| context 预算过滤 | modelmeta 未参与路由 | ❌ 需启用 |
| 影子流量 | 无 | ❌ 需新增 |
| cause= 决策日志 | router_path 日志 | ✅ 易扩展 |

## 4. 调研局限

- Martian 文档抓取失败（JS 渲染），以其官网定位为准。
- Akash 早期 LLM Broker 文档页随 2026 站点改版 404，以 AkashML 公开资料为准。
- LiteLLM Auto Routing v2 为 2026-07 最新文档（v1.94+）。
