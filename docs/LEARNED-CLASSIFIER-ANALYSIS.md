# 分类模型构建：现有样本分析报告

> 状态：**分析完成 + 原型实验** (2026-08-15)
> 数据：49 个 review 样本 judge 标注（41 个有候选模型信号）
> 目标：评估"能否用现有标注训练模型选择最佳模型"

## 1. 训练数据

| 项 | 值 |
|---|---|
| 有信号样本 | 41 |
| 标签分布 | mimo-v2.5: 22 (54%) / MiniMax-M3: 14 (34%) / deepseek-v4-flash: 5 (12%) |
| 特征 | task_category + 上下文长度 + 工具调用数 + 语言（20 维结构化） |
| 评估 | Leave-One-Out CV |

## 2. 特征区分度分析

| 特征 | 区分度 | 证据 |
|---|---|---|
| **task_category** | ✅ 最强 | deploy→MiniMax(3:1)、visual→mimo(4:1)、writing→mimo(5:1)、math→MiniMax(2:1) |
| 上下文长度 | ❌ 弱 | 各长度桶标签分散 |
| 工具调用数 | ❌ 弱 | 同上 |
| 语言 | ❌ 无 | cn 主导（78%），无区分 |
| prompt 文本（TF-IDF） | ❌ 无 | 短指令（"继续"），TF-IDF 仅 15 维 |

## 3. 模型实验（LOOCV 准确率）

| 方法 | 准确率 | 说明 |
|---|---|---|
| **校准规则**（每任务多数标签） | **61.0%** | 25/41，最佳 |
| majority（总是 mimo-v2.5） | 53.7% | 标签偏斜的天然基线 |
| 任务规则（25 样本旧调优） | 36.6% | 旧 bias 表 |
| LogisticRegression（结构化特征） | 31.7% | 数据量不足 |
| LightGBM（TF-IDF+结构） | 31.7% | 同上 |

## 4. 校准 bias 表（41 样本，建议默认）

```yaml
auto_router:
  task_source: auto
  task_model_bias:
    code:       [mimo-v2.5, MiniMax-M3]
    data:       [mimo-v2.5, MiniMax-M3]
    deploy:     [MiniMax-M3, mimo-v2.5]
    general:    [mimo-v2.5, MiniMax-M3]
    math:       [MiniMax-M3, mimo-v2.5]
    understand: [MiniMax-M3, mimo-v2.5]
    visual:     [mimo-v2.5, MiniMax-M3]
    writing:    [mimo-v2.5, deepseek-v4-flash]
    translate:  [mimo-v2.5, MiniMax-M3]
```

与旧表的差异：**deploy/understand 翻转为 MiniMax-M3 优先**、writing 次选改 deepseek、visual 翻转为 mimo 优先。

## 5. 结论

1. **41 样本不足以训练 ML 分类器**：LR/LGBM（32%）被 majority（54%）打败——特征少、样本少、标签偏斜
2. **规则（每任务多数标签）在当前规模下最优**：61%，且可解释、可配置
3. **最优策略演进**：V1 成本序 12% → 旧规则 37% → **校准规则 61%**（41 样本 top1）
4. **下一步路径**：
   - a) 扩大标注（全量 9656 或每任务更多样本）→ ML 模型才有数据基础
   - b) 用校准规则作为正式默认（当前最优）
   - c) 后续标注增加后用 ML 替换规则（TF-IDF 会在更多数据下有用）

## 6. 局限

- 校准规则在 41 样本上拟合评估，存在乐观偏差（同分布评估）
- 标签偏斜（mimo 54%）——deepseek 只有 5 个样本，任何模型都难学其模式
- actual 胜出样本（无候选信号）未计入——真实部署中"会话延续"可能主导

## 7. 脚本

```
scripts/learned-router/prepare_training.py   # 标注 → 训练集
scripts/learned-router/train_structured.py   # 结构化特征 + 基线对比
scripts/learned-router/train_router.py       # TF-IDF + LGBM（--loocv 小数据评估）
```
