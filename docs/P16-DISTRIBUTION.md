# P16 — 交付与发布 / Distribution & Release

> Date: 2026-11-01 · Owner: llmRx maintainers · Status: **R1-R8 已实施**
> 优先级:**高于 P15**。没有版本化和发布骨架,后面所有分发形态都无从谈起。
> 关联:[P15-CLIENT.md](P15-CLIENT.md)(客户端形态)、[OPERATIONS.md](OPERATIONS.md)(部署运维)。

## 1. 现状盘点(逐项核实)

| 交付形态 | 现状 | 证据 |
|---|---|---|
| Docker 镜像 → ghcr.io | ✅ CI 自动构建推送 | `.github/workflows/docker.yml` |
| Docker 镜像多架构 | ⚠️ **仅 amd64** | 同上,`platforms: linux/amd64`,注释说明 arm64 需 cross gcc |
| Helm chart | ⚠️ 存在,但版本号是占位 | `deploy/helm/llmrx/Chart.yaml`:`version: 0.1.0`、`appVersion: "latest"` |
| 裸机构建 | ⚠️ 需在仓库内 `go build` | `Makefile` `build:` 目标、`start.sh` `ensure_binary()` |
| 预编译二进制发布 | ❌ **没有** | `git tag -l` 为空,无 goreleaser,无 release 产物 |
| 原生包(deb/rpm/brew) | ❌ 没有 | — |
| 版本号(`-version`) | ❌ **没有** | `cmd/gateway/main.go` 的 flag 只有 `-config` / `-healthcheck` / `-wipe-keys` / `-pprof-addr` |
| 语义化版本 tag | ❌ 从来没有打过 | `git tag -l` 输出为空 |

### 1.1 另外发现的三个不一致

1. **go 版本**：`go.mod` 写 `go 1.18`,CI 用 `go-version: '1.22'`。1.18 早已 EOL,
   两者应统一(建议统一到 CI 的 1.22)。
2. **Dockerfile 依赖 BuildKit**：使用了 `COPY <<EOF ... EOF` heredoc 语法
   (`Dockerfile` L27-34),老版本 docker / buildah 会失败。需要在文档里
   写明最低 Docker 版本(20.10+/BuildKit 必需)。
3. **Helm chart 无 schema 校验**：`values.yaml` 里注释说 channels/tokens
   不走 chart,但没有任何校验阻止用户误填;`Chart.yaml` 的 `version`
   与镜像 tag 完全脱钩。

### 1.2 结论

项目目前**只有"开发者能在仓库里跑",没有"用户能装"**。补齐的最小集是:
版本号 → 打 tag → 自动出多平台产物 → 文档给出各形态的安装命令。

## 2. 四种交付形态

按"谁用、怎么用"分四层,每层独立可交付:

| # | 形态 | 目标用户 | 安装方式 | 现状 |
|---|---|---|---|---|
| ① | **Docker 镜像** | 自托管运维 | `docker run ghcr.io/sn0wfree/llmrx:vX.Y.Z` | ✅ 缺 arm64 + 版本 tag |
| ② | **单文件二进制** | 裸机 / 内网 / 离线环境 | 下载 release 压缩包,`./llmrx -config config.yml` | ❌ 完全空白 |
| ③ | **Helm chart** | K8s 集群 | `helm install llmrx ./deploy/helm/llmrx` | ⚠️ 版本占位 |
| ④ | **客户端边缘代理** | 本机 / 边缘机房 | 下载 `llmrx-client` 或 `docker run` | ❌ P15 规划中 |

本文件覆盖 ①②③(发布骨架);④ 的发布形态依赖 §5 的 D2 决策。

## 3. 目标与非目标

### 3.1 目标

1. 一次 `git tag v1.2.3 && git push --tags` 就能产出全部发布物;
2. 每个产物都能回答"我是哪个版本、哪个 commit、什么时候构建的";
3. 四种形态共用同一个版本号(镜像 tag / Chart / 二进制 / 下载文件名一致);
4. 升级有明确路径(尤其 SQLite 库文件的 schema 迁移);
5. 用户在**不看源码**的情况下完成首次部署。

### 3.2 非目标

- 不做 SaaS 托管版;
- 不做自动回滚/灰度发布基础设施(单机自托管为主);
- 不做 Windows 服务封装 / systemd 单元之外的平台集成(先给文件,用户自接);
- 代码签名(Windows Authenticode / macOS notarization)留二期——但
  必须在文档里注明"首次运行会被系统拦截",避免用户以为是恶意软件。

## 4. 交付矩阵

目标产物(每个 tag 触发):

| OS / Arch | 二进制 | Docker | 说明 |
|---|:---:|:---:|---|
| linux/amd64 | ✅ | ✅ | 主力自托管 |
| linux/arm64 | ✅ | ✅ | 树莓派 / ARM 云主机 / Apple Silicon 上的 Linux VM |
| darwin/amd64 | ✅ | — | Intel Mac |
| darwin/arm64 | ✅ | — | Apple Silicon |
| windows/amd64 | ✅ | — | 需说明 CGO 约束(见 §5 D2) |
| windows/arm64 | ⏳ | — | 受限于 CGO,见 §5 D2 |

配套产物:`.tar.gz`/`.zip` 压缩包、`checksums.txt`、`CHANGELOG` 片段、
可选 `deb`/`rpm`(nfpm)。

## 5. 关键决策

### D1 版本来源:git tag 是唯一真相

- **格式**:语义化版本 `vMAJOR.MINOR.PATCH`,tag 直接 `v1.2.3`;
- **注入**:`-ldflags "-X main.version={{.Version} -X main.commit={{.Commit} -X main.date={{.Date}"`;
- **兜底**:即使没有 ldflags,`runtime/debug.ReadBuildInfo()` 也能从二进制里
  读到 `vcs.revision` / `vcs.time`——所以手搓的 `go build` 也不会是"unknown";
- **Changelog**:沿用现有 `CHANGELOG.md`(Keep a Changelog 格式),发版时把
  `## [Unreleased]` 段切成版本号,并由 release workflow 自动生成 GitHub
  Release notes(按 commit 前缀分组:feat/fix/perf/docs/...)。

版本号先从 `v0.1.0` 起步(项目功能已闭环但 API 未承诺稳定),
`P0-P13` 的完成度不需要追认成 `1.0`——按 SemVer,`0.x` 正好表达"功能可用但
接口可能变"。

### D2 CGO 与交叉编译:这是发布骨架唯一的真实难点

**事实**:`internal/store/sqlite.go` 导入 `github.com/mattn/go-sqlite3`,
该驱动**必须 CGO**;当前 Docker 构建用
`CGO_ENABLED=1 GOARCH=amd64 -extldflags '-static'`。

这带来三个具体障碍:

| 目标 | CGO 障碍 |
|---|---|
| linux/arm64 | 需要 `aarch64-linux-gnu-gcc`,CI 的 ubuntu runner 默认没有 |
| darwin/arm64 | 需要 macOS runner(macos-14),Linux 上交叉 CGO 到 darwin 基本不可行 |
| windows/amd64 | 需要 MinGW/MSYS2 工具链,matTN 在 Windows 上编译较麻烦 |
| windows/arm64 | CGO 几乎无法跨到 |

**三个可选路径**:

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A. 原生 runner 矩阵**(推荐起步) | linux/arm64 装 `gcc-aarch64-linux-gnu` 交叉;darwin 用 macos-13/14;windows 用 msys2 装 gcc | 零代码改动,行为完全不变 | CI 矩阵 4-5 个 job,耗时翻倍;windows/arm64 放弃 |
| **B. 切纯 Go SQLite** | `mattn/go-sqlite3` → `modernc.org/sqlite` | `CGO_ENABLED=0`,**一个 runner 交叉出全部 6 个目标**;镜像构造大幅简化 | 依赖替换,影响 store 热路径;性能与 pragma 行为需实测;`sqlite_tuning_test.go` 等测试要复核 |
| **C. 双轨** | 服务端保留 CGO(只发 Linux);客户端用纯 Go | 服务端行为不变;客户端轻量好分发 | 两套构建 + 两套依赖管理 |

**建议**:先按 **A** 打通发布流程(风险最低,不动任何业务代码)。
**B 不再是 P15 的前置条件**——客户端走双二进制、强制无 CGO 即可(见 §5 D7),
B 只是让"服务端也能出轻量跨平台二进制"这一件事变得容易,优先级可降为可选 spike。

**不要**一开始就做 B——发布骨架和依赖替换混在一个 PR 里,出问题分不清是谁的锅。

### D3 镜像策略:一次构建、多标签

- 每个 tag 产出:精确版 `v1.2.3`、滚动版 `v1.2`、`v1`、`latest`;
- **`latest` 只跟随非预发布 tag**,预发布(`v1.2.3-rc.1`)不污染 `latest`;
- 镜像 label 写入 `org.opencontainers.image.{version,revision,created,source}`
  (现有 `docker/metadata-action` 已能自动生成);
- 保留 `ghcr.io/sn0wfree/llmrx:sha-<short>` 供回溯。

### D4 二进制分发:压缩包 + 校验和,不做安装器

- 命名:`llmrx_v1.2.3_linux_amd64.tar.gz`(goreleaser 默认格式);
- 内含:二进制、`config.yml` 示例、`README` 片段、`LICENSE`;
- `checksums.txt` + 可选 cosign 签名(二期);
- 不写 shell 安装器(`curl | sh` 类)。理由:这个项目部署在内网/生产,
  运维需要审计脚本内容;给明确的 `wget + tar + run` 三步比魔术脚本更合适。
- 但**可以**提供 systemd unit 模板(`deploy/systemd/llmrx.service`)。

### D5 Helm:版本与镜像 tag 同源

- `Chart.yaml`: `version` 跟 chart 自身语义版本(可与 app 版本解耦但同步 bump),
  `appVersion` 由 release workflow 在打包时用 `--set` 或 sed 注入成 tag 版本;
- `values.yaml` 的 `image.tag` 默认改成 `""`,模板里 fallback 到
  `.Chart.AppVersion`——这样 `helm install` 不带参数就装对版本;
- 增加 `values.schema.json`(可选):拒绝把 channels/tokens 塞进 config
  (与现有注释一致)。

### D6 升级:SQLite 单文件必须给迁移说明

现有 schema 迁移是**懒迁移**(`s.addColumn()` 在启动时补列)。这对用户是好事
(升级即生效,不用手工 migrate),但必须在文档里明确:

1. **升级前备份 `data/llmrx.db`**(WAL 模式下还要 `-wal`/`-shm`);
2. 升级后首次启动会自动补列;
3. **降级不受支持**——新版本加的列旧版本不认识,回滚需还原备份。

发布说明里每个含 schema 变更的版本都要标 `⚠️ schema change`。

### D7 对 P15 客户端的影响(已澄清:两者解耦)

**早期版本的本文档错误地声称"客户端的分发依赖 D2"——事实并非如此。**

客户端不需要 CGO,只要它**不 import `internal/store` 与 `internal/router`**:

| 包 | 是否 import `internal/store` | 可用于客户端 |
|---|---|---|
| `internal/provider` | ❌ | ✅ 纯 Go |
| `internal/cache` | ❌(只需传入 `*sql.DB`,驱动由调用方决定) | ✅ 纯 Go |
| `internal/model` / `secrets` / `logging` / `ratelimit` | ❌ | ✅ 纯 Go |
| `internal/router` | ✅(engine.go / breaker.go / static.go) | ❌ 会拖动 CGO |
| `internal/store` | 自身即含 `_ "github.com/mattn/go-sqlite3"` | ❌ 会拖动 CGO |

因为 `internal/store/sqlite.go` 有一行驱动注册的 blank import,所以**任何**
对 `internal/store`(以及依赖它的 `internal/router`)的引用,都会把 CGO
sqlite 链进二进制。

结论:

- **客户端**:零 CGO 路径 = `net/http` 转发 + `cache.MemoryCache`(或独立的
  `modernc.org/sqlite`)+ JSONL 用量缓冲 + 自写极简 channel picker,
  `CGO_ENABLED=0` 一次交叉出全平台。**不依赖 D2,不必等它**。
- **服务端**:D2 仍然独立存在——它决定的是「能不能直接发 darwin/windows
  的**服务端**二进制」。若服务端只走 Docker(现状),D2 可以延后。
- 因此建议采用**双二进制**:`llmrx`(服务端,可保留 CGO)与
  `llmrx-client`(客户端,强制 `CGO_ENABLED=0`)。两套构建的成本,
  换来客户端分发的质变。

唯一仍与 P15 耦合的是**版本号**:P15 的 `/client/v1/heartbeat` 要上报客户端
版本、配置 bundle 带 `min_version`,所以 R1(版本注入)必须在 P15.1 之前完成。

## 6. 版本化设计细节

### 6.1 二进制自述

```go
// cmd/gateway/main.go
var (
    version = "dev"      // -X main.version
    commit  = "none"     // -X main.commit
    date    = "unknown"  // -X main.date
)

func buildInfo() string {
    // ldflags 优先;若为 dev 则回退 runtime/debug.ReadBuildInfo()
    // → 返回 "v1.2.3 (a1b2c3d, 2026-11-01T10:00:00Z)"
}
```

新增 flag:`-version`(打印后退出)。**行为要求**:在 `-healthcheck` 之前
短路,与现有 `-wipe-keys` 的短路风格一致(见 `main.go` L62-91)。

### 6.2 运行时可观测

- `GET /health` 现状返回 `{"status":"ok","intent_backend":"..."}`
  (`internal/server/server.go`)。**追加** `"version"` 字段(向后兼容,
  只加不改);
- 所有响应加 `X-Llmrx-Version` 头(便于从客户端侧确认对面版本);
- 启动日志第一行打印版本(现有 `logging.Info` 风格)。

> ⚠️ `server_test.go` / `metrics_test.go` 里如果有对 `/health` body 的精确
> 断言,需同步更新——这是纯增量字段,测试应改为包含式断言。

### 6.3 发布流程

```
1. 开发合入 master(CI: vet + race + 覆盖率门槛 + build)
2. 把 CHANGELOG.md 的 [Unreleased] 改名为 [vX.Y.Z] + 日期
3. 单独 PR 提交 changelog,合入
4. git tag vX.Y.Z && git push origin vX.Y.Z
5. release.yml 触发:
   - goreleaser: 二进制矩阵 + 压缩包 + checksums + GitHub Release
   - docker: buildx linux/amd64+arm64 → ghcr.io
   - helm: 打包 + 上传到 GitHub Release(或 gh-pages 索引)
6. 校验: 下载一个产物、起容器、跑 /health 确认版本号
```

## 7. 需要新增的文件

```
NEW
.goreleaser.yaml                       构建矩阵 + 归档 + checksums
.github/workflows/release.yml          tag 触发,编排 goreleaser + docker + helm
deploy/systemd/llmrx.service           systemd 单元模板(可选 sudo 相关注释)
scripts/release-check.sh               发版前本地自检(changelog/tag/clean tree)
docs/P16-DISTRIBUTION.md               本文件

MODIFY
cmd/gateway/main.go                    +version/commit/date + -version flag
internal/server/server.go              +/health version 字段 + X-Llmrx-Version 头
.github/workflows/docker.yml           移到 release 或补充 arm64(取决于 D2)
.github/workflows/test.yml             对齐 go 版本(1.22)
go.mod                                 go 1.18 → 1.22(与 CI 一致)
Makefile                               +release-dry / +version 变量
deploy/helm/llmrx/Chart.yaml           version/appVersion 占位改为语义版本
deploy/helm/llmrx/values.yaml          image.tag 默认置空,fallback AppVersion
deploy/helm/llmrx/templates/deployment.yaml  tag 用 Chart.AppVersion 兜底
README.md                              安装章节:四条命令对应四种形态
CHANGELOG.md                           首版 [v0.1.0] 切段
```

## 8. 实施顺序(可独立验收)

| 步 | 内容 | 产出 | 量级 |
|---|---|---|---|
| **R1** | `-version` flag + build info + `/health` 版本字段 + 测试 | 二进制能自述版本 | ~120 LoC |
| **R2** | go 版本对齐 + Makefile `version` 变量 + `release-check.sh` | 本地可复现版本注入 | ~60 LoC |
| **R3** | `.goreleaser.yaml` + `release.yml`(先只做 linux amd64/arm64) | 打 tag 出压缩包 + checksums | ~150 行配置 |
| **R4** | docker.yml 补 arm64(D2-A 路径)+ 多标签 | `docker run` 支持 arm | ~40 行配置 |
| **R5** | darwin / windows 矩阵(D2-A) | 全平台二进制 | ~80 行配置 |
| **R6** | Helm 版本注入 + `values.schema.json` | chart 装出正确版本 | ~50 行 |
| **R7** | README 安装章节 + CHANGELOG `[v0.1.0]` | 用户不看源码能装 | 文档 |
| **R8**(可选) | nfpm deb/rpm、systemd unit、cosign | 提升运维体验 | ~100 行配置 |

R1-R4 完成即"能用":有版本号、有 arm 镜像、有可下载的二进制。
R5 是"好用",R8 是锦上添花。

## 9. 与 P15 的接口(已解耦)

**客户端无需等待 D2**。详见 §5 D7:只要客户端不引用 `internal/store` /
`internal/router`,就走纯 Go,`CGO_ENABLED=0` 可直接交叉出全平台。

唯一真实的耦合点是**版本号**:P15 的 `/client/v1/heartbeat` 会上报客户端版本,
配置 bundle 带 `min_version`(见 P15 §4 D4)。所以 R1(`-version` 注入)
必须在 P15.1 之前落地,否则控制面无法做版本兼容判断。

## 10. 验收标准

| 指标 | 目标 |
|---|---|
| `./llmrx -version` 输出语义版本 + commit + 构建时间 | ✅ |
| `git tag v0.1.0 && push` 后 GitHub Release 自动出现全平台产物 | ✅ |
| 下载 linux/arm64 压缩包在 ARM 机器上 `./llmrx -healthcheck` 通过 | ✅ |
| `docker run ghcr.io/sn0wfree/llmrx:v0.1.0` 在 arm64 主机可启动 | ✅ |
| `helm install` 不带参数装出的镜像 tag == Chart appVersion | ✅ |
| `/health` 返回版本且旧字段不变 | ✅ |
| 镜像 tag / Chart / 二进制 / 压缩包四者版本号完全一致 | ✅ |
| 升级(0.0.x → 0.1.0)后旧 DB 可正常打开并自动补列 | ✅ |
| 发布文档包含"升级前备份 DB"与"降级不支持"提示 | ✅ |

## 11. 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| CGO 交叉编译在 CI 上反复失败 | 发布流程卡住 | 先只做 linux amd64(R3),arm64/darwin/windows 逐个加;每个平台单独验证 |
| 依赖替换(modernc)引出性能回退 | 数据面变慢 | 独立 spike + 现成 benchmark(`scripts/loadtest`)+ 不达预期就放弃 |
| arm64 静态链接的 CA 证书路径差异 | TLS 到上游失败 | 沿用 Dockerfile 显式 COPY ca-certificates 的做法,并在 arm64 上实测一次真实上游调用 |
| 打 tag 时 CHANGELOG 还没切段 | Release notes 混乱 | `release-check.sh` 校验 `[Unreleased]` 必须为空或已切段 |
| Windows 用户首次运行被 SmartScreen 拦截 | 用户以为有病毒 | README 明确说明;二期做签名 |
| 版本号写进 `/health` 破坏现有测试 | CI 红 | 用包含式断言;改动与测试同 PR |
| Helm chart 与 app 版本耦合过紧 | chart 改动被迫 bump app | 保持两个版本字段独立,仅 `appVersion` 跟 tag |

## 12. 实施结果(2026-11-01)

R1-R8 全部落地。与规划稿的差异记录在此。

| 步 | 内容 | 产出 |
|---|---|---|
| R1 | `-version` + build info + `/health` 版本字段 + `X-Llmrx-Version` 头 | `cmd/gateway/version.go`, `internal/server/server.go` |
| R2 | go.mod 1.18 → 1.22;Makefile `VERSION/COMMIT/BUILD_DATE` + `make version`;`scripts/release-check.sh` | `Makefile`, `go.mod` |
| R3 | goreleaser 五平台矩阵 + release 工作流 | `.goreleaser.yaml`, `.github/workflows/release.yml` |
| R4 | 多架构镜像(per-arch build + manifest) | 同上 |
| R5 | darwin/windows 矩阵 | 同上 |
| R6 | Helm `image.tag` 回退 `.Chart.AppVersion`;Chart 版本对齐 | `deploy/helm/llmrx/*` |
| R7 | README 四种安装形态 + CHANGELOG `[v0.1.0]` 切段 | `README.md`, `CHANGELOG.md` |
| R8 | systemd unit(非 root + hardening) | `deploy/systemd/llmrx.service` |

### 12.1 关键决定

- **D2 采纳 A(原生 runner 矩阵)**:不替换 `mattn/go-sqlite3`。goreleaser
  按平台显式声明 `CC`(linux/arm64 用 `aarch64-linux-gnu-gcc`,darwin 走
  macOS runner,windows 走 MinGW)。运行时性能 100% 保留。
- **首个版本号 `v0.1.0`**:功能闭环但 API 未承诺稳定,SemVer 的 `0.x`
  正好表达这一点。
- **docker.yml 与 release.yml 职责分离**:前者只出分支开发镜像
  (`edge` / `sha-*`),后者负责正式版本与多架构 manifest,避免抢 `latest`。

### 12.2 版本回退链路(实测)

```
裸 go build          → dev (9cf4306, 2026-11-01T14:00:40Z, go1.22.12)   ← VCS stamp 回退
ldflags 注入         → v0.1.0 (abc1234, 2026-11-01T10:00:00Z, go1.22.12)
-version + 坏 config → 正常输出后退出(不加载配置)
```

### 12.3 仍需人工确认的事项

- `release.yml` 无法在本地验证(GitHub Actions 环境),首次打 tag 时需实跑一次;
- windows 的 CGO 构建依赖 runner 上的 MinGW,若首次失败按 §11 风险表处理;
- Helm chart 打包依赖 `helm` CLI,已在 workflow 中通过 `azure/setup-helm` 提供。

1. **D2 走哪条路**(A 原生矩阵 / B 纯 Go / C 双轨)——建议 A 起步、B 做 spike;
   这个答案同时决定 P15 的构建方式。
2. **首个 tag 版本号**:`v0.1.0`(建议)还是 `v1.0.0`?
3. **是否公开分发**:如果只是内部/自用,ghcr.io 私有 + 本地产物足够,
   R5(darwin/windows)可以砍掉,工作量减半。

---

## 13. 发布演练记录(2026-11-02)

在真正打 `v0.1.0` 之前,把整条发布链路在本机跑了一遍。**演练本身发现了 5 个
缺陷**,其中 3 个是发布产物的真实错误,2 个是校验脚本的错误。

### 13.1 演练范围

| 环节 | 做法 | 结果 |
|---|---|---|
| 门禁脚本 | `scripts/release-check.sh v0.1.0`(含全量测试) | 通过(修 2 个 bug 后) |
| goreleaser 配置 | `goreleaser check` | 1 个配置校验通过(修 1 个 bug 后) |
| 客户端产物 | `goreleaser build --snapshot --id client` | 5 平台,31s |
| 服务端产物 | `goreleaser build --snapshot --id linux-amd64` | 36s |
| Docker 镜像 | `docker build` + 实际运行 | 30.3MB,`/health` 正常 |
| Helm chart | `helm lint` / `template` / `package` | 0 失败,4.4KB |

### 13.2 发现并修复的 5 个问题

**① CI 在测两个已 EOL 的 Go 版本(真实缺陷)**

```yaml
go-version: ['1.18', '1.21', '1.22']   # 修复前
```

`go.mod` 早就声明 `go 1.22`。`setup-go` 遇到模块要求的版本高于矩阵版本时,
构建必然失败——所以 1.18 和 1.21 两条腿**从来没有真正跑过**,只是白占 CI 时间,
还与 P16 §5 D1"对齐到 1.22"的结论矛盾。改为 `['1.22']` 并加注释绑定 go.mod。

**② goreleaser 用了 v2.6+ 才有的 `formats` 字段(真实缺陷)**

配置里写的是 `formats: [tar.gz]`,但 `formats`(复数)从 v2.6 才有;CI 里是
`~> v2` 所以线上碰巧没事,但本地用 v2.5.1 校验直接报:

```
field formats not found in type config.Archive
```

改成兼容整个 v2 系列的 `format: tar.gz`(单数)。**这意味着原配置在 v2.0-v2.5
上会直接失败**——CI 只是恰好用了新版本。

**③ `X-Llmrx-Version` 响应头永远是 `dev`(真实缺陷,最严重)**

Docker 实测:

```
/health body      -> {"version":"v0.1.0-drill"}   OK
X-Llmrx-Version   -> dev                          BAD
```

原因:中间件在 `registerMiddleware()`(由 `New()` 调用)里捕获了
`s.buildVersionOrDev()` 的值,而 `SetBuildVersion()` 在 `New()` **返回之后**才调用。
闭包捕获的是注册时的空值,于是永久定格为 `dev`。

修复:改成每个请求读取一次。同时**测试本身也曾失效**——原来的
`TestVersionHeaderMiddleware` 手工复制了闭包逻辑,所以测不出这个 bug;现在它驱动
真正的 `registerMiddleware`,按真实顺序(先注册、后设版本)断言。

**④ `release-check.sh` 的脏工作区检测误报**

`git status --porcelain` 在 WSL 读 Windows 检出时把**每个文件**都报成已修改
(索引 LF / 工作区 CRLF),505 个文件、11 万行"改动"。改用
`git diff -w --ignore-blank-lines`,纯行尾差异被忽略,真实内容改动仍会失败。

**⑤ `release-check.sh` 的 Go 版本解析错两次**

- 原逻辑 `awk '/go-version:/{print $2}'` 假定单值。CI 是数组
  `['1.18','1.21','1.22']`,取出来是 `[1.18,`,永远不匹配。
- 改成提取所有带引号的版本后仍失败:因为 `go.mod` 在 Windows 侧是 CRLF,
  `awk` 的字段带尾部 `\r`,比较 `1.22\r != 1.22`。两侧都 `tr -d '\r'` 后通过。

### 13.3 产物实测数据

| 产物 | 大小 | 说明 |
|---|---|---|
| `gateway-client` linux/amd64 | 13.4 MB | **statically linked + stripped**,零 CGO |
| `gateway-client` linux/arm64 | 12.9 MB | 同上 |
| `gateway-client` darwin/arm64 | 13.0 MB | 同上 |
| `gateway-client` windows/amd64 | 13.5 MB | 同上 |
| `llmrx` linux/amd64 | 21 MB | CGO 静态链接 |
| `llmrx:drill` 镜像 | 30.3 MB | 演练产物,未加 `-s -w`;发布实测 7.9 MB |

**版本自述验证**:

```
$ ./gateway-client -version
0.0.1-next (359e6ff, 2026-11-02T04:35:59Z, go1.22.12, edge)

$ docker run ... llmrx:drill -version
v0.1.0-drill (local, 2026-11-02T00:00:00Z, go1.22.12)
```

**Helm 版本回退验证**:

```
$ helm template llmrx deploy/helm/llmrx | grep image:
image: "ghcr.io/sn0wfree/llmrx:0.1.0"          # 回退到 appVersion

$ helm template ... --set image.tag=v9.9.9 | grep image:
image: "ghcr.io/sn0wfree/llmrx:v9.9.9"          # 覆盖生效
```

### 13.4 遗留风险(演练中观察到但未改)

**静态链接的 glibc 警告**。服务端二进制构建时持续输出:

```
warning: Using 'getaddrinfo' in statically linked applications requires
at runtime the shared libraries from the glibc version used for linking
```

这是 CGO 静态链接的已知特性。`FROM scratch` 镜像里没有 glibc,理论上 DNS 解析
可能在某些环境下失败(尤其配置了 NSS 模块的域名)。实测 `/health` 与上游请求均
正常,因为 Go 的纯 DNS 解析器在多数场景接管;但**如果生产环境出现上游域名解析
失败,这是第一个要查的地方**。缓解手段:改用 `gcr.io/distroless/static`,或在
scratch 镜像里补 `/etc/nsswitch.conf`。已记入风险,不在本次演练中改动。

**其余未验证项**(需要真实 GitHub 环境):

- `release.yml` 的 goreleaser 全平台矩阵(arm64 需 `gcc-aarch64-linux-gnu`,
  windows 需 MinGW,本机均无);
- 多架构 manifest 合并与 ghcr 推送;
- Helm chart 上传到 Release。

这些只能在首次 `git push origin v0.1.0` 时验证。

### 13.5 演练结论

**可以打 tag。** 本机能验证的部分全部通过,5 个缺陷已修;剩余的只有需要真实
CI 环境才能跑的部分,且失败模式已知(交叉工具链缺失时会明确报错,不会静默产出
坏产物)。

发版步骤:

```bash
# 1. 确认门禁
scripts/release-check.sh v0.1.0

# 2. 打 tag 并推送(触发 release.yml)
git tag v0.1.0
git push origin v0.1.0

# 3. 观察 Actions:goreleaser -> docker -> helm 三个 job
# 4. 下载一个产物验证版本号,并 docker run ghcr.io/...:v0.1.0 -version
```

## 14. 首次真实发布记录(v0.1.0,2026-11-02)

### 14.1 结果

`v0.1.0` 于 2026-11-02T06:00:57Z 触发第 5 次 release 运行,全部 job 通过
(`build-linux` / `build-client` / `build-darwin` / `build-windows` /
`package-and-release` / `docker` / `helm`),发布 12 个资产,总计 56.76 MB。

| 资产 | 大小 |
|---|---|
| `llmrx_0.1.0_linux_amd64.tar.gz` | 8,073,333 |
| `llmrx_0.1.0_linux_arm64.tar.gz` | 7,373,856 |
| `llmrx_0.1.0_darwin_amd64.tar.gz` | 7,611,012 |
| `llmrx_0.1.0_darwin_arm64.tar.gz` | 7,126,999 |
| `llmrx_0.1.0_windows_amd64.zip` | 7,673,106 |
| `gateway-client_*` × 5 | 4.0–4.5 MB |
| `checksums.txt` | 1,017 |
| `llmrx-0.1.0.tgz`(Helm) | 4,547 |

### 14.2 下载校验

用 GitHub API 的资产 `digest` 字段(服务端计算的 sha256)与 `checksums.txt`
逐条比对,**10 个二进制全部一致**,不需要下载即可确认归档未损坏。

实际下载 linux/amd64 两个归档后:

```
gateway-client_0.1.0_linux_amd64.tar.gz: OK
llmrx_0.1.0_linux_amd64.tar.gz: OK

$ ./gateway-client -version
0.1.0 (c778253, 2026-11-02T06:01:13Z, go1.22.12, edge)
```

> 注:本机到 GitHub release CDN 的带宽只有 ~16 KB/s,单次请求 150 s 超时,
> 必须用 `curl -C -` 断点续传。这是网络环境问题,不是产物问题。

### 14.3 发现的两个打包缺陷

**缺陷 1:两个平台的 unix 归档里,二进制都丢了可执行位。**

```
$ tar -tvzf llmrx_0.1.0_linux_amd64.tar.gz
drwx------ runner/runner        0 ./                # ← 700
-rw-r--r-- runner/runner 21652056 ./llmrx           # ← 644
$ tar -tvzf gateway-client_0.1.0_linux_amd64.tar.gz
-rw-r--r-- runner/runner 11219096 ./gateway-client  # ← 644

$ ./llmrx -version
bash: ./llmrx: Permission denied
```

根因:`goreleaser build` 产出的 `dist/` 里二进制是 755,但
`package-and-release` job 用 `actions/download-artifact` 把 `dist/` 从构建
job 取回来——**upload/download-artifact 不保留 unix 权限位**,下载回来一律
644,`cp` 之后仍然是 644,`tar` 如实记录。

同一条流水线还顺带泄漏了第二个权限问题:stage 目录用 `mktemp -d`,默认 700,
被打进 `./` 条目。解压后如果以另一个用户运行(典型:`tar -C /opt` 后用非
root 启动),会连目录都进不去。

**缺陷 2:Helm chart 不在 `checksums.txt` 里。**

`( cd out && sha256sum * > checksums.txt )` 在 `package-and-release` job 里执行,
而 chart 由后面的 `helm` job 单独 `helm package` 后上传,两者没有先后依赖。
结果 12 个资产里只有 10 个有校验和。对运维来说,chart 恰恰是需要校验的那个。

### 14.4 修复

- 打包时把 stage 目录 `chmod 755`,二进制 `chmod 755`,**不依赖上游权限位**;
  Windows 的 zip 分支跳过(zip 不记录 unix 权限位,`python3` 写出的条目
  在 Windows 上也不需要可执行位);
- `package-and-release` 的 `sha256sum *` 只覆盖它自己产出的文件。
  Helm chart 由后面的 `helm` job 单独打包上传,两者无依赖关系,因此 12 个
  资产里只有 10 个有校验和。
  **修复:新增独立 `checksums` job,`needs: [package-and-release, helm]`,
  下载全部资产、统一计算、覆盖上传 `checksums.txt`。**

已按此修改 `.github/workflows/release.yml`;`v0.1.0` 的资产保持原样不改——
校验和缺失已在本节记录,重新发版会在 `v0.1.1` 生效。

### 14.5 镜像与文档的三处不一致

**缺陷 3:镜像 tag 是 `0.1.0`,文档里写的是 `v0.1.0`。**

`docker/metadata-action` 的 `type=semver,pattern={{version}}` 会**去掉 `v`
前缀**。实测 ghcr 上的 tag 列表:

```
master, sha-*, 0.1.0, 0.1, latest, ...
```

没有 `v0.1.0`。而 README 第 213 行、本文件第 42/299/507 行、以及 release
notes 的 Install 段落全都写 `ghcr.io/sn0wfree/llmrx:v0.1.0`——照抄会 404。

**缺陷 4:镜像只有 linux/amd64,但文档声称支持 arm64。**

实测 manifest index 里只有一个真实平台条目:

```
linux/amd64    layers=5  total=7.9 MB
unknown/unknown               (buildx attestation)
```

`docker` job 装了 `gcc-aarch64-linux-gnu`,**但从未使用**——`Build and push
amd64` 步骤只写了一条 `platforms: linux/amd64`。README 第 219 行「支持
linux/amd64 与 linux/arm64(树莓派 / ARM 云主机)」和 §10 验收表的 ✅ 都是
不成立的。

**缺陷 5:实际镜像体积 7.9 MB,文档写 13 MB / 30.3 MB。**

`FROM scratch` + `-s -w` 静态链接后实测 7.9 MB,而 README 写「约 13 MB」、
§13.3 演练表写「30.3 MB」。后者是没加 `-s -w` 的 drill 产物,不该作为
发布数据留在表里。

### 14.6 修复

- `docker/metadata-action` 增加一行 `type=semver,pattern=v{{version}}`,
  让 `ghcr.io/sn0wfree/llmrx:v0.1.1` 这样的 tag 真实存在,文档里的命令成立。
- `docker` job 改为真正的 multi-arch:两个架构各编一次二进制到
  `build/<arch>/llmRx`,再逐架构 `docker buildx build --push`,
  最后用 `docker buildx imagetools create` 合并 manifest。
  `Dockerfile` 保持 `COPY build/llmRx` 不动——本地
  `scripts/build-docker.sh` 和 `docker.yml` 不受影响,buildx 也不需要
  在 QEMU 里跑 Go 编译器。
- 文档体积数字改成实测 7.9 MB。

**本地验证**(本机只有 amd64 工具链,arm64 逻辑靠 CI):

```
$ docker buildx build --platform linux/amd64 -t llmrx:multitest --load .
$ docker run --rm llmrx:multitest -version
dev (4810723, 2026-11-02T07:47:39Z, go1.22.12)
$ docker image inspect llmrx:multitest --format '{{.Size}}'
7930000            # 7.56 MiB 解压后 7.93 MB

$ # 合并机制用本地 registry 验证
$ docker buildx imagetools create \
    --tag 127.0.0.1:5001/llmrx:v9.9.9 --tag 127.0.0.1:5001/llmrx:latest \
    127.0.0.1:5001/llmrx:build-amd64
$ docker buildx imagetools inspect 127.0.0.1:5001/llmrx:v9.9.9
MediaType: application/vnd.oci.image.index.v1+json
  Platform: linux/amd64
```

`v` 前缀 tag 与 manifest index 合并两条路径都实测通过。arm64 那一半需要
CI 的 `gcc-aarch64-linux-gnu`。

### 14.7 修复后的 CI 状态

`fb3a2b4` 推送后:

| workflow | 结果 |
|---|---|
| `docker` | ✅ success |
| `test` | ❌ failure(`Test with race detector`) |

`docker` 通过是关键信号:`Dockerfile` 保持 `COPY build/llmRx` 的设计确实
没有破坏既有构建路径。

`test` 的失败**不是本次改动引入的**——`f0a3fa5`(推送前的部署提交)在
1.21 上就已经失败,更早的 `a6ff331`、`b047551`、`46a3b21` 也连续失败。
本地复现全绿,三种配置都试过:

```
go test -race ./...                        # 全部 ok
go test -race -count=1 ./...               # 全部 ok
GOMAXPROCS=2 go test -race -count=1 -p 1 ./...   # 全部 ok
GOMAXPROCS=2 go test -race -count=1 -timeout=300s ./...  # 全部 ok
```

CI 上该步骤耗时 4m54s 后失败,而本地同一组测试约 4 分钟,说明不是超时
被杀(Go 默认 10 分钟)。差异点是 runner 的 CPU/内存规格。**这是既有问题,
需要单独排查**:拿不到 CI 日志正文(未登录的 `gh` 无法下载),只能在
runner 上复跑才能定位。已知的两个可疑点:

- `internal/webui` 在 race 下要 68–93s,是全仓最慢的包,可能有依赖
  真实时钟的窗口断言;
- `internal/router/auto/llm_test.go:67` 断言 `time.Since(start) < 200ms`,
  在慢 runner 上会假失败。

已记入遗留项,不在本次发布修复范围内。

### 14.8 `v0.1.1` 的两次 release 失败

**第一次(`5e09f3c`)**:`docker` 与 `checksums` 两个 job 失败。

- `checksums` 失败在 `Download every release asset`。该 job 没有
  `actions/checkout`,而 `gh release download` 需要从 git remote 推断
  仓库名。修复:加 checkout,并显式传 `--repo "$GITHUB_REPOSITORY"`。
  `helm` job 的 `gh release upload` 同样补上 `--repo`。
- `docker` 失败在 `Build and push per-architecture images`。手写的
  `docker buildx build --push` 在默认 `docker` driver 上没能按 ghcr
  期望的方式建立 builder 与认证——ghcr 上根本查不到 `build-amd64`
  tag,说明第一个镜像就没推上去。改为两个 `docker/build-push-action@v5`
  步骤(与 `v0.1.0` 已验证的路径一致)。

**第二次(`11f27aa`)**:`checksums` 通过(证明 `--repo` 修复有效),
但 `docker` 仍在 `Build amd64 image` 失败。

这次的改动是两个 `build-push-action` + 一个 `Stage amd64 binary`
(`cp build/amd64/llmRx build/llmRx`)。失败仍无日志可读——`gho_` 类型的
token 缺 `workflow` scope,`/logs` 端点返回 401。

**采用的修复**:把 staging 步骤彻底去掉,改用构建参数。`Dockerfile`
增加:

```dockerfile
ARG BIN_PATH=build/llmRx
COPY ${BIN_PATH} /usr/local/bin/llmRx
```

release 的两个 build-push-action 分别传
`BIN_PATH=build/amd64/llmRx` 与 `BIN_PATH=build/arm64/llmRx`;
本地 `docker build` 与 `docker.yml` 不传参,默认值保持原行为。

本地已验证:

```
$ docker build --build-arg BIN_PATH=build/amd64/llmRx -t llmrx:argtest .
$ docker run --rm llmrx:argtest -version
dev (4810723, 2026-11-02T07:47:39Z, go1.22.12)
```

同时 `Build binaries for both architectures` 增加了
`ls -lR build` 与 `file` 输出,下次失败可以从日志直接看到二进制是否
真的产出。这是一次有诊断价值的重试,不是盲目重跑到绿为止。

**第三次(`a81c962`)** 仍然在 `Build amd64 image` 失败。三个不同的
实现(手写 buildx、staging + action、build-arg + action)在同一处失败,
说明问题不在二进制路径,而在镜像引用本身。

**根因:GHCR 不接受大写仓库名。** `github.repository` 是
`sn0wfree/llmRx`,而 GHCR 的镜像路径必须全小写:

```
$ curl -o /dev/null -w '%{http_code}' https://ghcr.io/v2/sn0wfree/llmRx/tags/list
404
$ curl -o /dev/null -w '%{http_code}' https://ghcr.io/v2/sn0wfree/llmrx/tags/list
401        # 401 而非 404，说明路径存在、只是需要认证
```

`docker/metadata-action` 会把镜像名自动转小写,所以 `v0.1.0` 里
`tags: ${{ steps.meta.outputs.tags }}` 是好的;而多架构方案里我手写了
`${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:build-amd64`——**大写 `R` 原样
保留,推送到 `ghcr.io/sn0wfree/llmRx:build-amd64` 直接被拒**,而
`build-push-action` 把它报成第一步失败。

这解释了为什么 `v0.1.0` 成功、`v0.1.1` 连续三次失败:唯一变化就是
中间镜像的引用从 metadata-action 的产物变成了手写字符串。

**修复**:新增 `Normalise the image reference` 步骤,用
`tr '[:upper:]' '[:lower:]'` 生成 `ref`,三个步骤统一引用
`${{ steps.img.outputs.ref }}`。

**第四次(`4bde99b`)**:两个架构的镜像都推送成功,`imagetools create`
也完成了,失败在最后一步 `imagetools inspect`——`steps.meta.outputs.tags`
是**多行**的多个 tag,`inspect` 只接受一个引用。改成取第一行。
这是纯诊断步骤的失败,发布本身已成功。

**实测发布结果**(`ghcr.io/sn0wfree/llmrx`):

```
v0.1.1: application/vnd.oci.image.index.v1+json
   linux/amd64            7.94 MB  layers=5
   unknown/unknown      attestation
   linux/arm64            7.32 MB  layers=5
   unknown/unknown      attestation

0.1.1   同上   latest  同上
```

多架构 manifest、`v` 前缀 tag、arm64 平台三项全部达成。

### 14.9 `v0.1.1` 产物实测验收

下载 `llmrx_0.1.1_linux_amd64.tar.gz` 与
`gateway-client_0.1.1_linux_amd64.tar.gz`,与发布版 `checksums.txt` 对照:

```
OK   llmrx_0.1.1_linux_amd64.tar.gz
OK   gateway-client_0.1.1_linux_amd64.tar.gz
```

归档权限(§14.3 缺陷 1 的验收点):

```
drwxr-xr-x runner/runner        0 ./                # 755，不再是 700
-rwxr-xr-x runner/runner 21652056 ./llmrx           # 755，不再是 644
drwxr-xr-x runner/runner        0 ./systemd/
-rwxr-xr-x runner/runner 11219096 ./gateway-client  # 755
```

运行:

```
$ ./llmrx -version
0.1.1 (4bde99b, 2026-11-02T09:08:53Z, go1.22.12)

$ ./gateway-client -version
0.1.1 (4bde99b, 2026-11-02T09:08:48Z, go1.22.12, edge)
```

镜像(§14.5 缺陷 3/4 的验收点):

```
v0.1.1 / 0.1.1 / latest  →  OCI image index
   linux/amd64   7.94 MB
   linux/arm64   7.32 MB
```

`checksums.txt` 覆盖 12 个资产中的 11 个个二进制归档 + chart
(共 1099 字节,含 chart 的 sha256)。

**结论:`v0.1.1` 是第一个「下载即可用」的版本。** `v0.1.0` 的
`f0a3fa5` 之前的问题(可执行位、chart 缺校验和、镜像无 `v` tag、
无 arm64)均已修复并实测确认。
