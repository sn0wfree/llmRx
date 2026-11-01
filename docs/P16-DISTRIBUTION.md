# P16 — 交付与发布 / Distribution & Release

> Date: 2026-11-01 · Owner: llmRx maintainers · Status: **规划稿(未实施)**
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

**建议**:先按 **A** 打通发布流程(风险最低,不动任何业务代码);
把 **B** 作为独立的性能验证 spike(它同时能解决 P15 客户端的分发问题,
见 §9)。**不要**一开始就做 B——发布骨架和依赖替换混在一个 PR 里,
出问题分不清是谁的锅。

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

### D7 对 P15 客户端的影响(先记录,不阻塞)

- 若走 D2-A:客户端是 CGO 二进制,Windows 需要 MinGW 构建,P15 分发变复杂;
- 若走 D2-B:客户端(以及服务端)全部无 CGO,`llmrx-client` 可以做到
  几 MB 单文件,`goreleaser` 一次出全平台——**这是 P15 最想要的结果**;
- 因此 **D2-B 的 spike 结果直接决定 P15 的交付体验**,建议在 P15 动工前完成。

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

## 9. 与 P15 的接口(重要)

D2 的选择会外溢到 P15:

- **若采用 D2-B(纯 Go SQLite)**:`llmrx-client` 可以 `CGO_ENABLED=0` 交叉编译,
  一次出全部平台;客户端二进制可从十几 MB 降到几 MB;桌面分发体验质变。
  **建议在 P15.1 之前先做这个 spike**——它的结论决定 P15 的构建方式,
  反过来做会返工。
- **若坚持 D2-A**:P15 客户端也要走原生 runner 矩阵,Windows 用户拿到的
  客户端需要 MinGW 构建产物;P15 §17 的打包步骤相应加重。

另一个与 P15 的耦合点:**`client_id` 与版本的对应**。P15 的
`/client/v1/heartbeat` 会上报客户端版本,而配置 bundle 里带 `min_version`
(见 P15 §4 D4)。所以版本号必须在 P15.1 之前就位,否则控制面无法做版本
兼容判断。

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

## 12. 待拍板

1. **D2 走哪条路**(A 原生矩阵 / B 纯 Go / C 双轨)——建议 A 起步、B 做 spike;
   这个答案同时决定 P15 的构建方式。
2. **首个 tag 版本号**:`v0.1.0`(建议)还是 `v1.0.0`?
3. **是否公开分发**:如果只是内部/自用,ghcr.io 私有 + 本地产物足够,
   R5(darwin/windows)可以砍掉,工作量减半。
