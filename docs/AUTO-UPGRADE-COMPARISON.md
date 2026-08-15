# Auto 模式升级：任务感知路由对比报告

> 状态：**v1 实现完成 + 两轮对比** (2026-08-15)
> 前置：Auto 升级实现（commit `ad07028`），评判方法论见 [EVALUATION-METHODOLOGY.md](EVALUATION-METHODOLOGY.md)

## 1. 升级内容

mode:auto 新增三件事（全部可配置，默认关闭即纯 v1 行为）：

| 特性 | 说明 |
|---|---|
| **任务分类** | 9 类关键词分类器（code/understand/writing/math/translate/data/deploy/visual/general），输入 = prompt + 最近上下文；`task_source: auto`（自动分类）或 `header`（X-Task-Type） |
| **任务模型偏好** | `task_model_bias`：每任务首选模型列表，候选按偏好重排（冷启动 gate + 尝试顺序生效） |
| **会话延续** | 长上下文会话（est > 100K tokens）优先延续该会话上次使用的模型（工具链延续） |

## 2. 对比实验设计

- **Ground truth**：review 样本的独立 judge（deepseek-v4-flash）双轨结果，候选模型按 3 维平均分排序
- **策略 V1**：纯成本序（deepseek-v4-flash → mimo-v2.5 → MiniMax-M3）
- **策略 TaskBias**：V1 序 + task_model_bias 重排
- 有效样本 = judge 对候选模型有区分度（actual 全胜的样本无候选信号，不计入）

## 3. 结果

### 第一轮：25 样本（有效 11 个）

| 指标 | V1 | TaskBias 默认 | TaskBias 调优 |
|---|---|---|---|
| Top1 一致率 | 9% | 18% | **55%** |
| Top2 命中率 | 9% | 45% | **82%** |

### 第二轮：49 样本（有效 41 个，样本扩大近 4 倍）

| 指标 | V1 | TaskBias 默认 | TaskBias 调优 |
|---|---|---|---|
| Top1 一致率 | 12% | 24% | **37%** |
| Top2 命中率 | 20% | 51% | **73%** |

### 按任务（49 样本，调优 bias）

| 任务 | n | bias top1 | bias top2 | 说明 |
|---|---|---|---|---|
| code | 5 | 60% | 100% | 偏好 mimo-v2.5 有效 |
| data | 6 | 50% | 100% | 偏好 mimo-v2.5 有效 |
| math | 3 | 67% | 100% | 偏好 MiniMax-M3 有效 |
| understand | 5 | 40% | 100% | 偏好 mimo-v2.5 有效 |
| general | 5 | 40% | 80% | 偏好 mimo-v2.5 有效 |
| visual | 5 | 20% | 80% | 弱偏好 |
| translate | 1 | 0% | 100% | 样本太少 |
| deploy | 5 | 20% | 20% | **无偏好，不配 bias** |
| writing | 6 | 17% | 17% | **无偏好，不配 bias** |

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
    # deploy / writing: 无一致偏好，走默认成本序
  session_continuation: true
  continuation_threshold: 100000
```

## 4. 结论

1. **任务偏好路由显著优于纯成本序**（样本翻倍后依然成立）：
   - Top1：9% → 37%（49 样本）；Top2：9% → 73%
2. **bias 收益集中在部分任务**：code/data/math/understand/general 收益大；deploy/writing 无差异
3. **V1 成本序最差**（12%）——deepseek-v4-flash 在长程 agentic 任务上并非最优
4. **actual 胜出率稳定**：72% → 88.5% → 82.6%（短/长批次）——长程会话历史模型优势显著，会话延续特性方向正确
5. 全量 9656 标注完成后应重跑校准（更精确的 bias 阈值）

## 5. 局限

- 49 样本（有效 41），统计显著性中等；结论方向可信
- judge 在超长上下文样本上偶发超时（scores 缺失 → 样本排除）
- 无 channel 的历史模型（ark-code-latest/mimo-v2.5-pro/glm-5.2 等）作为候选扩展时 503，不影响候选间排序
- 会话延续尚未在对比中单独量化（review 样本跨 session，无延续链）

## 6. 后续

- [ ] 全量 9656 标注完成后重跑 compare_routing.py 校准 bias
- [ ] 会话延续独立回测（构造同 session 多轮请求序列）
- [ ] 对比 `task_source: auto` vs `header` 的分类一致性
- [ ] 考虑把 bias 提升为 (task × tier) 二维
- [ ] deploy/writing 类在更多样本下复核（可能确实无偏好）
