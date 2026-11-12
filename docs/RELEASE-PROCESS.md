# 发布流程（Release Process）

> 适用对象：维护者。目标是把「开发完成 → 可发布」的每一步固定下来，
> 避免遗漏测试、版本号、文档与推送顺序。
>
> 设计原则：**能自动化的不靠人记**；每个闸门都有可验证的退出码；
> 失败即停，不带病发布。

---

## 0. 流程总览

```
开发完成
   │
   ▼
① 变更盘点 ── 分类（feat/fix/refactor/breaking）
   │
   ▼
② 本地全量验证 ── vet + test + race + 覆盖率
   │
   ▼
③ 版本号决策 ── semver（major/minor/patch）
   │
   ▼
④ 文档同步 ── CHANGELOG / UPGRADE / 相关指南
   │
   ▼
⑤ 提交与打标 ── 单一语义化提交 + annotated tag
   │
   ▼
⑥ 推送 ── 分支 + 标签（探活重试）
   │
   ▼
⑦ 推送后验证 ── 干净 worktree 验证 + CI 结果
   │
   ▼
⑧ （可选）部署 ── 远程实例滚动
```

---

## 1. 阶段详解

### ① 变更盘点

```bash
git log --oneline $(git describe --tags --abbrev=0)..HEAD
git diff --stat $(git describe --tags --abbrev=0)..HEAD
```

按 conventional commits 归类本轮的 `feat` / `fix` / `refactor` / `docs` / `test`。
**任一 `!` 或 `BREAKING CHANGE` 即触发 minor/major 升级判断。**

### ② 本地全量验证（必须全绿才继续）

一条命令（脚本见 §4 `scripts/release-check.sh`）：

```bash
scripts/release-check.sh v0.3.1        # <version> 必填，--fast 可跳过测试闸门
```

内部执行并**捕获真实退出码**（不要用管道，`| head` 会掩盖失败）：

| 闸门 | 命令 | 门槛 |
|---|---|---|
| 静态检查 | `go vet ./...` | 退出码 0 |
| 单元测试 | `go test -count=1 ./...` | 0 FAIL |
| 竞态检测 | `go test -race -count=1 ./...` | 0 FAIL、0 DATA RACE |
| 覆盖率 | `make cover-check` | 包级地板 webui/store/api 各 ≥ 80% |
| 构建 | `go build ./...` | 退出码 0 |

> 历史教训：曾因 `go test ... | head -8` 掩盖失败而误判为通过。
> 脚本一律直接取退出码。

### ③ 版本号决策

遵循 [SemVer](https://semver.org/)：

| 变更类型 | 版本位 | 示例 |
|---|---|---|
| 破坏性变更（API/状态码/配置语义） | **minor**（0.x 阶段）/ major（1.0+） | v0.2.0 → v0.3.0 |
| 新增功能（向后兼容） | minor | v0.3.0 → v0.4.0 |
| 修复 / 文档 / 重构 | patch | v0.3.0 → v0.3.1 |

当前处于 0.x，破坏性变更按项目惯例升 **minor**（已在 CHANGELOG 声明）。

### ④ 文档同步清单

发布前逐项确认（打勾即完成）：

- [ ] `CHANGELOG.md`：把 `[Unreleased]` 定版为新版本小节，含
      `Added` / `Changed` / `Fixed` / **`Changed — ⚠️ BREAKING`**
- [ ] `docs/UPGRADE.md`：破坏性变更加**编号条目**（含检测 grep + 修复方法）
      与 `Versions Covered` 系列行
- [ ] `docs/api-reference.md` / `docs/ROUTES.md`：新增或变更的端点、状态码
- [ ] `docs/OPERATIONS.md`：新增配置项、运维步骤
- [ ] `config.yml`：新增配置键的注释样例
- [ ] `README.md`：能力清单 / 文档导航（若新增文档）
- [ ] `docs/architecture.html`：若包结构变化，`DUMP_ROUTES` + graphify 重测
- [ ] `docs/ACCOUNT-SECURITY.md` 等专题文档：若涉及安全行为变更

### ⑤ 提交与打标

```bash
# 单一语义化提交（把测试+文档+版本一起收口）
git add -A internal/ docs/ CHANGELOG.md README.md config.yml
git commit -m "chore(release): v0.X.Y

<摘要：本轮做了什么>

Breaking: <如有>
"

# annotated tag（不是轻量标签）
git tag -a v0.X.Y -m "llmRx v0.X.Y — <一句话主题>

BREAKING: <如有，写清迁移方向>"
```

### ⑥ 推送

```bash
git push origin master --follow-tags    # 分支 + 标签一起
# 或显式：git push origin master v0.X.Y
```

**网络抖动处理**：GitHub 443 偶发不可达时用 `scripts/push-retry.sh`
（§4）——`ls-remote` 探测成功才执行 push，最多 20 轮：

```bash
scripts/push-retry.sh master v0.3.1
# 或一次性推当前分支 + 所有未推标签：
scripts/push-retry.sh --all-tags
```

> 注意：不要用轻量标签，不要分多次推送分支和标签（中间态会让 CI 看到
> 无标签的 master）。

### ⑦ 推送后验证

```bash
# 干净 worktree 验证已推送的提交（不受工作区未提交改动影响）
git worktree add /tmp/release-verify v0.X.Y
cd /tmp/release-verify
go build ./... && go test -count=1 ./...
cd - && git worktree remove /tmp/release-verify
```

同时确认：

- `git rev-parse HEAD origin/master` 一致
- 标签指向的提交正确：`git rev-list -n1 v0.X.Y`
- GitHub Actions：`test.yml`（vet+race+覆盖率）、`release.yml`（tag 触发多平台
  产物 + ghcr 镜像）均绿

### ⑧ （可选）部署

仅当版本包含运行时变更时执行，见 `deploy/REMOTE-DEPLOY.md`：

```bash
# 用标签树构建镜像（版本串干净，不带 -dirty）
git worktree add /tmp/vX.Y.Z v0.X.Y
cd /tmp/vX.Y.Z && sh scripts/build-docker.sh llmrx:local
# 推送 + 滚动（保留数据与 master key）
```

部署后验证 `/health` 的 `version` 字段是否为新标签。

---

## 2. 闸门与责任人

| 闸门 | 通过的硬标准 | 谁执行 |
|---|---|---|
| 全量测试 | `release-check.sh` 退出码 0 | 提交人（本地） |
| 覆盖率 | ≥ 60% | 脚本 / CI |
| 版本号 | 与变更类型匹配（breaking→minor） | 提交人 |
| 文档 | 清单全勾 | 提交人 |
| CI | test.yml + release.yml 全绿 | 自动 |
| 远端一致 | HEAD == origin/master | 推送后脚本 |

---

## 3. 常见失败与处理

| 现象 | 原因 | 处理 |
|---|---|---|
| `go test` 有 FAIL | 真失败 | 修代码；禁止用 `\|\| true` 或管道掩盖 |
| 覆盖率 < 60% | 新代码缺测试 | 补测试或说明原因（CI 会拦）|
| 标签已存在 | 重复发布 | `git tag -d` 后重打；**已推送的标签不要强推**，改用 patch 版本 |
| push 超时 | GitHub 抖动 | `push-retry.sh` 探活重试 |
| 提交后才发现未暂存文件 | 忘了 `git add` | 补提交或 amend（**未推送前**）；已推送则新提交修复 |
| 工作区有他人 WIP | 多人/多任务并行 | 不碰对方文件；发布提交只 `git add` 自己改的路径 |

---

## 4. 配套脚本

| 脚本 | 作用 |
|---|---|
| `scripts/release-check.sh` | 全部闸门顺序执行，任一失败即停，输出真实退出码 |
| `scripts/push-retry.sh` | 探活 + 重试推送 |

### `scripts/release-check.sh <version>`

在**打标签之前**运行。失败即停，每项都打印 OK/FAIL：

```bash
scripts/release-check.sh v0.3.1            # 全套闸门
scripts/release-check.sh v0.3.1 --fast     # 只跑静态检查（迭代时用）
# 或：make release-check V=0.3.1
```

检查项：

| # | 检查 | 失败含义 |
|---|---|---|
| 1 | 工作区干净 | 有未提交改动（`-w --ignore-blank-lines` 排除 CRLF 伪影）|
| 2 | tag 未被占用 | 版本号重复 |
| 3 | CHANGELOG 已定版 | 仍是 `[Unreleased]` |
| 4 | go.mod 版本 ∈ CI matrix | 本地/CI Go 版本不一致 |
| 5 | `go vet` | 静态问题 |
| 6 | `go test -count=1` | 单测失败 |
| 7 | `go test -race -count=1` | 数据竞争 |
| 8 | `make cover-check` | 覆盖率低于包级地板（webui/store/api 各 80%）|
| 9 | `go build ./...` | 编译失败 |

`--fast` 跳过 5–9（对应 CI 已覆盖，适合高频迭代）。

### `scripts/push-retry.sh [--all-tags] [targets...]`

推送前先用 `ls-remote` 探活，成功才 push；失败按轮次重试。

```bash
scripts/push-retry.sh                       # 推当前分支
scripts/push-retry.sh master v0.3.1         # 推分支 + 标签
scripts/push-retry.sh --all-tags            # 推当前分支 + 所有本地未推标签
# 或：make push-retry

REMOTE=origin ROUNDS=20 WAIT_SEC=20 scripts/push-retry.sh   # 可调参数
```

`--all-tags` 会在 `--follow-tags` 之后逐个补齐未被远端引用的标签
（`--follow-tags` 只推「从被推提交可达的 annotated tag」）。

---

## 5. 与现有 CI 的关系

本流程是 **推送前** 的本地闸门；CI 是 **推送后** 的复核：

```
本地 release-check.sh  ──►  push  ──►  CI test.yml（vet+race+覆盖率）
                                         CI release.yml（tag → 产物/镜像）
```

两者门槛一致（覆盖率 60%），本地通过通常 CI 也通过；CI 额外覆盖
多平台构建（linux/darwin/windows）与容器打包。
