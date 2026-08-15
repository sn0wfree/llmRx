# 成本-价值策略：模型测评分数驱动的路由分层

> 状态：**调研完成 + 分析工具就绪** (2026-08-15)
> 数据源：OpenRouter 实时价格（已验证可拉取）、公开 benchmark（手动维护）、本地 judge 实测（41 样本）

## 1. 业界做法调研

### 1.1 模型测评分数来源

| 来源 | 内容 | 获取方式 |
|---|---|---|
| **LMArena / Chatbot Arena** | 众包 Elo 排名（人类偏好） | 网页/API（反爬，需定期手动导出） |
| **OpenRouter Rankings** | 任务维度消费份额 + benchmark | API 反爬；`/api/v1/models` 可拉价格 |
| **SuperCLUE**（中文） | 中文能力榜单 | 网页 |
| **官方发布** | MiniMax-M3 BrowseComp 83.5、mimo-v2.5-pro 宣称等 | 新闻稿 |
| **LiteLLM catalog** | 价格/上下文/能力（项目已内置） | `scripts/fetch-models.sh` ✅ |

### 1.2 性价比分层策略（业界）

| 方案 | 做法 | 参照 |
|---|---|---|
| **成本带分层** | low/medium/high/xhigh/max 按价格带选模型 | OpenRouter cost_tier |
| **阈值校准** | 用偏好数据校准"多少比例请求走强模型" | RouteLLM threshold |
| **质量/成本权重** | bandit 按 quality/cost 权重选模型 | LiteLLM adaptive router |
| **能力硬过滤** | 请求需要的能力（工具/视觉/JSON）剔除不支持模型 | OpenRouter / 本项目 modelmeta |

### 1.3 Orchestrator-Worker（规划者-执行者）做法

| 方案 | 架构 | 参照 |
|---|---|---|
| **plan/build 双模式** | 强模型出方案，执行模式跑工具链 | Claude Code / **opencode（本项目数据就有 agent: plan/build）** |
| **角色链** | 产品经理→架构师→工程师→QA 流水线 | MetaGPT |
| **多 agent 对话** | 规划者与执行者多轮协作 | AutoGen / CrewAI |
| **plan-execute** | 先规划再执行，拆解子任务 | Qwen-Coder 系列 |

**共同模式**：强（贵）模型负责**高认知负载**（拆解、方案、评审），廉价模型负责**高吞吐执行**（编码、检索、格式化）。

## 2. 我们的分析结果（2026-08-15 实测）

### 2.1 价格 + 测评指数（OpenRouter 实时 API + Artificial Analysis 指数）

| 模型 | in/1M | out/1M | intel | coding | agentic |
|---|---|---|---|---|---|
| deepseek-v4-flash | $0.07 | **$0.14** | **51.8** | **69.1** | **48.4** |
| mimo-v2.5 | $0.14 | $0.28 | 38.0 | 56.8 | 24.4 |
| mimo-v2.5-pro | $0.44 | $0.87 | 42.9 | 60.2 | 29.5 |
| MiniMax-M3 | $0.30 | $1.20 | 45.4 | 58.6 | 36.1 |
| glm-5.2 | $0.49 | $1.54 | 52.6 | 68.8 | 45.7 |
| deepseek-v4-pro | $1.17 | $2.34 | 53.2 | 68.8 | 49.6 |

### 2.2 性价比指数（质量/输出价格，1M tokens）

```
14.1  deepseek-v4-flash   ← 碾压级性价比（intel 51.8 ≈ pro 的 53.2，价格 1/17）
 7.4  mimo-v2.5
 2.5  mimo-v2.5-pro
 1.9  MiniMax-M3
 1.1  deepseek-v4-pro
```

### 2.3 本地 judge 实测（41 样本候选模型平均分）

```
mimo-v2.5         2.07
deepseek-v4-flash 1.93
MiniMax-M3        1.70
```

**关键洞察**：
- **deepseek-v4-flash 是性价比之王**：intel/coding/agentic 全部接近旗舰（51.8/69.1/48.4），价格只有 M3 的 1/9、pro 的 1/17
- **glm-5.2 被低估**：intel 52.6 第三高（手动录入时只给了 4.0）——真实指数高于 M3
- **MiniMax-M3 实际指数低于手动估值**：intel 45.4（手动填 4.5 偏高），但 agentic 36.1 中等偏上
- **mimo-v2.5 的 judge 实测 > benchmark 指数**：agentic 短指令+长上下文场景（我们的流量）mimo 表现优于其通用指数——**本地实测与公开 benchmark 存在场景差异，两者互补**

结合任务偏好（41 样本校准）：math/deploy→MiniMax-M3、visual/writing→mimo-v2.5——**任务最优 ≠ 全局最优 ≠ 性价比最优**，三层叠加使用。

## 3. 策略建议

### 3.1 分层路由（性价比驱动，2026-08-15 数据）

```
日常/简单（workhorse）:
  simple/standard tier → deepseek-v4-flash（性价比 14.1，intel 51.8）
  / mimo-v2.5（judge 实测最佳 2.07，按任务再分）

高难度（premium）:
  complex/agentic tier → glm-5.2（intel 52.6 / coding 68.8 / agentic 45.7）
  或 deepseek-v4-pro（intel 53.2 最高）
  或 MiniMax-M3（math/deploy 任务偏好）
```

### 3.2 Orchestrator-Worker 建议（2026-08-15 数据更新）

```
Planner（规划/拆解）: deepseek-v4-pro 或 glm-5.2
  （intel 53.2/52.6、coding 68.8、agentic 49.6/45.7 全面领先）
Worker（执行）:       deepseek-v4-flash / mimo-v2.5（性价比前二）

流程:
  高复杂度请求 → planner 出方案 + 任务拆解
  → 子任务按 task_model_bias / 复杂度分发给 worker 执行
  → 结果汇总（可由 planner 评审）

llmRx 落地点: combo 配置增加 plan_model 字段（agentic tier 用 planner），
  执行层沿用 tier 表（worker 模型）。客户端（opencode）已原生支持
  plan/build 双模式——网关侧只需把 plan 阶段的请求路由到 planner 模型。
```

### 3.3 落地清单

| 项 | 状态 |
|---|---|
| `fetch_benchmarks.py`：拉 OpenRouter 价格 + 合并手动分数 | ✅ 已实现 |
| `analyze_value.py`：性价比分析 + 策略建议 | ✅ 已实现 |
| `data/learned-router/benchmarks.json`：公开分数（待校准） | ✅ 已建 |
| config 支持 `plan_model`（orchestrator-worker 落点） | 待实施 |
| tier 表按性价比校准（workhorse/premium 分层） | 待实施 |

## 4. 使用方式

```bash
# 更新价格 + 分数 → 重新分析
python scripts/learned-router/fetch_benchmarks.py
python scripts/learned-router/analyze_value.py
```

## 5. 局限

- 公开分数为手动录入（LMArena 反爬，无法全自动），标注"待校准"
- judge 实测仅 41 样本（候选模型，不含 actual）——随标注扩大更新
- 性价比指数是粗粒度（未区分输入/输出占比、缓存命中率）
- orchestrator-worker 需客户端配合（llmRx 是网关，不跑 agent 循环）
