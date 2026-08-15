# LLM 评估方法论（Judge 方案）

> 状态：**已定稿** (2026-08-15)
> 背景：训练式路由器需要给样本标注"哪个模型最优"（LLM-as-judge）。
> 调研依据：MT-Bench 论文 (arXiv:2306.05685)、AlpacaEval/AlpacaFarm (arXiv:2305.14387)、Chatbot Arena。

## 1. 业界结论（调研摘要）

### 1.1 LLM-as-a-Judge 可行性与偏差

强 LLM judge（GPT-4 级）与人类偏好一致率 **>80%**（与人类评委间一致率同级）。

**四大已知偏差**：

| 偏差 | 现象 | 缓解 |
|---|---|---|
| Position bias | 偏好第一个答案 | position swap（A/B 交换判两次） |
| Verbosity bias | 偏好更长回答 | 长度控制（LC）、rubric 强调简洁 |
| Self-enhancement bias | 偏好自己的输出 | 独立 judge（不参与候选） |
| Limited reasoning | 数学/推理题不准 | CoT judge（先推理后评分） |

### 1.2 评分方式

- **Pointwise（单答评分）**：1-10 打分，MT-Bench 排行即用此，与 pairwise 高度一致，便宜、无位置偏差
- **Pairwise（对比投票）**：A/B 对比选优，更精细但贵
- **双轨融合**：两者一致时高置信；不一致标记低置信

### 1.3 路由场景参照

- RouteLLM：GPT-4 judge 标注偏好数据 → 训练路由模型
- LiteLLM adaptive：正则满意度信号（粗糙）

## 2. 定稿方案（llmRx 训练式路由器）

### 2.1 判定流程（每样本）

```
候选模型（排除 judge）回复完整上下文（并发）
        ↓
① 评分调用：judge 对每个候选（含 actual）输出 1-5 × 3 维度
   （正确性 / 完整性 / 上下文契合）→ 加权平均分 → score_rank
        ↓
② pairwise 调用：judge 对每对候选输出 winner → 投票 → vote_rank
        ↓
③ 双轨融合：
   best_score == best_vote → best（高置信）
   不一致                  → best="conflict"（低置信，留人工/跳过）
        ↓
④ 指标：consistent（best==model_used）、history_wins（best==actual）
```

### 2.2 关键决策

| # | 决策 | 定稿 |
|---|---|---|
| 1 | **独立 judge** | `deepseek-v4-flash`（不参与候选）——消除自我偏好 |
| 2 | **评分制 + 投票制双轨** | 一致才确定 best，冲突标记 low_confidence |
| 3 | **actual 对等化** | 合并样本后到下一 user 消息前的全部 assistant 文本（缓解 verbosity 偏差中"实际回复过短"） |
| 4 | **CoT judge** | judge 先给理由再输出 JSON |
| 5 | **rubric 强化** | 明确"简洁加分、冗长不加分"（LC 思路 prompt 化） |
| 6 | **position swap** | 默认关闭（成本 ×2），仅小规模校准样本启用（`--swap`） |
| 7 | **交叉模式保留** | 旧 cross-judge 作为 `--mode cross` 兼容保留 |

### 2.3 成本估算（9656 样本 × 3 候选 + 1 judge）

| 调用 | 每样本 | 说明 |
|---|---|---|
| 候选回复 | 3 | 并发 |
| 评分 | 1 | 含 actual 4 个候选一起评 |
| pairwise | C(4,2)=6 | 4 候选两两对比 |
| **合计** | **10 次/样本** | ≈ 9.7 万次调用（不带 swap） |

## 3. 输出格式

```json
{
  "prompt": "...",
  "best_model": "mimo-v2.5",          // "conflict" 表示双轨不一致
  "best_score": "mimo-v2.5",          // 评分轨最优
  "best_vote": "MiniMax-M3",          // 投票轨最优
  "model_used": "MiniMax-M3",
  "consistent": false,
  "history_wins": false,
  "confidence": "high" | "low",       // 双轨一致=high
  "scores": {"mimo-v2.5": {"correctness": 4, "completeness": 5, "contextual_fit": 4}},
  "votes": {"mimo-v2.5": 2, "MiniMax-M3": 1},
  "judge_model": "deepseek-v4-flash",
  "candidate_count": 4
}
```

## 4. 校准流程（position swap 用途）

1. 抽 100-200 样本跑 `--swap`（每对判两次）
2. 计算 swap 一致性率（judge 对 A/B 和 B/A 判断一致的比例）
3. 一致性 < 70% → 加强 rubric / 换 judge；> 85% → 全量可不开 swap
4. 记录校准报告（写入 eval 报告）

## 5. 遗留事项

- [ ] 独立 judge 若 flash 表现不稳（数学/推理），换 MiniMax-M3 重校准
- [ ] 双轨冲突率（conflict 占比）> 20% 时需复盘 rubric
- [ ] actual 对等化后 verbosity 偏差残余：可在汇总时统计 actual 长度与胜率相关性
