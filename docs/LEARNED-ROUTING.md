# 训练式路由器（Learned Router）

> 状态：**阶段一实施中**（Go 推理侧基础设施）
> 前置：v1 智能路由已完成（见 [AUTO-ROUTING.md](AUTO-ROUTING.md)）+ 4 个业界差距已补齐（见 [SMART-ROUTING-GAPS.md](SMART-ROUTING-GAPS.md)）+ 内容感知路由设计（见 [CONTENT-AWARE-ROUTING.md](CONTENT-AWARE-ROUTING.md)）
> 数据源：opencode SQLite 聊天记录导出

## 0. 已锁定决策

| # | 决策 | 定稿 |
|---|---|---|
| 1 | 标签方案 | LLM-as-judge 离线标注为主 + 隐式信号为辅助特征 |
| 2 | 训练技术栈 | Python（LightGBM）→ ONNX 导出 |
| 3 | 数据源 | opencode SQLite 导出（session + message + part） |
| 4 | 在线融合 | 训练模型作先验，与 TS bandit 加权 |
| 5 | 评估验证 | 多 LLM 评估：核心案例集 → 多模型对比 |
| 6 | 推理方式 | Go 侧 ONNX 加载（或补全 Rust dlopen） |

## 1. 分阶段实施

### 阶段一：Go 推理侧基础设施（当前）

目标：搭好推理侧基础设施，先能跑通，用 mock 模型测试。

**改动文件**：

| 文件 | 内容 | 行数 |
|---|---|---|
| `internal/router/auto/learned.go` | 推理接口 + ONNX 加载 + 特征向量化 | ~200 |
| `internal/router/auto/learned_test.go` | 推理正确性 + 回退测试 | ~100 |
| `internal/router/auto/thompson.go` | 融合公式修改 | ~50 |
| `internal/model/types.go` | `TokenComboModel` 加配置字段 | ~20 |
| `internal/config/config.go` | 配置解析 | ~30 |
| `internal/admin/handler.go` | state 端点扩展 | ~50 |
| `cmd/gateway/main.go` | 启动时加载 | ~30 |

**配置**：

```yaml
learned_router:
  enabled: false
  model_path: data/learned-router/router_model.onnx
  vocab_path: data/learned-router/vocab.json
  feature_stats_path: data/learned-router/feature_stats.json
  weights:
    ts: 0.4
    learned: 0.4
    cost: 0.2
  fallback: true
```

**接口**：

```go
type LearnedRouter interface {
    Infer(prompt string, candidates []string, needs Needs) map[string]float64
    Reload() error
    Loaded() bool
}
```

### 阶段二：训练代码 + 执行

目标：用 opencode 数据训练出 `router_model.onnx`。

| 文件 | 内容 | 行数 |
|---|---|---|
| `scripts/learned-router/export_samples.py` | opencode.db → samples.jsonl | ~120 |
| `scripts/learned-router/judge_samples.py` | 多 LLM 并行标注 | ~150 |
| `scripts/learned-router/train_router.py` | LightGBM + ONNX 导出 | ~200 |
| `scripts/learned-router/evaluate_router.py` | 核心案例 + 多 LLM 评估 | ~150 |
| `scripts/learned-router/requirements.txt` | 依赖 | ~10 |
| `scripts/learned-router/config.yaml` | 训练配置 | ~30 |

## 2. 融合公式

```go
final_score(model) = w_ts × TS_sample + w_learned × learned_score + w_cost × cost_normalized
```

默认权重：`w_ts=0.4, w_learned=0.4, w_cost=0.2`

- 冷启动 / 模型不可用 → 回退启发式（`cause=learned_fallback`）
- 候选模型不在训练集 → learned_score = 0.5（中性先验）

## 3. 里程碑

| 阶段 | 内容 | 天数 |
|---|---|---|
| 阶段一 | Go 推理侧（learned.go + thompson + 配置 + 测试） | 2 |
| 阶段二 | Python 训练（导出 + 标注 + 训练 + 评估） | 3 |
| 验证 | 训练 → 生成模型 → 在线推理验证 | 1 |

## 4. opencode 数据（已验证）

```
session (340) → message (178,533) → part (628,184)
10 providers, 11 models
MiniMax-M3 47%, mimo-v2.5 22%, deepseek-v4-flash 14%, ark-code 8%
```

## 5. 总估算

| 部分 | 行数 |
|---|---|
| Go 推理侧 | ~510 |
| Python 训练 | ~660 |
| 测试 | ~200 |
| **总计** | **~1,370** |

---

**git first doc first** —— 本文档合入后开始阶段一实施。
