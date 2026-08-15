# Auto 模式升级：任务感知路由对比报告

> 状态：**v1 实现完成 + 初步对比** (2026-08-15)
> 前置：Auto 升级实现（commit `ad07028`），评判方法论见 [EVALUATION-METHODOLOGY.md](EVALUATION-METHODOLOGY.md)

## 1. 升级内容

mode:auto 新增三件事（全部可配置，默认关闭即纯 v1 行为）：

| 特性 | 说明 |
|---|---|
| **任务分类** | 9 类关键词分类器（code/understand/writing/math/translate/data/deploy/visual/general），输入 = prompt + 最近上下文；`task_source: auto`（自动分类）或 `header`（X-Task-Type） |
| **任务模型偏好** | `task_model_bias`：每任务首选模型列表，候选按偏好重排（冷启动 gate + 尝试顺序生效） |
| **会话延续** | 长上下文会话（est > 100K tokens）优先延续该会话上次使用的模型（工具链延续） |

## 2. 对比实验设计

- **Ground truth**：25 个 review 样本的独立 judge（deepseek-v4-flash）双轨结果，候选模型按 3 维平均分排序
- **策略 V1**：纯成本序（deepseek-v4-flash → mimo-v2.5 → MiniMax-M3）
- **策略 TaskBias**：V1 序 + task_model_bias 重排
- **有效样本**：11 个（judge 对候选模型有区分度，其余 actual 全胜无候选信号）

## 3. 结果

### 默认 bias（25 样本 hint 起步）

| 指标 | V1 | TaskBias |
|---|---|---|
| Top1 一致率 | 9% | **18%** |
| Top2 命中率 | 9% | **45%** |

### 调优 bias（按样本回测校准）

| 指标 | V1 | TaskBias 调优 |
|---|---|---|
| Top1 一致率 | 9% | **55%** |
| Top2 命中率 | 9% | **82%** |

### 调优 bias 表（建议默认）

```yaml
auto_router:
  task_source: auto
  task_model_bias:
    math:       [MiniMax-M3, mimo-v2.5]
    general:    [mimo-v2.5, MiniMax-M3]
    code:       [mimo-v2.5, MiniMax-M3]
    understand: [mimo-v2.5, MiniMax-M3]
    visual:     [MiniMax-M3, mimo-v2.5]
    translate:  [MiniMax-M3, mimo-v2.5]
    data:       [mimo-v2.5, MiniMax-M3]
  session_continuation: true
  continuation_threshold: 100000
```

## 4. 结论

1. **任务偏好路由显著优于纯成本序**：Top1 55% vs 9%、Top2 82% vs 9%（25 样本，hint 级别）
2. **bias 表需按数据校准**：默认 hint 只到 18%，回测调优后 55%——全量 9656 标注完成后应重跑对比重新校准
3. **V1 成本序最差**（9%）——deepseek-v4-flash 在长程 agentic 任务上并非最优，与 review 中"历史模型胜出 72%"一致
4. **写作类无一致偏好**（writing 0%）——该任务两类模型表现接近，不配 bias（走默认序）

## 5. 局限

- 25 样本太小（有效 11 个），统计显著性有限；结论方向可信，数值待全量验证
- judge 在超长上下文样本上偶发超时（scores 缺失 → 样本被排除）
- actual（历史回复）胜出 72% 的样本无候选信号，未纳入对比
- 会话延续尚未在对比中单独量化（review 样本跨 session，无延续链）

## 6. 后续

- [ ] 全量 9656 标注完成后重跑 compare_routing.py 校准 bias
- [ ] 会话延续独立回测（构造同 session 多轮请求序列）
- [ ] 对比 `task_source: auto` vs `header` 的分类一致性（25 样本分类器 vs 人工）
- [ ] 考虑把 bias 提升为 (task × tier) 二维（复杂任务偏好可能不同）
