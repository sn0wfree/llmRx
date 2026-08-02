# 智能路由 + 自我进化学习（v1/v2/v3 实施规划）

> 状态：**v1 已实施完成**（2026-08-02，5 个 commit 全部合入）。v2/v3 待排期。
> 调研背景见 [RESEARCH-ROUTING.md](RESEARCH-ROUTING.md)。
> 实施顺序：v1 基础闭环 ✅ → v2 进化成熟（规划）→ v3 离线进化 + 反馈（规划）。

## 0. 已锁定决策汇总

| # | 决策点 | 定稿 |
|---|---|---|
| 1 | 功能边界 | 模型自动选择 + 渠道自动调度（两者都要） |
| 2 | 分类器 | 启发式 7 维 + 可选 LLM 两级（失败回落启发式） |
| 3 | v1 学习 | 启动在线进化（非"不做学习"） |
| 4 | v1 臂维度 | (tier × model) 起步 |
| 5 | v1 reward | 纯成功/失败（α+1/β+1） |
| 6 | v1 持久化 | thompson.json V2（V1 自动迁移），后续升级 store 表 |
| 7 | v1 配套 | cause= 日志 + 响应头 + state 端点（只读 + reset 复用 /reload） |
| 8 | v2 task 来源 | 复用 intent 5 类（数据足够后再重设计 scorer 任务分类） |
| 9 | v2 quality_weight 默认 | **0.3**（LiteLLM 默认，跨档权衡默认开） |
| 10 | v3 影子采样 | 默认关闭，用户显式开（采样率后续定） |
| 11 | v3 反馈形态 | like/dislike（后续可调） |
| 12 | v3 离线工具 | 内嵌 `llmRx -eval-calibrate` 子命令（单二进制） |

## 1. 功能定义

两层自动，入口统一为 **auto 型 combo**（`mode: auto`，请求发 `model=auto` 或该 combo 名）：

1. **模型自动选择**：启发式复杂度分类（可选 LLM 升级）→ 复杂度 tier → 按 tier 的候选模型表 + 成本档选模型
2. **渠道自动调度**：选中模型后走现有 L1-L5 选渠道；模型级失败时按成本升序自动串行尝试 tier 内下一模型，全失败回退 fallback 列表

**自我进化含义**：每笔真实请求更新 (tier×model) 的质量后验（v2 升 (task,tier,model)）→ 低质量高成本模型自动边缘化、高质量低成本模型自动上位，无需人工干预、无需离线训练（v3 再加离线环）。

## 2. 配置形态（v1 定稿）

扩展 `TokenComboModel`（`internal/model/types.go:57` 加 `ComboModeAuto` + `Tiers` 字段），沿用现有 combo CRUD/API。combo 的 `tiers` 以 JSON 存入 `token_combo_models.tiers` 列（表单/API 均可配置）：

```yaml
- name: auto
  mode: auto
  is_default: true
  tiers:
    simple:   {models: [deepseek-chat, gpt-4o-mini]}   # 分数 < 0.25
    standard: {models: [deepseek-chat, gpt-4o]}        # 0.25 ≤ s < 0.55
    complex:  {models: [gpt-4o, claude-3-5-sonnet]}    # 0.55 ≤ s < 0.8
    agentic:  {models: [claude-3-5-sonnet]}            # s ≥ 0.8
  fallback: [deepseek-chat]     # 分类失败/无 tier 命中
```

全局参数（`config.yml`，v1 已实现）：

```yaml
auto_router:
  tier_thresholds: [0.25, 0.55, 0.80]   # 覆盖默认分档阈值（3 个数字）
  llm_classifier:                        # 可选 LLM 分类器（独立端点，不占业务 channel）
    enabled: false
    base_url: http://127.0.0.1:9999/v1
    api_key: sk-classifier
    model: classifier-1b
    timeout_sec: 1.5                     # 超时/失败自动回落启发式（cause=heuristic_fallback）
```

- 每 tier 候选内按 L3 成本策略选（同模型多渠道则 L1-L5 选渠道）
- tier 阈值全局默认、可运行时调整（阈值全局 + combo 可覆盖）
- 4 档 tier（simple/standard/complex/agentic），默认阈值见上
- tier 候选表显式给模型（可解释，非 OpenRouter cost_tier 百分位风格）
- LLM 分类器独立端点配置（小模型端点，不占业务 channel）
- 管理端观测：`GET /admin/api/v1/auto-router/state`（臂 α/β + 决策计数，只读）；`POST /admin/api/v1/reload` 同时重置臂与计数

## 3. 架构

```
model=auto → auto 分支
  ├─ 1. classify(text) → (tier, score, cause)      # 启发式 scorer，LLM 可选升级
  ├─ 2. pool.Select(tier) → [候选模型按成本排序]     # 硬过滤无可用渠道的
  ├─ 3. 臂内 TS 采样选模型                            # v1: (tier×model) Beta
  ├─ 4. 逐个模型走 RouteWith()（L1-L5）             # 失败→下一模型（成本升序）
  ├─ 5. 全败 → fallback 列表串行尝试（绕过 breaker，安全网）
  └─ 6. 决策日志 cause= + 响应头 X-llmRx-Auto-Tier/Routed-Model
```

### 3.1 新包 `internal/router/auto/`

| 文件 | 内容 |
|---|---|
| `scorer.go` | `ComplexityScorer` 接口 + `heuristicScorer`：7 维（tokenCount、codePresence、reasoningMarkers、technicalTerms、simpleIndicators、multiStepPatterns、questionComplexity）加权 → 0-1 分 → tier 映射。亚毫秒、零依赖、纯函数 |
| `llm.go` | 可选 `LLMClassifier`：OpenAI 兼容端点 JSON 分类（复用现有 provider/http client 栈），超时 1.5s/失败/未配置 → 自动回落启发式（cause=heuristic_fallback） |
| `pool.go` | `AutoPool.Select(tier)`：tier 候选 + 成本排序 + 可用性过滤；臂内 TS 采样；`SelectFallback()` |
| `decision.go` | `Decision{Tier, Score, Cause, Candidates, Selected, RoutedModel}` → 结构化日志 |

### 3.2 现有代码修改（v1）

- `internal/model/types.go`：`ComboModeAuto` + `TokenComboModel.Tiers map[string]TierConfig`
- `internal/api/router.go`：`handleCombo`（:658）加 auto 分支；`autoSelectModel` 含模型级失败转移（复用 `handleSerialCombo` 的非 2xx → 下一候选语义）；路由日志追加 `auto_tier=... cause=... score=... arm=... θ=...`
- `internal/router/engine.go`：`RecordSuccess/Failure` 扩展（arm key + status）；thompson 装配升级
- `internal/router/thompson/thompson.go`：Sampler 状态升级 **V2** `{tier:{model:[α,β]}}`，V1 读兼容迁移
- `internal/router/breaker.go`：失败分桶（status 参数、429/401/404/5xx）
- `internal/router/strategy.go`：新增 `weighted-random`（价格倒数平方 9:4:1）
- `internal/router/stage.go`：costStage 支持新策略；L5 上下文传 arm key（auto 场景）
- `internal/admin/handler.go`：combo CRUD 校验 auto 配置 + state 端点（只读）
- `internal/webui/templates/combos/`：表单加 tiers JSON 文本域
- `internal/config/config.go` + `config.yml`：全局 tier 阈值、LLM 分类器端点

## 4. 学习机制设计（v1 启动）

- **臂**：`(tier, model)` 二维（tier 4 档 × 候选 ~10-20 模型 = 40-80 臂，数据密度够）；task 维度 v2 叠加
- **reward**：成功 α+1 / 失败 β+1（现有 Beta 先验）。**成本不进 reward**——成本已由 tier 候选表天然约束（tier=成本档，无跨档权衡），bandit 只管质量
- **冷启动**：臂观测 < MinSamplesPerChannel(5) 时用 tier 静态成本序（现有门控复用）——防初始流量全压最便宜
- **持久化**：thompson.json **Version 2**（`{version:2, channels:{channelID:[α,β]}, arms:{"simple:deepseek-chat":[α,β]}}`），V1 `betas` 文件自动迁移进 `channels`、下次 Save 重写为 v2；5 分钟快照沿用
- **cluster 模式**（P12 PG 多副本）：学习状态共享 → v2 迁 store 表；v1 单机 JSON 文件

## 5. 执行层增强（v1 一并做）

| 项 | 实现 | 改动 |
|---|---|---|
| 失败分桶 | `RecordFailure(channelID, status)`；429 → 冷却 5s；401/404 → 冷却不计重试；5xx 计连续失败（现语义）；分钟失败率>50% 冷却 | breaker.go + engine.go 签名扩展 ~100 行 |
| 加权随机 | 新增 cost 策略 `weighted-random`：价格倒数平方加权（9:4:1），同 tier 内分散流量防打爆 | strategy.go ~40 行 |
| 安全网 | auto 池 fallback 列表**绕过 breaker 过滤**（宁可降级不报错）；全局渠道保持现状（有半开恢复） | 集成点 ~20 行 |
| context 预算 | 估算 token（len/4 近似）→ 过滤 context_window 不足的 channel（启用 modelmeta） | ~50 行 |

## 6. 观测与可解释

- 决策日志：`auto_tier=complex cause=heuristic score=0.71 arm=gpt-4o θ=0.62 → routed=deepseek-chat`
- 响应头：`X-llmRx-Auto-Tier` / `X-llmRx-Routed-Model`
- admin 状态端点 `GET /api/v1/auto-router/state`：臂 α/β、tier 命中分布、cause 分布（只读 + reset 复用 /reload）——"看见它在进化"

## 7. 明确不做（v1）

- task 维度臂、quality_weight 跨档权衡、cluster 学习状态入 store（v2）
- 影子流量、人工反馈、离线评估、session affinity（v3 及以后）
- 延迟窗口排序（latency-aware provider 调度，需延迟统计）
- 管理端可视化（v3）

## 8. 实施顺序（v1，5 个 commit）✅ 全部完成

1. **thompson V2**：维度升级 + V1 兼容 + 单测（迁移/持久化/并发）✅
2. **分类器**：`scorer.go` + tier 映射 + 单测（中文/空输入/各维度边界）✅
3. **选择与集成**：`pool.go` + `decision.go` + types/combo auto 分支 + 失败转移 + 端到端测试（mock provider）+ cause= 日志 + 响应头 ✅
4. **执行层增强**：失败分桶（RecordFailure 签名扩展）+ weighted-random + context 过滤 + 安全网 + 单测 ✅
5. **配套**：LLM 分类器（mock 回落测试）→ admin state 端点 + combo 表单 → config.yml 示例 → 文档 → 全量回归（-race + PG 复测）✅

## 11. 实施记录（v1 已完成）

| commit | 内容 |
|---|---|
| `61f0948` | thompson V2：双臂空间（channel + `"tier:model"`）、V1 文件自动迁移、`SampleArms` |
| `4513542` | 启发式分类器：7 维加权 + 4 档 tier 映射（CJK 公平计量、短文本惩罚抑制） |
| `ef23161` | auto 集成：combo `mode:auto` + tiers 列、TS 臂采样、模型级失败转移、fallback 去重、`auto.decision` 日志 + `X-llmRx-Auto-Tier/Routed-Model` 头 |
| `b27ea14` | 执行层：失败分桶（401/404 hard-reject、429 5s、分钟窗口失败率）、`weighted_random` 策略、context 预算过滤、fallback 绕过熔断安全网 |
| commit 5 | LLM 分类器（1.5s 超时回落 heuristic_fallback）、`auto.Stats` 决策计数、admin state 端点（只读 + /reload 重置）、webui 表单（auto 模式 + tiers JSON + fallback）、`auto_router` 配置、文档 |

## 12. 设计符合性对照（v1 已核验）

实现对照设计 §1-§6 逐项核验（2026-08-02，代码级）：

| 设计项（§） | 实现位置 | 状态 |
|---|---|---|
| 启发式 7 维分类 → 0-1 分 → 4 档 tier（§1/§3.1） | `auto/scorer.go`（CJK 公平计量、短文本惩罚抑制，优于设计的 len/4 近似） | ✅ |
| 可选 LLM 分类器，1.5s 超时/失败回落 `heuristic_fallback`（§3.1） | `auto/llm.go` | ✅ |
| `mode:auto` combo + tiers JSON 入库（tiers/fallback 列 + 迁移）（§2/§3.2） | `model/types.go` + `store` | ✅ |
| 全局阈值 + LLM 分类器配置（§2） | `config.go` `auto_router` 段（含测试） | ✅ |
| 池选择：可用性硬过滤 + 成本序 + TS 臂采样（§3/§4） | `auto/pool.go` + thompson `SampleArms` | ✅ |
| 模型级失败按成本序转移 + fallback 串行（去重、`SkipBreaker` 安全网）（§1/§5） | `api/router.go:963,1044,1091` | ✅ |
| 学习闭环：(tier,model) 臂 α/β、纯成功失败 reward、冷启动门控、V1→V2 迁移、5 分钟快照（§4） | `thompson/thompson.go:118-186,380` | ✅ |
| 失败分桶：429→5s、401/404 hard-reject、分钟窗口 >50% ≥10 样本冷却（§5） | `breaker.go:21-29,257` | ✅ |
| `weighted_random` 策略（1/p²，9:4:1 分布测试）（§5） | `strategy.go:94` | ✅ |
| context 预算过滤（`TokenEstimate`×1.2、绝不饿死）（§5） | `auto/pool.go:59` + `api/router.go:1091` | ✅ |
| 决策日志 + `X-llmRx-Auto-Tier/Routed-Model` 头（普通+流式都发）（§6） | `api/router.go:1144,1164` | ✅ |
| `GET /admin/api/v1/auto-router/state` 只读 + `/reload` 重置（§2/§6） | `admin/handler.go:130` | ✅ |
| 决策统计 `auto.Stats`（tier_hits/cause_hits/fallbacks…）（§6） | `auto/stats.go` | ✅ |

验收标准（§9）逐条核对：auto 链路端到端（mock provider 的 `TestAutoCombo_*`）✅；V2 读写 + V1 迁移 + 快照 ✅；决策日志全字段 + 响应头 ✅；分桶可测 + weighted-random 9:4:1 ✅；非 auto 零行为变化（`-race` 全量绿）✅；`-race` + PG DSN 复测全绿 ✅。

**结论：满足设计要求，无功能缺失。** 与设计描述的 3 处小偏差均为实现更优或位置不同：

- §3.2 设计写"`handleCombo` 加 auto 分支"，实际拆为独立的 `handleAutoCombo`/`handleStreamAutoCombo`——流式请求也支持了（设计未要求）
- 设计写"`SelectFallback()` 在 pool.go"，实现把 fallback 逻辑放在 api 层（含去重 + 绕过 breaker 合并处理），功能等价
- `RecordFailure(channelID, status)` 签名符合设计，但 arm 更新走独立的 `RecordArmSuccess/Failure`（成功路径无需 status，更干净）

唯一环境性缺口：`data/config.yml` 为 gitignored 本地文件未入库，但示例已完整写进本文件 §2，不构成功能缺失。

## 9. 验收标准（v1）

- `model=auto` 按复杂度落 tier → 臂采样选模型 → L1-L5 渠道；失败自动转移 + fallback 安全网
- thompson.json V2 读写 + V1 文件平滑迁移；5 分钟快照不丢
- 决策日志含 `tier/cause/score/arm/θ/routed_model`；响应头返回
- 429/401/404/5xx 分桶冷却行为可测；`weighted-random` 分布符合 9:4:1
- 非 auto 模式零行为变化（L5 维度升级不触达普通路径）
- `-race` 全量 + PG DSN 复测全绿

## 10. 工作量评估

v1 新代码约 700-900 行 + 测试；不动现有 L1-L5 行为（默认 mode 非 auto 时零影响）。

---

# v2：进化成熟（6 个 commit）

## 目标

- (tier×model) → **(task, tier, model) 三维臂**（LiteLLM 同规模，格上限 200 观测）
- 跨档权衡打开（`quality_weight` 默认 **0.3**）
- 学习状态入 PG store（P12 cluster 多副本共享）
- 失败信号精细化（分钟滑动窗口失败率）

## 改动

**修改**
- `internal/router/thompson/thompson.go`：Sampler 状态 key `int64` → `string`（`"task:tier:model"`）；Save/Load **Version 3**（V2 兼容迁移）；并发 safe
- `internal/router/auto/scorer.go`：scorer 同时输出 `task_type`（复用 intent 5 类 via Classifier）
- `internal/router/auto/pool.go`：`score = (1-w)·quality_norm + w·cost_norm`（w=quality_weight 配置，默认 0.3）
- `internal/router/breaker.go`：分钟失败率滑动窗口（环形缓冲/计数器 TTL 桶）+ 阈值冷却
- `internal/store/`：新表 `quality_stats(arm_key, task, tier, model, alpha, beta, samples, last_updated)`，dialect 双驱动（SQLite/PG），`INSERT ... ON CONFLICT UPDATE` 原子
- `internal/store/thompson_repo.go`：learning 表 CRUD（仿 alert_repo 模式）+ storetest 扩展
- `internal/router/engine.go`：thompson Save/Load 走 store（cluster 模式分支判定）；单机仍 JSON 文件
- `internal/admin/handler.go`：state 端点加 task/tier 筛选 + quality_weight 运行时调整
- `internal/config/config.go` + `config.yml`：`quality_weight`(默认 0.3)、`quality_tier` 预设（冷启动每模型先验质量档）、`learning_store`(auto/json)、`minSamples` 可调
- `docs/AUTO-ROUTING.md`/ARCHITECTURE：v2 章节

## commit 划分

1. `quality_stats` store 表 + dialect + storetest + 迁移
2. multi-key thompson（string arm）+ V3 格式 + cluster 分支（单机仍 JSON）+ 兼容迁移
3. task 维度接入：scorer 输出 task、pool 用 multi-key arm、冷启动 quality_tier 预设
4. quality_weight 跨档权衡 + cost_norm 归一 + 单测
5. 增强失败信号（分钟失败率滑动窗口 + 阈值冷却）+ 单测
6. admin state 多维筛选 + 运行时调 quality_weight + 文档

## 验收

- 三维臂数据在 PG store 共享，两副本可见同后验
- quality_weight=0.3 时低成本高质量模型自动平衡（跨档权衡默认开）
- V2 thompson.json 单机启动自动迁移到 store（cluster 模式）
- 分钟失败率 >50% 自动冷却，可测
- 非 cluster 模式零回归（仍走 JSON 文件）

## 风险与注意

- **数据稀疏**：task×tier×model = 5×4×15 = 300 臂，冷启动收敛慢 → 缓解：quality_tier 先验 + 实施顺序先 tier×model 收敛再加 task
- **quality_weight 行为变化**：v1 成本由 tier 硬约束；v2 默认 0.3 跨档权衡开——升级行为变化，需 CHANGELOG 标注；w=0 严格回 v1
- **分布式并发**：PG 原子更新（INSERT ON CONFLICT）避免两副本同时 incr 丢失；冷启动期容忍少量重复观测

---

# v3：离线进化 + 反馈（6 个 commit）

## 目标

- **影子流量 mirroring**：主请求返回后异步发到候选模型，不阻塞主链路采集对比数据
- **人工反馈**：请求级 like/dislike，存 feedback 表
- **离线评估**：`llmRx -eval-calibrate` 子命令——重放 shadow_samples 校准 tier 阈值、更新质量先验、AB 对比报告
- **可视化**：admin dashboard 看见进化趋势（臂质量分布、tier 命中热力、cause 分布）

## 改动

**新增**
- `internal/shadow/`（影子包：mirror 调度、采样配置、剥离日志 ID）
  - `mirror.go`：主请求成功后 `go func` 打影子请求（`metadata["is_silent_experiment"]=True`，剥离日志 ID 防撞）
  - `sampler.go`：采样率（默认关闭，用户显式开；日预算硬上限）
  - `shadow_test.go`：httptest mock + 不阻塞主链路验证
- `internal/feedback/`（反馈包）
  - `store.go`：feedback CRUD
  - `aggregator.go`：反馈聚合到 quality 先验（周期任务）
- `cmd/gateway`：`-eval-calibrate` 子命令（阈值校准 / 先验更新 / AB 报告）

**修改**
- `internal/store/`：新表 `shadow_samples(id, request_fingerprint, prompt_summary, tier, arm, routed_model, shadow_model, shadow_status, shadow_cost, shadow_latency, created_at)` + `feedback(id, request_id, user_rating, model, tier, comment, created_at)`；保留期清理（沿用 logstore 模式）
- `internal/api/router.go`：主请求成功 hook → 触发 mirror goroutine（受采样率 + 配置开关）
- `internal/admin/handler.go`：`POST /api/v1/feedback`（like/dislike）+ `GET /api/v1/auto-router/trends` + 影子配置
- `internal/webui/templates/`：进化趋势卡片（复用现有 charts）+ 评分 UI
- `internal/config/config.go`：`shadow{enabled, sample_rate, models, budget_per_day}`、`feedback{enabled}`
- `docs/AUTO-ROUTING.md`：v3 章节

## commit 划分

1. `shadow` 包 + 异步调度 + 采样配置 + 不阻塞主链路单测
2. `shadow_samples` 表 + dialect + 保留期清理 + 样本持久化
3. `feedback` 接口 + 表 + admin UI 评分（like/dislike）
4. `-eval-calibrate` 离线工具（校准阈值 + 先验更新 + AB 报告）
5. 趋势端点 + dashboard 可视化卡片（臂质量分布/tier 热力/cause 饼图）
6. 文档 + 全量回归

## 验收

- 影子请求不增加主链路 P99 延迟（benchmark 对比）
- 采样率可配，日成本预算上限；默认关闭
- like/dislike 评分可聚合进 quality 先验（离线任务）
- 离线工具重放后能输出"建议阈值"
- dashboard 展示进化趋势可解释

## 风险与注意

- **影子成本翻倍**：采样率默认低 + 每模型日预算硬上限；默认关闭
- **反馈稀疏**：like/dislike 比 5 星易收集；可按 tier 采样主动请求反馈
- **prompt 隐私**：shadow_samples 只存摘要/fingerprint，不存原文（guardrail 友好）
- **离线评估偏差**：shadow 样本 ≠ 生产分布，AB 报告需标注采样条件

---

# 演进兼容矩阵

| 阶段 | thompson 格式 | 学习信号 | 持久化 | 兼容性 |
|---|---|---|---|---|
| 现（pre-v1） | V1 `{channelID:[α,β]}` | 成功/失败（channel 级） | thompson.json 文件 | — |
| **v1** | V2 `{channels:{id:[α,β]}, arms:{"tier:model":[α,β]}}` | 成功/失败（tier×model 臂） | thompson.json 文件（V1 自动迁移） | 非 auto 模式零变化 |
| v2 | V3 `{task:{tier:{model:[α,β]}}}` | 成功/失败（三维） | store 表 + JSON 文件（cluster 分支） | V2 → store 迁移；quality_weight=0 回 v1 |
| v3 | V3（不变） | + 影子样本 + like/dislike | + shadow_samples / feedback 表 | 影子与反馈默认关闭 |

# 数据流全图

```
请求 → 分类(tier) ─→ 臂选择(TS) ─→ 渠道(L1-L5) ─→ 调用
            │           │                          │
            │     [v2 task 维度臂]                  ↓
            │     [v2 quality_weight=0.3 跨档]   反馈(success+status)
            │           │                          │
            │           ↓                          ↓
            │     5min 快照 ← (tier×model) Beta 更新
            │           │  [v2 store 表共享]
            │     [v3 影子异步] → shadow_samples ──┐
            │                                     ↓
            │     [v3 like/dislike] → feedback ─→ 离线评估
            │                                     ↓
            └── [v3 llmRx -eval-calibrate 校准阈值/先验/AB] ←┘
```
