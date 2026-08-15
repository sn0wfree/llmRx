# 内容感知模型选择（v2 智能路由）

> 状态：**设计阶段**，等待实施批准
> 前置：v1 智能路由已完成（见 [AUTO-ROUTING.md](AUTO-ROUTING.md)）+ 4 个业界差距已补齐（见 [SMART-ROUTING-GAPS.md](SMART-ROUTING-GAPS.md)）
> 调研背景：见 [RESEARCH-ROUTING.md](RESEARCH-ROUTING.md)，业界参照见末尾对照表

## 0. 已锁定决策汇总

| # | 决策点 | 定稿 |
|---|---|---|
| 1 | 与 v1 关系 | **增强现有 mode:auto**（不新增 mode） |
| 2 | 内容维度 | **任务匹配 + 综合权衡**（任务×模型质量 + 成本） |
| 3 | 能力约束 | **硬过滤**（vision/tools/JSON/reasoning 不匹配则剔除候选） |
| 4 | 任务分类器 | **启发式 + 可选 LLM**（双轨，沿用 v1 模式） |
| 5 | 任务类型数量 | **8 类**（对齐 LiteLLM-adaptive 7 类 + 1 个 vision） |
| 6 | 模型→能力来源 | **modelmeta**（已有 102 模型 + capabilities，零接入成本） |
| 7 | 状态信息 | **Decision 字段扩展** + `cause=` 日志 + 响应头 |
| 8 | v2 学习维度 | **(task × tier × model)** —— v1 的 (tier×model) 升一维 |
| 9 | 质量/成本权重 | **quality_weight=0.7 / cost_weight=0.3**（LiteLLM 默认） |
| 10 | 任务上下文窗口 | **可选**（off 默认，agentic 场景手动开） |
| 11 | 中文任务检测 | **V1 仅英文**（路线图 V2 增量补），与业界 RouteLLM/LiteLLM 一致 |

---

## 1. 问题定义

v1 智能路由（mode:auto）已完成"**内容 → 模型**"的闭环，但只用了**复杂度一维**：

```
content → classify(text) → tier ∈ {simple,standard,complex,agentic}
       → tier candidates (人工表) → TS 采样 → picked model
```

### v1 缺失的 3 类信号

| 信号 | 现状 | 影响 |
|---|---|---|
| **任务类型**（code/writing/summary/translation/math/chat/vision） | 完全没识别 —— 现有 intent 分类器只作用在 L4（渠道标签匹配），不影响模型选择 | code 请求走 cheap 模型可能答不对，writing 请求走 o1 浪费 |
| **能力约束**（tool_use / vision / json_mode / reasoning） | modelmeta 102 模型 capabilities 字段**零参与路由**（只用于 /v1/models 展示和 context window 计算） | 请求带 `tools=[...]` 命中不支持工具的模型 → 400 错误 |
| **对话上下文**（多轮 agentic 场景） | 只看最后一条 user 文本 | agentic 场景每轮都是 `<system-reminder>` 短文本，分类失真 |

业界对照（详见末尾业界调研）：

- **LiteLLM Adaptive Router** —— 7 类任务 × per-model quality 矩阵，冷启动用 `strengths` 声明（最接近）
- **OpenRouter Auto Router** —— ~30 细粒度任务 + 市场消费份额排序（规模化数据驱动）
- **RouteLLM** —— 强/弱二元 + 训练分类器（不涉及多模型能力分发）
- **Portkey Conditional** —— 规则式（`metadata.task=coding` → 指定模型），非学习

---

## 2. 设计目标

### 2.1 功能目标

1. **任务匹配**：自动识别 8 类任务（见 §3），按任务×模型质量学习排序
2. **能力硬过滤**：请求带 tools/vision/json/reasoning 信号 → 仅保留支持的模型作为候选
3. **多轮上下文**：可选把对话历史喂给 LLM 分类器（缓解 agentic 失真）
4. **可观测**：每笔请求的 Decision 暴露 `task / needs / filtered / cause`，写入响应头和日志

### 2.2 非目标（明确不做）

- **不做 LLM 评判**（LiteLLM adaptive 已知局限，正面避开）
- **不做训练式路由器**（RouteLLM 路线，需要偏好数据，超出范围）
- **不做内容缓存/语义缓存**（P8 已规划）
- **不做按 metadata 显式路由**（Portkey 风格，YAGNI，留作 §10 未来扩展）
- **不做用户反馈环**（v3 范围）

### 2.3 兼容性目标

- **零破坏**：未配置 v2 字段的 combo 行为等同 v1（能力过滤是兜底默认开，任务识别只在配置后激活）
- **零迁移**：现有 token_combo_models.tiers 数据无需改动
- **渐进启用**：每项能力都有显式开关，默认保守（见 §4）

---

## 3. 任务类型（8 类）

对齐 LiteLLM Adaptive Router 7 类 + vision 类（多模态近年显著增长）：

| Task | 启发式关键词（英文） | 示例请求 | 候选模型倾向 |
|---|---|---|---|
| `code_generation` | function, implement, write code, class, api, algorithm, refactor | "write me a Python sort function" | code-strong 模型 |
| `code_understanding` | explain code, what does this, debug, error, traceback | "why does this throw null pointer" | reasoning-strong |
| `writing` | write essay, draft, story, blog, email, summarize section | "draft a marketing email" | writing-strong（claude 偏好） |
| `analytical_reasoning` | analyze, evaluate, compare, pros and cons, probability, prove | "calculate the probability that..." | reasoning-strong |
| `math` | equation, integral, derive, solve, simplify, 求解 | "solve this differential equation" | math-strong（o1/o3 偏好） |
| `factual_lookup` | what is, who is, when did, define, capital of | "what is the capital of France" | cheap 模型（factual 简单） |
| `translation` | translate, translation, 翻译 | "translate this to French" | 翻译强模型 |
| `vision` | （不依赖文本）请求 messages 含 image_url/multipart | （image input） | vision-capable 模型 |

**fallback**：`general` —— 启发式分数低 + LLM 失败 → 不分流，按 v1 复杂度路径走。

### 3.1 中文支持（V1 不做）

V1 仅英文关键词，与 RouteLLM/LiteLLM 业界现状一致。
**路线图 V2**：根据真实流量统计，补充高频中文关键词（"写个"/"实现"/"函数"/"代码"/"翻译"等）。

### 3.2 与现有 intent 分类器的关系

**不冲突**：
- intent（5 类：code/chat/summary/translate/math）继续作用在 **L4 渠道层**（channel.Intents 标签前移）
- 新增 task（8 类）作用在 **第一层模型选择**

如未来需要合并，两者任务分类器可统一（共用启发式 scorer，输出拆分多类）。当前解耦保持改动局部化。

---

## 4. 能力硬过滤

### 4.1 设计原则

**能力不匹配 = 必须过滤**（不是软排序，是硬剔除）：
- 请求带 `tools` → 仅保留 `supports_tool_use` 的模型
- 请求带 `image_url` content part → 仅保留 `supports_vision` 的模型
- 请求带 `response_format: json_object` → 仅保留 `supports_json_mode` 的模型
- 请求带 `reasoning_effort` / `thinking` 字段 → 仅保留 `supports_reasoning` 的模型

**宽松策略**：未知能力的模型**保留**（不剔除）。理由：
- modelmeta 不全（claude-3-5-sonnet 没有条目）
- 宁可多发一次被 provider 400 拒绝，比漏发到正确模型但被剔除更糟
- LLM 失败时仍能 fallback

### 4.2 Needs 提取（第一层输入 → 候选过滤）

```
// auto/needs.go （新文件 ~80 行）
func ExtractNeeds(req *provider.ChatRequest) Needs {
    needs := Needs{} // 零值 = 无需求 = 不过滤
    if len(req.Tools) > 0 { needs.Tools = true }
    for _, m := range req.Messages {
        for _, p := range m.ContentParts {           // 多模态 content parts
            if p.Type == "image_url" { needs.Vision = true }
        }
    }
    if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
        needs.JSONMode = true
    }
    if req.ReasoningEffort != "" || req.Thinking != nil {
        needs.Reasoning = true
    }
    return needs
}
```

### 4.3 候选过滤（第二层 → 与 context fallback 串联）

复用 v1 的 context fallback 串联模式（`auto/pool.go:FilterContextCandidates`），新增一层 capability filter：

```
FilterContextCandidates (v1 已有)
  → FilterByCapabilities (v2 新增)
    → TS 采样
```

过滤函数（`auto/capability.go`，~60 行）：

```
func FilterByCapabilities(candidates []string, needs Needs, meta *modelmeta.Registry) []string {
    if needs.IsEmpty() { return candidates }  // 无需求 = 不过滤
    out := make([]string, 0, len(candidates))
    for _, m := range candidates {
        cm := meta.Get(m)
        if cm == nil { out = append(out, m); continue }  // 未知 → 保留
        if needs.Tools     && !cm.Capabilities.ToolCall    { continue }
        if needs.Vision    && !cm.Capabilities.Attachment { continue }
        if needs.JSONMode  && !cm.Capabilities.JSONMode    { continue }
        if needs.Reasoning && !cm.Capabilities.Reasoning   { continue }
        out = append(out, m)
    }
    return out
}
```

### 4.4 全过滤安全网（与 v1 context fallback 同模式）

如果 capability filter 把所有候选剔除：
1. 检查 `combo.CapabilityFallback`（v2 新字段，可选）—— 显式声明的兜底模型
2. 没有 fallback → 沿用 v1 context fallback 的"安全网"：跳过能力过滤（宁可被 provider 400，比漏发到正确模型更糟）
3. 触发原因写 `cause=capability_fallback`

### 4.5 已知 modelmeta 现状问题

- `claude-3-5-sonnet` 没有 modelmeta 条目（未知 → 宽松策略自动放行，OK）
- `JSONMode` 当前从 `hasTools` 派生（registry.go:266），所以**有 tools 的请求被 JSONMode 过滤的概率为 0**（互相蕴含）。无需特殊处理，但后续 modelmeta 重构时需注意。
- `supports_reasoning` 仅 3 个模型（o1/o3 series）有

---

## 5. 任务识别（启发式 + 可选 LLM）

### 5.1 双轨设计

与 v1 复杂度分类完全同构：

```
1. 启发式 scorer.scoreTask(text) → {code_generation, code_understanding, ..., general}
2. 若 combo/task_classifier=llm 配置 → 异步调用小模型 → 失败回落启发式
3. fallback: general（与 v1 complexity fallback 同模式）
```

### 5.2 启发式 scorer（`auto/task_scorer.go`，~120 行）

复用 v1 的启发式 scorer 架构，输出从单一分数变为**8 类 softmax 概率**：

```
type TaskScore struct {
    Task   Task   // 8 类枚举
    Scores map[Task]float64  // 各类概率
    Cause  string  // "task_keyword" / "task_llm_classifier" / "task_fallback"
}

var taskKeywords = map[Task][]weightedKeyword{
    code_generation: {{"function", 2.0}, {"implement", 2.0}, {"class", 1.5}, ...},
    writing:         {{"draft", 2.0}, {"essay", 2.0}, {"blog", 1.5}, ...},
    ...
}

func scoreTask(text string) TaskScore {
    counts := countMatches(text, taskKeywords)
    return softmax(counts) → pick max (or general if all below threshold)
}
```

**关键设计**：与 v1 complexity score 并存，互不影响：
- `scoreComplexity(text)` → (tier, complexity_score) —— v1
- `scoreTask(text)` → (task, task_scores) —— v2
- 两者同时跑，分别决策

### 5.3 LLM 升级（沿用 v1 配置）

完全复用 v1 `auto_router.llm_classifier` 配置，新增一个字段区分用途：

```yaml
auto_router:
  llm_classifier:           # 复杂度（v1，已有）
    enabled: false
    base_url: ...
    model: classifier-1b
    timeout_sec: 1.5
  task_classifier:          # 任务（v2 新增，独立端点独立模型）
    enabled: false
    base_url: ...
    model: classifier-1b   # 推荐小模型 Haiku/gpt-4o-mini
    timeout_sec: 1.5
    context_window_size: 0  # 默认 0（关闭）；3 表示看最近 3 轮
```

### 5.4 多轮上下文（可选，对齐 LiteLLM）

`task_classifier.context_window_size > 0` 时启用：
- 把最近 N 轮 user 消息拼成 `[1] ... [2] ... [3] ...` 喂给 LLM 分类器
- 截断每轮 200 字符（与 LiteLLM 对齐）
- agentic 场景启用：解决每轮都是 `<system-reminder>` 短文本的失真问题
- 默认 0 关闭，避免额外延迟和成本

---

## 6. 排序与学习

### 6.1 评分公式（v2 新增）

v1 仅按成本和 TS 质量臂采样选模型。v2 综合权衡：

```
final_score(model, task, tier) =
    quality_weight × TS_sample(task, tier, model)      # 0..1
  + cost_weight    × normalize_cost(model)            # 0..1（高 cost → 低分）
```

默认 `quality_weight=0.7, cost_weight=0.3`（LiteLLM 默认）。

### 6.2 学习维度升级

v1: `Beta(α, β)` per `(tier × model)`
v2: `Beta(α, β)` per `(task × tier × model)` —— 上一维 +1

**升级路径**（兼容）：
- 新增独立 bandit map `bandits_by_task[task][tier][model]`
- v1 的 `bandits[tier][model]` 保留作为未识别任务（`general`）的兜底
- 数据结构：v2 bandit 状态写 `thompson.json` 的新字段 `v2_bandits`，V1 字段保留只读
- 不需要迁移：启动时若 `v2_bandits` 不存在，全新请求落到 `general` bandit（与 v1 等价）

### 6.3 冷启动

观测 < 5 的 (task,tier,model) 臂：跳过 TS 采样，回退到**成本升序**的第一个。
**与 v1 不同**：v2 冷启动同时考虑 `task` 倾向（如 `code_generation` 任务优先选 `complex` tier 候选里 code-strong 的模型），可用 modelmeta 的 `code_generation` strength tag（OpenRouter-style）或 `auto_router.task_model_bias` 配置声明：

```yaml
auto_router:
  task_model_bias:           # 任务 → 模型偏好（冷启动 + tiebreak）
    code_generation: [claude-3-5-sonnet, gpt-4o]
    writing:         [claude-3-5-sonnet]
    analytical_reasoning: [o1-mini, claude-3-5-sonnet]
```

未声明的模型走纯 TS 学习。

### 6.4 cost 归一化

`normalize_cost(model)` = `1 - (cost(model) - min_cost) / (max_cost - min_cost)`
- 候选集内 min/max 归一化（不是全局 modelmeta 全部模型）
- 未知 cost 的模型按 0 处理（即 cost_weight 项无影响）

---

## 7. Decision 字段与可观测性

### 7.1 Decision 结构扩展（`auto/decision.go`）

```go
type Decision struct {
    // v1 fields (existing)
    Tier      Tier     `json:"tier"`
    Score     float64  `json:"score"`
    Cause     string   `json:"cause"`        // "complexity_scorer" | "llm_classifier" | ...

    // v2 fields (new)
    Task       Task            `json:"task"`
    Needs      Needs           `json:"needs"`
    TaskScores map[Task]float64 `json:"task_scores,omitempty"`
    Filtered   []FilteredCandidate `json:"filtered,omitempty"`  // 被剔除的 (model, reason)
    QualityWeight float64      `json:"quality_weight"`
    CostWeight    float64      `json:"cost_weight"`
    BanditKey     string       `json:"bandit_key"` // e.g. "code_generation:complex:claude-3-5-sonnet"
}

type FilteredCandidate struct {
    Model  string `json:"model"`
    Reason string `json:"reason"` // "no_tools" | "no_vision" | "no_json" | "context_window" | "fallback"
}
```

### 7.2 响应头扩展

```
X-llmRx-Auto-Tier:           complex
X-llmRx-Auto-Task:           code_generation
X-llmRx-Auto-Cause:          task_keyword
X-llmRx-Auto-Bandit-Key:     code_generation:complex:claude-3-5-sonnet
X-llmRx-Auto-Filtered-Count: 2   # 被能力/context 过滤掉的模型数
```

### 7.3 日志扩展

每笔决策一行（含 v1 全部 cause，新加 task 系 cause）：

```
ComplexityRouter: routing decision cause=complexity_scorer, tier=COMPLEX, task=code_generation, score=0.85, signals=[...], needs={tools:true}, filtered=[deepseek-chat:no_tools], routed_model=claude-3-5-sonnet
```

### 7.4 状态端点扩展

`GET /admin/api/v1/auto-router/state` 增加：
- `bandits_by_task` —— 全 (task,tier,model) 臂的 α/β
- `task_distribution` —— 各任务占比
- `capability_filter_rate` —— 能力过滤命中率

---

## 8. 配置形态

### 8.1 combo 字段（`TokenComboModel` 扩展）

```yaml
- name: auto-v2
  mode: auto
  is_default: true
  tiers:                       # v1 已有
    simple:   {models: [deepseek-chat, gpt-4o-mini]}
    standard: {models: [deepseek-chat, gpt-4o]}
    complex:  {models: [gpt-4o, claude-3-5-sonnet]}
    agentic:  {models: [claude-3-5-sonnet]}
  fallback: [deepseek-chat]
  capability_fallback: [gpt-4o]   # v2 新增：全能力过滤后兜底
  task_model_bias:                 # v2 新增：冷启动偏好
    code_generation: [claude-3-5-sonnet]
    writing:         [claude-3-5-sonnet]
```

### 8.2 全局参数（config.yml）

```yaml
auto_router:
  tier_thresholds: [0.25, 0.55, 0.80]
  
  # v1 (existing)
  llm_classifier:
    enabled: false
    ...
  
  # v2 (new)
  task_classifier:
    enabled: false
    base_url: http://127.0.0.1:9999/v1
    api_key: sk-classifier
    model: classifier-1b
    timeout_sec: 1.5
    context_window_size: 0         # 默认 0；agentic 建议 3
  quality_weight: 0.7             # 默认 0.7
  cost_weight: 0.3                # 默认 0.3
  capability_filter: true         # 默认 true（兜底硬过滤）
  task_recognition: true          # 默认 true（识别但不强制使用）
```

---

## 9. 改动清单

### 9.1 新增文件

| 文件 | 行数估计 | 用途 |
|---|---|---|
| `internal/router/auto/needs.go` | ~80 | Needs 结构 + ExtractNeeds |
| `internal/router/auto/capability.go` | ~80 | FilterByCapabilities |
| `internal/router/auto/task_scorer.go` | ~150 | 8 类关键词 + softmax |
| `internal/router/auto/task_classifier.go` | ~100 | LLM 升级（沿用 v1 框架） |
| `docs/CONTENT-AWARE-ROUTING.md` | (本文件) | 设计文档 |

### 9.2 改动文件

| 文件 | 改动 |
|---|---|
| `internal/router/auto/types.go` | Decision 字段扩展（Task/Needs/TaskScores/Filtered/QualityWeight/CostWeight/BanditKey） |
| `internal/router/auto/scorer.go` | `scoreTask()` 新增；与 `scoreComplexity()` 并存 |
| `internal/router/auto/decision.go` | 决策构造函数接受 task/needs |
| `internal/router/auto/pool.go` | `FilterByCapabilities` 串联到 `FilterContextCandidates` 后 |
| `internal/router/auto/bandit.go` | 新增 `bandits_by_task[task][tier][model]`；保留 v1 `bandits[tier][model]` 作 general 兜底 |
| `internal/router/auto/thompson.go` | `Select(task, tier, candidates)` 接受 task 参数；综合权衡公式 |
| `internal/router/auto/llm.go` | 任务分类 verdict schema 加 `task` 字段 |
| `internal/router/auto/state.go` | 状态序列化新增 v2 字段 |
| `internal/model/types.go` | `TokenComboModel` 加 `CapabilityFallback` + `TaskModelBias` |
| `internal/api/router.go` | `handleAutoCombo` 提取 Needs → 决策 → 响应头 |
| `internal/api/admin.go` | state 端点扩展 v2 字段 |
| `internal/config/config.go` | `AutoRouterConfig` 加 v2 字段 |
| `config.yml` | 示例加 v2 段 |

### 9.3 测试

| 文件 | 覆盖 |
|---|---|
| `internal/router/auto/needs_test.go` | Needs 提取所有信号组合 |
| `internal/router/auto/capability_test.go` | 过滤逻辑 + 宽松策略 + 兜底 |
| `internal/router/auto/task_scorer_test.go` | 8 类关键词 + softmax + fallback |
| `internal/router/auto/bandit_v2_test.go` | v2 维度 bandit + v1 兼容回退 |
| `internal/router/auto/thompson_v2_test.go` | 综合权衡公式 |
| `internal/api/combo_v2_test.go` | 端到端：请求带 tools/JSON/image 走对模型 |

### 9.4 估算

总计 **~600 行**新代码 + 测试，**1 个 commit**。

---

## 10. 未来扩展（不在本次范围）

- **V2 增量**：中文任务关键词（基于真实流量统计）
- **V3 反馈环**：like/dislike 反馈影响 bandit（已在 [AUTO-ROUTING.md](AUTO-ROUTING.md) §V3 规划）
- **Capability filter 的 service-tier 区分**：某些模型部分参数支持（如 deepseek-chat 支持 JSON 但通过 prompt 不通过原生）—— 需要更细粒度的 capability schema
- **任务分类器的 embedding 升级**：LiteLLM semantic_keyword_matching / OpenRouter embedding classify
- **Per-tier 任务偏向**：combo 级别声明"complex tier 偏 code"，提升冷启动准确率

---

## 11. 业界对照表

| 维度 | LiteLLM Auto Router v2 | LiteLLM Adaptive Router | OpenRouter Auto Router | RouteLLM | Portkey Conditional | **llmRx v2（本设计）** |
|---|---|---|---|---|---|---|
| 分类器 | 启发式/LLM/keyword | regex 7 类 | in-flight ~30 类 | 训练(MF/BERT/ELO/causal) | 规则 | **启发式/LLM（8 类）** |
| 决策维度 | 复杂度 tier | (task × model) bandit | 任务消费份额 | 强/弱 winrate | 规则命中 | **(task × tier × model) bandit** |
| 能力感知 | ❌ | ❌ | ✅ 工具/输出约束 | ❌ | ✅ params 路由 | ✅ **modelmeta 硬过滤** |
| 成本/质量旋钮 | ❌ | weights(0.7/0.3) | cost_tier | threshold | ❌ | **quality_weight/cost_weight** |
| 多轮上下文 | ✅ classifier ctx | ❌ | ✅ session sticky | ❌ | ❌ | **✅ task_classifier.ctx (可选)** |
| 会话粘性 | ✅ session_affinity | ❌ | ✅ session sticky | ❌ | ✅ metadata | ✅ **v1 已实现** |
| 优雅降级 | ✅ | ✅ | ✅ default set | ❌ | ✅ default target | ✅ **v1 已实现** |
| 学习 | adaptive flag | bandit per cell | 市场被动学习 | 训练分类器 | 规则 | **Thompson bandit** |
| 可解释性 | ✅ cause= | ✅ quality/samples | ✅ metadata.task | ❌ | ✅ 规则链 | ✅ **cause= + Decision 字段** |
| 业界主要局限 | LLM judge 缺失 | 无延迟、regex 英文偏置 | 分类器 fast-fail 风险 | 偏好数据需求 | 非学习 | LLM judge 缺失（同 LiteLLM） |

---

## 12. 实施检查清单（开工前需确认）

- [ ] **中文任务关键词** —— V1 仅英文已确认；V2 增量补
- [ ] **多轮 context window** —— 默认 0 关闭已确认；agentic 场景手动开
- [ ] **能力过滤宽松策略** —— 未知能力模型保留已确认
- [ ] **v2 bandit 兼容** —— v1 bandit 作 general 兜底已确认
- [ ] **质量/成本默认权重** —— 0.7/0.3 已确认
- [ ] **响应头扩展** —— 6 个 X-llmRx-Auto-* 已确认

---

**git first doc first** —— 本文档先合入分支 `feat/smart-routing-gaps`（或新开 `feat/content-aware-routing`），再开工实施。