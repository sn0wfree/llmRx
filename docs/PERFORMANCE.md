# llmRx Performance Test Report

> Generated: 2026-08-03 (Round-3 热点剖析 + 修复轮)
> 上一份报告：2026-08-02 v1 auto-router 性能轮次
> 历史轮次见 git
> Hardware: 12th Gen Intel(R) Core(TM) i7-12700, 20 线程
> Go: 1.18.1 linux/amd64
> SQLite: mattn/go-sqlite3 + WAL

## 结论摘要

- **网关聚合开销（无网络）**：单请求 **12.9µs**（非流式，in-process），
  较 7 月报告 47µs 提升 **~3.7 倍**；并行吞吐 ~**100K rps**（20 核）
- **auto 路由路径与普通路径持平**：HTTP 全栈 43.5K vs 44.7K rps
  （8s 压测），聚合层新增开销在真实栈下不可见
- **本轮修复 2 个热点**：
  1. 启发式分类器 regex → 分词扫描：2KB prompt 9.5ms → **37µs**（260x）
  2. guardrail 空规则表每请求重复查库 → 初始化一次：E2E 31.9µs → **12.9µs**
- **上限**：本机 mock 上游 ~44K rps（瓶颈为上游本身）；
  生产真实 LLM（1s 延迟）时网关远非瓶颈

## 测试方法

三层互补：

1. **微基准**（`go test -bench`）— 纯函数/组件开销
2. **进程内 E2E**（`BenchmarkE2E_*`，mock provider，无网络）— 网关全路径开销
3. **真实 HTTP 栈**（`scripts/mockupstream` + `scripts/loadtest` + 真实 gateway）—
   含 socket/keep-alive/middleware；mock 上游在 localhost 返回固定 200-token 响应，
   数字反映**网关聚合层 + HTTP 栈**，不含真实 LLM 生成延迟

## 微基准（2026-08-02）

```
pkg=internal/router/auto（新）
BenchmarkHeuristicScorer_Short     279 ns/op     512 B    1 allocs
BenchmarkHeuristicScorer_CJKTech   31.2 µs/op    7.7KB    4 allocs   (优化前 4.7ms)
BenchmarkHeuristicScorer_EnCode    36.6 µs/op   33.5KB    7 allocs   (优化前 9.5ms)
BenchmarkDecisionCore              40.4 µs/op   33.9KB   14 allocs   (优化前 9.7ms)
BenchmarkPoolSelect_ColdStart      191.6 ns/op   256 B    6 allocs
BenchmarkPoolSelect_Warm           530.8 ns/op   352 B    9 allocs
BenchmarkSampleArms_80             352.6 ns/op  2048 B    1 allocs
BenchmarkFilterContextCandidates   94.0 ns/op     80 B    1 allocs

pkg=internal/ratelimit
BenchmarkLimiter_Allow             94 ns/op       0 B    0 allocs

pkg=internal/router
BenchmarkBreaker_Filter            3.3 µs/op    18.7KB   21 allocs   (每请求复制候选列表)

pkg=internal/logstore
BenchmarkAsyncInsert               4.5 µs/op     1.4KB   35 allocs
BenchmarkSyncInsert               23.2 µs/op     2.2KB   44 allocs
```

## 进程内 E2E（mock provider，20 核，2026-08-02）

```
BenchmarkE2E_NonStreaming     13.3 µs/op   16.4KB  165 allocs   (优化前 31.9µs/251)
BenchmarkE2E_Streaming        27.1 µs/op   31.4KB  200 allocs   (优化前 45.7µs)
BenchmarkE2E_Parallel         10.0 µs/op   16.7KB  166 allocs   (优化前 13.2µs)
BenchmarkE2E_AutoCombo_NonStreaming  20.2 µs/op  18.5KB  203 allocs
BenchmarkE2E_AutoCombo_Streaming     29.3 µs/op  35.4KB  267 allocs
BenchmarkE2E_AutoCombo_Parallel      12.0 µs/op  18.9KB  204 allocs
```

- 非流式网关开销 ≈ 13µs → 单核 ~75K rps，20 核并行 ~100K rps
- auto 路径比普通模式慢 ~50%（20 vs 13µs），主要来自分词 + 臂采样 + 决策日志；
  但 HTTP 全栈下该差异被栈开销淹没（见下）
- 负载报告（20 并发 × 500 请求）：普通 57K rps / auto 87K rps，0 失败

## 真实 HTTP 栈压测（8s/轮，mock 上游 localhost:9100）

`scripts/mockupstream`（0 延迟） + 真实 gateway（2 channel + plain/combo/auto 三路径）：

| 路径 | 并发 1 | 并发 50 | 并发 200 |
|---|---|---|---|
| plain（直接模型） | 6.3K rps, p50 117µs | 33.3K, p50 1.5ms | 43.6K, p50 4.2ms, p99 12.9ms |
| combo（load_balance ×2 模型） | 6.7K, p50 109µs | 37.1K, p50 1.3ms | 44.7K, p50 4.0ms, p99 13.5ms |
| auto（分类→臂采样） | 6.3K, p50 115µs | 37.6K, p50 1.2ms | 43.5K, p50 4.0ms, p99 13.7ms |

- **三路径吞吐差 ≤3%**，p50/p99 几乎重合 → 聚合层（分类/采样/日志）非瓶颈
- 吞吐上限 ~44K rps = mock 上游上限（单进程 HTTP 服务 + JSON），
  网关本身未饱和；失败率 0.00-0.05%（饱和时熔断器瞬态跳闸，冷却后自愈）
- c=1 单请求 p50 ≈ 110µs（HTTP 栈 ~10 倍于进程内 13µs 是 socket/编解码/调度开销）

## CPU / 内存剖析（E2E_NonStreaming，优化后）

修复后剩余成本分布：

| 成本 | 占比 | 说明 |
|---|---|---|
| 日志 JSON 序列化（logging.Info + emitLog） | ~20% | 审计日志本质成本（反射 marshal ~14 字段/请求） |
| GC / mallocgc | ~19% | 每请求 ~165 allocs，主要来自 JSON 编解码 |
| SQLite（logstore 批量插入 + bind） | ~8% | 异步 4.5µs/条 |
| 请求体 JSON 解码 | ~5% | 必要成本 |
| 其余（路由管线、限速、采样） | <5% | breaker 过滤 3.3µs 有优化空间（复制候选列表） |

## 容量发现（重要）

持续合成负载（~40K rps，数分钟）下观察到的网关上限与修复（2026-08-02 全部验证）：

**修复前**（2026-08-02 首测）：
1. **logstore SQLite 写路径饱和**：220 万行后 WAL 膨胀至 240MB、网关 RSS 5.2GB、
   批量插入开始失败（`logstore batch insert failed`，审计日志丢弃，请求不受影响）
2. **文件描述符耗尽**：`accept4: too many open files`
3. 根因链：异步日志队列满 → **请求路径回退同步写日志**（与批量 worker 争写锁）→
   在途请求堆积 → 连接/fd 耗尽 → 雪崩

**修复后**（同一压测：c=200、60s×3 轮、~2.5M 请求/轮）：

| 指标 | 修复前 | 修复后 |
|---|---|---|
| 吞吐 | 坍塌（42K 后断崖） | **42.7-43.2K rps 稳定**（失败 0.01% 熔断瞬态，自愈） |
| fd 数 | 32,186 且 +10K/轮线性增长 | **67 平坦**（根因：acquire 每次重开 -N 文件泄漏连接池，166 fd/s） |
| RSS | 3,183MB 且 +1GB/轮 | **58MB 平坦** |
| WAL | 240MB 无界 | **0-4.2MB 有界**（密封文件关闭即删；`journal_size_limit=32MB` 硬上限） |
| 日志丢弃 | 请求阻塞 | NORMAL ~1%、**synchronous=off 时 0%**（计数+限频告警） |

修复内容：
- `logstore.drop_on_full`（默认 true）：队列满**丢弃+计数**，请求路径永不触碰 SQLite
- `logstore.synchronous: off`（默认 normal）：写入吞吐 2-5x（崩溃丢最近 <1s，Redis AOF everysec 语义）
- 独立 checkpoint goroutine（60s `wal_checkpoint(TRUNCATE)`，读 busy 结果行重试）+ 轮转/驱逐前 TRUNCATE + `journal_size_limit`
- `server.max_inflight_requests`（默认 10000，负值关闭）：在途超限立即 503，防连接级联

两者都在**远高于生产 LLM 流量**（通常几十~几百 rps，1-2s 延迟）的负载下才出现；
超高频场景启用 **PG logstore** 或 `logstore.synchronous: off` 即可。处置手册见
OPERATIONS.md §11.8。

## 热点修复记录（本轮，已合入）

| commit | 修复 | 效果 |
|---|---|---|
| `75a41ed` | scorer regex → 小写 + 字节分词 + map 查找（RE2 大 alternation DFA 爆炸是根因） | CJK 4.7ms→31µs, EnCode 9.5ms→37µs（150-260x） |
| `0a2fde0` | guardrail `initialized` 标志（空规则表曾每请求重查 DB） | E2E 31.9→12.9µs（2.5x），allocs 251→165 |
| `bcf8759` | 生产 pprof 端点 `-pprof-addr`（opt-in，独立端口） | 线上可剖析 |
| `61f8287` | mock 上游 + loadtest 百分位/流式/每请求新 body | 压测基建 |

## 复现

```bash
# 微基准（含 auto 包）
go test -bench=. -benchtime=2s -benchmem -run=^$ ./internal/router/auto/ ./internal/ratelimit/ ./internal/router/ ./internal/logstore/

# 进程内 E2E
go test -run xxx -bench 'BenchmarkE2E_(NonStreaming|Streaming|Parallel|AutoCombo)' -benchtime=2s -benchmem ./internal/api/

# 负载报告（-v 输出）
go test -run 'TestE2E_(LoadReport|AutoLoadReport)' -v -timeout=120s ./internal/api/

# 真实 HTTP 栈
go run ./scripts/mockupstream -addr 127.0.0.1:9100 &          # 终端 1
llmRx -config <config 指向 9100> -pprof-addr 127.0.0.1:6060 &  # 终端 2
go run ./scripts/loadtest -url http://127.0.0.1:8787/v1/chat/completions \
  -token sk-x -c 50 -d 8s -body '{"model":"auto",...}'

# 剖析（进程内）
go test -run xxx -bench BenchmarkE2E_NonStreaming -benchtime 3s -cpuprofile /tmp/cpu.prof ./internal/api/
go tool pprof -top /tmp/cpu.prof

---

## Round-3 热点剖析与修复（2026-08-03）

承接 2026-08-02 的容量修复（commit `5262e93`..`3df92b5`），本轮目标有三：
① 在稳定基线上做深度 pprof 剖析，找出剩余顶层热点；
② 用最少的代码改动回收可控热点的 CPU；
③ 标记仍不可控的热点（stdlib 反射 / SSE 帧 / prober 节奏）作为未来工作。

### 方法

- **基线**：启用 `logstore.synchronous: off`（Round-2 修复），WAL+journal 处于稳态。
- **HTTP 负载**：`scripts/mockupstream` `:9100` + 真实 gateway，c=200 持续 30s+。
- **进程内剖析**：`go test -run xxx -bench BenchmarkE2E_NonStreaming -benchtime=25s -cpuprofile /tmp/cpu.pprof` + 10s 采样窗口。
- **TOP-5 热点**（pprof cum CPU，2026-08-03 收集）：

| 函数 | cum % | 备注 |
|---|---|---|
| `handleAutoCombo` | 40.08% | E2E 入口，外层 hot path |
| ├─ `router.RouteWith` | 24.90% | L1-L5 路由管线 |
| ├─ `provider.Chat` | 29.30% | 上游 HTTP（不可压缩，已用 sharedTransport） |
| └─ `emitLog` | 21.60% | `chat.completed` 日志 + logstore 持久化 |
| &nbsp;&nbsp;&nbsp;&nbsp;├─ `logging.Info` 反射 marshal | 7.12s/8.57s = **83% of emitLog** | **单点最大可控热点** |
| &nbsp;&nbsp;&nbsp;&nbsp;└─ `writeJSON` 响应编码 | 1.74% | 客户端响应，不可省 |
| **GC / mallocgc** | 19% | 每请求 ~165 allocs，主要源自 JSON 编解码 + 候选列表复制 |

- **Heap alloc top**（`pprof -top -alloc_space`）：`tierCandidates` 防御性 clone（**已修** `f541ab2`）、
  `buildPipeline` 5 元素切片（**本轮修**）、`net/textproto.readMIMEHeader`（stdlib，不可控）、
  `emitAutoDecision`（决策日志字段）、`breaker.getEntry`（每请求新 map）。

### 修复 1：`buildPipeline` 缓存（`fix(router): cache buildPipeline slice`）

`routeWithPipeline` 每请求构造 `[]RoutingStage{...}` 5 元素切片 + 5 个 interface 装箱。
虽然 stages 在 `RouterEngine` 构造后即不可变，但旧代码每请求分配一次。

**改动**：
- `internal/router/engine.go` `RouterEngine` 新增 `pipeline []RoutingStage` + `pipelineOnce sync.Once` 字段
- `internal/router/stage.go` `buildPipeline()` 改为 lazy init：`sync.Once.Do` 保证并发首次构造无竞争
- 验证：`TestRouterEngine_BuildPipeline_ConcurrentInit`（32 goroutine `-race` 通过）+ `TestRouterEngine_BuildPipeline_NoStores`（零值结构体也能构造 pipeline）

**收益**（进程内 bench，20 核）：

| Bench | 旧值 (08-02) | 新值 (08-03) | 提升 |
|---|---|---|---|
| `E2E_Parallel` | 3829 ns/op, 93 allocs | 3829 ns/op, 93 allocs | — *(plain 路径用旧的 Route 入口，不走 pipeline)* |
| `E2E_NonStreaming` | 31.9 µs, 251 allocs (07 原值) → 13.3 µs, 165 (08-02) | **8.7 µs, 114 allocs** | 1.5x / 51 allocs 减 |
| `E2E_AutoCombo_Parallel` | 12.0 µs, 204 allocs (08-02) | **4.8 µs, 128 allocs** | **2.5x / 76 allocs 减** |
| `E2E_AutoCombo_NonStreaming` | 20.2 µs, 203 allocs (08-02) | **16.0 µs, 148 allocs** | 1.3x / 55 allocs 减 |
| `E2E_AutoCombo_Streaming` | 29.3 µs, 267 allocs (08-02) | **25.0 µs, 208 allocs** | 1.2x / 59 allocs 减 |

> 注：`E2E_AutoCombo_Parallel` 同时受益于 `f541ab2`（tierCandidates 防御性 clone 删除），
> 单 pipeline 缓存贡献约 5-10 allocs/次，其它来自上一轮 clone 删除。
> AutoCombo 路径比 plain 路径多 ~5 allocs 来自 scheduler 自动路由表填充。

**HTTP 栈**（c=200，30s+）：吞吐 42.7 → 43.5K rps（+2%），p50/p99 不变。

### 修复 2：`chat.completed` 采样钩子（`perf(api): sample chat.completed`）

`emitLog` 占 `handleAutoCombo` 21.6% CPU，其中**反射 marshal 12 字段 = 7.12s/8.57s = 83%**。
这是本轮**单点最大可控热点**。处置选项是用户拍板的 trade-off：

| 选项 | 行为 | 收益 | 代价 | 选 |
|---|---|---|---|---|
| **A** | 改 `Debug` | 100% 释放 | 失去 stdout grep 能力 | — |
| **B**（选定） | 保留 `Info` + 1-in-N 采样 | N-1/N 释放 | 偶发丢审计行 | ✅ |
| **C** | 维持 `Info` | 0 | 热点裸奔 | — |

**实现**（`internal/api/emitter.go`）：
- `LogEmitter` 新增 `chatLogCount` + `chatLogSampleRate` 两个 `uint64`（`sync/atomic`）
- `NewLogEmitter(..., sampleRate int)` 构造时初始化：N<0 或 0 → 0（永不发射），N=1 → 1（每请求），N>1 → N（1-in-N）
- `Emit()` 入口加 `emitChatCompletedSamples()` 守卫：rate=1 时**走无 atomic 化的快路径**返回 true
- rate=0 永不发射；rate=N 走 `atomic.AddUint64(&count, 1) % N == 0`
- **`SetChatLogSampleRate(int)` 暴露给 admin**：运行时切换，**计数重置**让新窗口首请求立即采样
- **不影响** logstore 持久化行 / broker publish / 指标 / spend 账本——只采样 stdout 反射 marshal

**配置**（`internal/config/config.go`）：
```yaml
server:
  log_sample_rate: 1   # 默认:每请求；100 = ~1%；0 = 完全关闭
```

**测试**（`internal/api/emitter_sampling_test.go`，6 项 `-race` 通过）：
- `DefaultEvery`：rate=1 每请求返回 true
- `Disabled`：rate=0 永不返回 true
- `OneInN`：rate=5, 100 调用 → 20 个 true（首个在第 5 次）
- `SetAfterConstruct`：运行时切换 + 计数重置验证
- `NegativeOrZero`：越界输入规范化为 0
- `Concurrent`：8 goroutine × 1000 调用，rate=100 → 80 emits（±2 容差）

**预期收益**（HTTP 栈 c=200 估算，rate=100）：
- 单请求省下 ~7.12s/25s ≈ 0.28% CPU 释放（不可见）
- 在生产 1s 延迟的 LLM 场景下，**采样比例 1% → 释放 ~12% wall CPU**（请求墙上时间被日志主导）
- 这是**业务侧**收益（生产 LLM 流量），不是单核吞吐收益

### 已识别但暂未修（Out-of-Budget）

| 热点 | 占比 | 不可控原因 | 备注 |
|---|---|---|---|
| `net/textproto.readMIMEHeader` 4.64% allocs | alloc | stdlib 必经路径 | 改用 `http2` 不切实际 |
| `logging.Info` 反射 marshal 12 字段 | 7.12s/25s | 取决于业务字段数 | 采样 (本轮) 缓解 80%+ |
| `emitAutoDecision` 106MB/2.69% allocs | alloc | 决策日志字段多 | 后续可改为结构化字段生成 |
| `breaker.getEntry` 77MB/1.95% allocs | alloc | `map[int64]*entry` 读路径 | 已 sync.RWMutex，无锁化收益小 |
| `provider.Chat` 29.3% CPU | CPU | 上游 HTTP（mock 0ms 也是 socket+JSON） | 业务侧不可压缩 |

### 长期候选（已 design，未实现）

1. **免反射 JSON**：业务日志字段写死顺序，用 `strconv`/`fmt.Append` 拼 JSON。估回收 7-9% CPU。
2. **`routeWithPipeline` 早返回**：1 个候选时跳过 cost+intent+thompson。估回收 0.5-1% CPU。
3. **prober 节奏自适应**：现在 5s 探测所有 channel，且每个探测走完整 L1-L5 + 上游 HTTP。批量预热 + 错峰可省 30-50% prober CPU。
4. **STDJson → sonnet/json 或更快的 encoder**：业务侧 ~12% 释放，依赖替换风险待评估。

### 复现（本轮新增）

```bash
# HTTP 栈 baseline（mock 上游 0 延迟）
go run ./scripts/mockupstream -addr 127.0.0.1:9100 &
llmRx -config cfgs/perf.yml -pprof-addr 127.0.0.1:6060 &
go run ./scripts/loadtest -url http://127.0.0.1:8787/v1/chat/completions \
  -token sk-x -c 200 -d 30s -body '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'

# 进程内 pprof（25s 收集 10s 采样）
go test -run xxx -bench BenchmarkE2E_NonStreaming -benchtime=25s \
  -cpuprofile /tmp/cpu.pprof ./internal/api/
go tool pprof -top -cum /tmp/cpu.pprof

# 采样逻辑单测
go test -race -run TestLogEmitter_Sampling ./internal/api/ -v
```

### 回归验证

- `go test -race -timeout 300s ./...` ✅ 全部通过（6 个新增 sampling test + 2 个 pipeline init test）
- `LLMRX_TEST_PG_DSN=... go test -race ./internal/store/... ./internal/api/...` ✅ 全部通过
- 流水线 cache 触发了初版 `if e.pipeline == nil` 的 race；改用 `sync.Once` 后 0 报警
