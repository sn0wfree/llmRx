# Git 历史同步记录

## 背景

本地 `master` 与 `origin/master` 历史完全无关（无 merge base）：

| | 提交数 | 内容 |
|---|---|---|
| 本地旧线 | 8 | Go 网关骨架 + `Rewrite: pipeservice → llmRx` |
| 远端 | 242 | 完整 llmRx（billing / webui / cluster / helm / tests / CI） |

## 处理

以远端为准对齐：

1. 本地旧线保留为分支 `local-rewrite`（`39c1dca`）
2. `master` reset 到 `origin/master`（`b047551`）

## 查看本地重写

```bash
git log --oneline local-rewrite
git diff master local-rewrite
```

如确认不再需要，可删除：`git branch -D local-rewrite`
