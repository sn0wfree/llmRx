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

### 2.1 价格（OpenRouter 实时）

| 模型 | in/1M | out/1M | 相对 M3 |
|---|---|---|---|
| deepseek-v4-flash | $0.07 | **$0.14** | 1/9 |
| mimo-v2.5 | $0.14 | $0.28 | 1/4 |
| mimo-v2.5-pro | $0.44 | $0.87 | 1/1.4 |
| MiniMax-M3 | $0.30 | $1.20 | 1 |
| deepseek-v4-pro | $1.17 | $2.34 | 2x |

### 2.2 性价比指数（质量/输出价格，1M tokens）

```
14.1  deepseek-v4-flash   ← 碾压级性价比
 7.4  mimo-v2.5
 4.8  mimo-v2.5-pro
 3.8  MiniMax-M3
 1.9  deepseek-v4-pro
```

### 2.3 本地 judge 实测（41 样本候选模型平均分）

```
mimo-v2.5         2.07
deepseek-v4-flash 1.93
MiniMax-M3        1.70
```

结合任务偏好（41 样本校准）：math/deploy→MiniMax-M3、visual/writing→mimo-v2.5——**任务最优 ≠ 全局最优**。

## 3. 策略建议

### 3.1 分层路由（性价比驱动）

```
日常/简单（workhorse）:
  simple/standard tier → mimo-v2.5 / deepseek-v4-flash
  （mimo 实测质量略高、flash 绝对便宜——按任务再分）

高难度（premium）:
  complex/agentic tier → MiniMax-M3（math/deploy 偏好、公开分高）
  或 mimo-v2.5-pro（1T 参数，agent 场景强）
```

### 3.2 Orchestrator-Worker 建议

```
Planner（规划/拆解）: MiniMax-M3 或 mimo-v2.5-pro（强推理、公开分 4.5/4.2）
Worker（执行）:       mimo-v2.5 / deepseek-v4-flash（性价比前二）

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
