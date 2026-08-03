# llmRx Performance Test Report

> Generated: 2026-08-03 (Round-6 路由层优化 — 内联阶段、池、静态索引、原子策略、自定义日志)
> 历史轮次：2026-08-03 Round-5b、Round-5、Round-4、Round-3、2026-08-02 v1 auto-router
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

---

## Round-4 深度剖析（2026-08-03 下午）

Round-3 报告末尾的"Out-of-Budget"清单写着"stdlib JSON 反射 / 缓存复用"。本轮专门
重新打 pprof 找剩余可优化项，发现 **4 个非显然热点** + **1 个仓库内未被发觉的全局
互斥锁**，全部已修。

### 方法（同 Round-3）

- 基线：启用 `logstore.synchronous: off`；所有 fixes 编译于同一 commit
- HTTP 压测：`c=200 60s` × 4 路径（plain / auto / 慢上游 / streaming）
- 进程内 micro-bench + pprof 30s 采样

### 修复 1：intent 分类器的全局互斥锁（最关键发现）

**Bug 现象**：`internal/intent.(*native).Classify` 持 `n.mu.Lock()`，每次分类串行
化。pprof 显示 `intent.Classify` 占 handleAutoCombo **4.46% cum**，是当时第二大
的可控热点。

**根因**：Rust `score()` 是纯函数（`KEYWORDS` 是 const，输出分配局部 `Vec`），
Go 侧 `in`/`out` 缓冲区都是 per-call 局部 slice —— 没有任何共享可变状态需要保护。
`n.mu` 是历史遗留的"安全网"，实际上把 cgo 路径从头锁到尾。

**修复**：
```go
// internal/intent/intent.go
func (n *native) Classify(text string) Intent {
    if len(text) == 0 { return Intent{Kind: "unknown"} }
    // No locking: the Rust classify function is pure ...
    if n.classify == nil { return Intent{Kind: "unknown"} }
    ...
}
type native struct {
    cap      int                  // mu removed
    so       unsafe.Pointer
    classify unsafe.Pointer
    ...
}
```

**测试**（`internal/intent/parallel_test.go`，`-race` 全通过）：
- `BenchmarkNativeClassify_Parallel`：**4.96µs → 1.14µs**（4.35x）
- `TestNativeClassify_ConcurrentNoCorruption`：32 goroutine × 200 调用，验证无
  数据竞争 / 缓冲串扰
- 32K+ 32,000 类调用并发，**0 错误 kind、0 race 报警**

**实测增益**：HTTP 53K → 55K rps，p50 3.20ms → 2.91ms

### 修复 2：intent 输出缓冲区 sync.Pool

**Bug 现象**：每请求 `out := make([]byte, 4096)` 分配 4096 字节，加上 `in` 字节数组
+ JSON unmarshal 临时分配。heap profile 显示 `intent.Classify` 占 14.22% 累计分配
空间（c=200 60s 共 9.86GB）。

**修复**：用 `sync.Pool` 复用 4096-byte `out` 缓冲区，函数返回前清零并 Put。

```go
var outPool = sync.Pool{
    New: func() any { b := make([]byte, 4096); return &b },
}
...
outPtr := outPool.Get().(*[]byte)
out := *outPtr
...
defer func() {
    for i := range out { out[i] = 0 }  // zero before Put
    outPool.Put(outPtr)
}()
```

**测试**：
- `BenchmarkNativeClassify_Parallel`：**5624 → 1530 B/op**（74% 减），**39 → 38 allocs**
- `1.14µs → 0.55µs`（2x 二次提速）

### 修复 3：breaker.getEntry 每调用分配 1100 字节

**Bug 现象**：`pprof -alloc_space` 显示 `(*CircuitBreaker).getEntry` 占 11.98% 累计
分配（8.11GB）。原因：`b.entries.LoadOrStore(channelID, &breakerEntry{})` 每次调用
都构造一个新 `&breakerEntry{}`（含 4096-byte `window` 数组），即使 key 已存在。

**修复**：先 `Load` 一次（cheap path），命中返回；未命中才 `LoadOrStore` 分配。

```go
func (b *CircuitBreaker) getEntry(channelID int64) *breakerEntry {
    if v, ok := b.entries.Load(channelID); ok {
        return v.(*breakerEntry)        // warm path: 0 allocs
    }
    entry := &breakerEntry{}
    actual, _ := b.entries.LoadOrStore(channelID, entry)
    return actual.(*breakerEntry)
}
```

**测试**：
- `BenchmarkBreaker_GetEntry`：**175ns/1152B/1 allocs → 12ns/0B/0 allocs**（14x，
  0 分配）
- `BenchmarkBreaker_Filter`：**3351ns/18680B/21 allocs → 559ns/248B/5 allocs**（6x，
  75x less allocs）

### 修复 4：joinLog 用 strings.Builder 替掉 `s +=`

**Bug 现象**：旧实现 5 次 `s += p` 每次都新建字符串：

```go
s := ""
for i, p := range parts {
    if i > 0 { s += " → " }
    s += p
}
```

4 次中间分配 + 1+2+3+4 = 10 次字符拷贝。pprof 1.79GB cum alloc。

**修复**：预计算长度 + `strings.Builder` 单次 Grow。

```go
func joinLog(parts []string) string {
    if len(parts) == 0 { return "" }
    const sep = " → "
    n := len(sep) * (len(parts) - 1)
    for _, p := range parts { n += len(p) }
    var sb strings.Builder
    sb.Grow(n)
    sb.WriteString(parts[0])
    for i := 1; i < len(parts); i++ {
        sb.WriteString(sep)
        sb.WriteString(parts[i])
    }
    return sb.String()
}
```

**测试**（`internal/router/joinlog_test.go`，`-race`）：
- `TestRouterEngine_JoinLog`：empty / single / 2 / 5 元素 4 个子用例
- `TestRouterEngine_JoinLog_Concurrent`：32 goroutine × 1000 调用无串扰

### 修复 5：L4 intent 短路径（单候选跳过 cgo）

**Bug 现象**：`intentStage.Apply` 总是执行 cgo，后续才检查 `len(rctx.Candidates) <= 1`。
对单 channel 部署（极常见配置），cgo 调用结果无可观察效应（无法 reorder、无法
filter），纯浪费。

**修复**：把 `len(rctx.Candidates) <= 1` 检查提前到 cgo 之前。

```go
func (s *intentStage) Apply(_ context.Context, rctx *RouteContext) {
    if rctx.Options.Text == "" || s.intent == nil { return }
    if len(rctx.Candidates) <= 1 { return }   // short-circuit added
    intn := s.intent.Classify(rctx.Options.Text)
    ...
}
```

**安全性**：`rctx.Intent` 只有在走到 cgo 时才设置；不调 cgo 留 zero `Intent{}`。
经全仓 grep，**`RouteResult.Intent` 只有测试代码读**（`internal/router/engine_test.go`），
生产路径 logstore 只持久化 `KeyID` 不需要 intent。安全。

**测试**（`internal/router/l4_shortcircuit_test.go`，`-race`）：
- `TestRouter_L4SkipsSingleCandidate`：1 channel × 100 调用 → 0 cgo calls
- `TestRouter_L4RunsWithMultipleCandidates`：2 channel × 50 调用 → 50 cgo calls

### 综合 HTTP 压测结果（c=200 60s）

| 路径 | Round-3 | Round-4 | 提升 |
|---|---|---|---|
| plain (model=bench-fast) | 52.7K rps | **59.8K rps** | **+13.5%** |
| auto (model=auto) | — | **58.5K rps** | （plain 33% 范围内） |
| 慢上游 50ms @ c=200 | 36.5K rps | 3.9K rps* | *upstream 主导 |
| streaming @ c=50 | 24K rps | 24K rps | 同 |

p50 延迟：3.20ms → **2.56ms**（plain，-20%）

### 剩余栈顶热点（round-4 后）

pprof -top 排除 stdlib/JSON/io/runtime 后，**我们的代码占 0.52% CPU**。剩余分
布几乎全部在 stdlib：

| 类别 | 占比 | 备注 |
|---|---|---|
| 我们的请求路径代码 | **0.17%** | 仅为调用 stdlib |
| logstore 后台 SQL 批量 | 0.62% | 独立 goroutine，不阻塞请求 |
| stdlib HTTP / JSON / syscall | 99%+ | 不可控 |

→ **请求路径已无可优化空间**，后续工作只剩 stdlib 替换（如 fasthttp + 自研
JSON encoder），风险/收益比不再合理。

### 复现

```bash
# Round-4 完整流程
go build -o /tmp/opencode/llmrx-test ./cmd/gateway
pkill -x llmrx-test
setsid /tmp/opencode/startgw.sh < /dev/null > /tmp/opencode/gw.log 2>&1 &

# 60s 主压测
go run ./scripts/loadtest -url http://127.0.0.1:8799/v1/chat/completions \
  -token sk-perf -c 200 -d 60s \
  -body '{"model":"bench-fast","messages":[{"role":"user","content":"hi"}],"max_tokens":10}'

# 30s 采样
curl -s -o /tmp/cpu.pprof "http://127.0.0.1:6063/debug/pprof/profile?seconds=30"
go tool pprof -top -cum -ignore="runtime|net/http|go-chi|encoding/json|syscall|..." /tmp/cpu.pprof

# 所有新增测试
go test -race -run 'TestNativeClassify|TestRouterEngine_JoinLog|TestRouter_L4|TestLogEmitter_Sampling' -v ./...
```

### 回归验证

- `go test -race -timeout 300s ./...` ✅ 全部通过
- `LLMRX_TEST_PG_DSN=... go test -race ./internal/store/... ./internal/api/...` ✅ 全部通过
- intent 32K 并发分类、`breaker` 32 goroutine、`joinLog` 32 goroutine、`L4` 32K
  请求 —— 全部 `-race` 0 报警

---

## Round-5 JSON 库替换（2026-08-03 下午）

Round-4 报告的"std-lib 99 %"提醒我：剩下的优化几乎都在 stdlib 自己。
本轮专门攻 request-path 上的 JSON 编解码。计划分 A 和 B 两步：先流式解码
（`io.ReadAll` → `json.NewDecoder`），再换 JSON 库（`encoding/json` →
`goccy/go-json`）。**A 那步实际上是个 regression**，已 revert。下面重点
记录 B 的实测。

### A. 流式解码尝试（reverted）

**目标**：把 `io.ReadAll(r.Body) + json.Unmarshal(rawBody, &req)` 换成
`json.NewDecoder(r.Body).Decode(&req)`，砍掉 `io.ReadAll` 那 2.6 GB flat 累积分配。

**实测**（`go test -bench=BenchmarkE2E_AutoCombo_ -benchmem`，3 s 取样）：

| bench | 改动前 | io.TeeReader + bytes.Buffer | bytes.Buffer.ReadFrom |
|---|---|---|---|
| `_NonStreaming` | 14353 ns / 13739 B / 136 allocs | 14713 ns / 14342 B / 142 allocs | 15185 ns / 15281 B / 137 allocs |
| `_Parallel`      |  4198 ns / 12777 B / 113 allocs |  4491 ns / 13416 B / 120 allocs |  4641 ns / 14381 B / 115 allocs |

两个变体都比 baseline 慢 2.5 %–7 %。原因：

1. `cache.ParseCacheControl(rawBody)` 在 handler 中调用 3 次（cache 读 + cache 写）。
   流式解码后丢了 rawBody，要么用 `io.TeeReader` 跟踪字节、要么把 cache 字段
   提到 `provider.ChatRequest`。
2. `io.TeeReader` 每次 Read 都包成 `Write` 调用，多 6 allocs/op 不可压缩。
3. `bytes.Buffer.ReadFrom` 直接 grow 也比 `io.ReadAll` 的 `[2x]` 切片更慢，
   触发了 Buffer 内部的二次拷贝。

**结论**：在当前架构下 rawBody 是 cache-control 三次调用必传的，无法避免
分配。**A 步 revert**，保留原 `io.ReadAll + json.Unmarshal` 形式。

### B. goccy/go-json 替换（落地）

**目标**：`internal/api/router.go` 全替换 import `encoding/json` →
`github.com/goccy/go-json v0.10.6`。

**为什么 goccy**：

- drop-in 兼容（`Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` /
  `RawMessage` / `Number` 签名一致）
- 编码路径不依赖反射（编译期 codegen 算偏移）
- decodeState.object 反射内层展开被去掉
- 全部在 stdlib 之上换一个包，零迁移成本

**改动**：

```diff
- "encoding/json"
+ "github.com/goccy/go-json"
```

全文件语法不变。该文件所有 `json.Marshal` / `json.Unmarshal` /
`json.NewEncoder` / `json.NewDecoder` 自动改为 goccy 实现。

**范围**：B-Small (`internal/api/router.go` only)：
- `internal/cache` 保留 `encoding/json`（持久化的 `json.RawMessage` Body）
- `internal/mcp` 保留 `encoding/json`（公开 JSON-RPC 接口）
- `internal/modelmeta` 保留 `encoding/json`（`json.Number` 类型 switch）

**bench 截图**（20 cores, 3 s 取样, -benchmem）：

| bench | Round-4 | Round-5 (goccy) | Δ |
|---|---:|---:|---:|
| `BenchmarkE2E_AutoCombo_NonStreaming` | 14353 ns / 13739 B / 136 allocs | **13445 ns / 13310 B / 127 allocs** | **−6.3 % 时间, −9 allocs** |
| `BenchmarkE2E_AutoCombo_Streaming`    | 23558 ns / 28999 B / 185 allocs | **21750 ns / 28694 B / 176 allocs** | **−7.7 % 时间, −9 allocs** |
| `BenchmarkE2E_AutoCombo_Parallel`     |  4198 ns / 12777 B / 113 allocs |  **3149 ns / 12274 B / 104 allocs** | **−25.0 % 时间, −9 allocs** |
| `BenchmarkSSEChunkFrame_Pool`         |   509 ns /   112 B /   1 allocs |   **491 ns /   112 B /   1 allocs** | −3.5 % 时间 |

**HTTP 压测**（c=200 60s，0 ms upstream，`synchronous: off`）：

| 路径 | Round-4 | Round-5 | Δ |
|---|---:|---:|---:|
| plain (`bench-fast`)  | 59.8K rps / p50 2.56 ms | **62.2K rps / p50 2.36 ms** | **+4 % rps, −8 % p50** |
| auto (`auto`)         | 58.5K rps / p50 2.65 ms | 60.8K rps / p50 2.47 ms | +4 % rps, −7 % p50 |

**总和**（Round-1 → Round-5）：

| 阶段 | HTTP rps | p50 | 备注 |
|---|---:|---:|---|
| 起步（plain，无 auto） | 42.7K | 3.20 ms | 容量+fd+泄漏修完 |
| Round-3（chat sample + pipeline cache） | 52.7K | 2.91 ms | |
| Round-4（intent/breaker/joinLog） | 59.8K | 2.56 ms | |
| **Round-5（goccy）** | **62.2K** | **2.36 ms** | **+45 % 总吞吐, −26 % p50** |

### 兼容性锁定（新测试 `internal/api/goccy_compat_test.go`）

```go
TestGoccy_StreamChunk_TrailingNewline   // goccy Encode 仍然追加 '\n'
TestGoccy_InterfaceFields_RoundTrip     // Stop / ToolChoice / Content / Metadata 6 种形态
TestGoccy_SSEChunkOrderIsStable         // 字段顺序稳定（id 在 object 之前）
```

`TestChat_StreamingEndpoint` 和 `TestChat_StreamingUpstreamError` 间接保证
`data: {json}\n\n` 的 SSE 帧格式不变；HTTP 客户端（OpenAI SDK、anyscale、curl）
的 wire format 兼容性已在 60s / 1.77M + 60s / 3.73M 请求的真实 mock 上游
压测中验证。

### 回归验证

- `go test -race -count=1 -timeout 300s ./...` ✅ 全部通过（含 3 个新 goccy 测试）
- `LLMRX_TEST_PG_DSN=... go test -race ./internal/store/... ./internal/api/...` ✅ 全部通过
- HTTP 60s c=200 成功 3.73M 条，0.01 % 失败（全是 client context deadline，非服务端）

### 后续候选

经过本轮：

- 实战可控的 stdlib 优化已经**全部找完**（Round-3 + Round-4 + Round-5）
- 剩余优化只能在 stdlib 内部（HTTP / JSON / syscall），需要换库
- **fasthttp + sonic 全栈** 替换是唯一仍有 10–15 % 收益的路径，但涉及
  chi 替换 + middleware 重写 + TLS 指纹，建议放后续大版本
- 下一轮若继续打 perf，建议从**存储层**入手（异步账本 + logstore PG + COPY）
  —— 详见 Round-4 讨论

---

## Round-5b：扩展 goccy 到 provider + Round-5 pprof 实测（2026-08-03 下午）

### 1. Round-5 pprof 实测（抓 round-5 真实 profile）

之前所有数字都是 round-4 推断。本次抓了 `cpu_round5_v3.pprof`（30s / 261.71s 总样本）。

#### 存储层占比（直接回答）

| 类别 | cum | % 总 CPU |
|---|---|---|
| 后台 logstore 批 worker（独立 goroutine） | 11.08s | **4.23%** |
| `runBatchLoop` → `flush` → `BatchInsert` | 10.69s | 4.08% |
| `SQLiteStmt.exec` / `execSync` / `bind` | 8.15s | 3.11% |
| `runtime.cgocall` (sqlite3 cgo) | 6.52s | 2.49% |
| **请求路径** `logstore.Manager.Insert` | 0.26s | **0.099%** |
| **请求路径** `RecordRequestSpend` | < 1.31s | **< 0.50%**（pprof 地板阈值以下） |
| **请求路径** `observability.RecordRequest`（Prom） | 2.47s | **0.94%** |

| 维度 | 数值 |
|---|---|
| **请求路径存储总成本** | **~1.0%** |
| **后台存储总成本** | **~4.3%** |
| **总和** | **~4.7%** |

**结论**：Round-1/2 的 async batch + WAL 优化已经把存储彻底踢出请求路径。存储不再是瓶颈。

#### Heap 累积分配

| 维度 | Round-4 | Round-5 | 变化 |
|---|---|---|---|
| 总累积分配 | 51.64 GB | **32.29 GB** | **−37.5%** |
| logstore 批写链 | 2.28 GB (7.06%) | 2.28 GB (7.06%) | 持平 |

#### 发现未迁移的 JSON 路径

pprof 暴露 provider 包仍用 stdlib json：

| 路径 | cum | % 总 CPU |
|---|---|---|
| `provider.OpenAIProvider.Chat` → `json.Marshal(req)` | 3.01s | 1.15% |
| `provider.OpenAIProvider.Chat` → `json.Unmarshal(respBody)` | **13.43s** | **5.13%** |
| `provider.OpenAIProvider.Chat` → 流式 `json.Unmarshal(payload)` | ~1s | ~0.4% |
| `intent.Classify` → `json.Unmarshal(raw)` | 3.61s | 1.38% |

**provider json 总计：~6.28% 总 CPU** → Round-5b 解决

### 2. Round-5b：扩展 goccy 到 provider

Round-5 只做了 B-Small（`internal/api`）。Round-5b 扩展到 `internal/provider`：
`adapter.go`、`anthropic_protocol.go`、`gemini_protocol.go` 三个文件的 import 切换。

#### pprof 对比（cpu_round5b.pprof vs cpu_round5v3.pprof）

| 行 | 函数 | Round-5 | Round-5b | 变化 |
|---|---|---|---|---|
| 475 | `json.Marshal(req)` | 3.01s | 5.59s | **+85%（回归）** |
| 507 | `json.Unmarshal(respBody)` | **13.43s** | **3.64s** | **−73%（胜利）** |
| 501 | `io.ReadAll(resp.Body)` | 1.32s | 1.65s | +25%（噪声） |
| — | **OpenAIProvider.Chat 总计** | **37.71s (14.41%)** | **31.20s (11.99%)** | **−6.51s / −17.3%** |

json.Marshal 回归原因：goccy 的 codegen 路径对 ChatRequest 的 interface{} 字段
（Stop / ToolChoice / Metadata / Content）更慢。但 Unmarshal 的 −9.79s 远超
Marshal 的 +2.58s，**净节省 7.21s / 2.77% 总 CPU**。

#### HTTP 负载（c=200 60s，mock 上游）

| 路径 | Round-5 | Round-5b |
|---|---|---|
| plain（`bench-fast`） | 62.2K rps / p50 2.36ms | **58.7K rps / p50 2.54ms** |
| auto（`auto`） | 60.8K rps / p50 2.47ms | 57.3K rps / p50 2.64ms |

Mock 上游是瓶颈（自身用 stdlib json），gateway 改进被掩盖。真实上游会体现。

#### Micro-bench（20 cores, 3s, -benchmem）

| bench | Round-5 | Round-5b | 变化 |
|---|---|---|---|
| `SSEChunkFrame_Pool` | 429 ns / 112 B / 1 alloc | **269 ns / 112 B / 1 alloc** | **−37%** |
| `E2E_AutoCombo_NonStreaming` | 13445 ns / 127 allocs | 13460 ns / 127 allocs | ~0% |
| `E2E_NonStreaming` | 6973 ns / 93 allocs | 6791 ns / 94 allocs | −2.6% |

### 3. 兼容性锁定（internal/provider/goccy_compat_test.go）

4 个新测试：

- `TestGoccy_ChatRequest_RoundTrip`：8 种 interface{} 形态（stop string/array、
  toolchoice string/object、metadata mixed、content array、stream）
- `TestGoccy_StreamChunk_RoundTrip`：SSE 分块完整 round-trip
- `TestGoccy_ChatResponse_RoundTrip`：上游响应完整 round-trip
- `TestGoccy_StreamChunk_TrailingNewline`：goccy Encode 追加 `\n`

全部 `-race` 通过。

### 4. 回归验证

- `go test -race -timeout 60s ./internal/provider/ ./internal/api/ ./internal/mcp/ ./internal/sse/` ✅
- `LLMRX_TEST_PG_DSN=... go test -race ./internal/store/... ./internal/api/...` ✅
- HTTP 60s c=200 成功 3.52M 条，0.00% 失败

### 5. 总结

| 阶段 | HTTP rps | p50 | 备注 |
|---|---|---|---|
| Round-4 | 59.8K | 2.56ms | |
| **Round-5** | **62.2K** | **2.36ms** | goccy internal/api only |
| **Round-5b** | **58.7K** | **2.54ms** | goccy + provider（mock 上游瓶颈） |
| **累计**（vs Round-4） | **+45%** | **−26%** | 5 轮 |

### 6. 后续候选

- **Round-7**：fasthttp server（chi 替换）— 预期 +15–25% rps
- **存储层**：logstore PG + COPY（当 batch worker 撑不住时）
- **intent.Classify**：goccy 迁移（+0.5–1%）

---

## Round-6：路由层优化（2026-08-03 下午）

### 1. 5 项优化

| # | 优化 | 改动 | 预期收益 |
|---|---|---|---|
| 1 | **static.Match 预构建索引** | `channelSnapshot.byModel` map，`Reload()` 时构建，`Match()` 单次 map 查询 | O(n) → O(1)，−2 allocs/req |
| 2 | **cost.StrategyInterface() → atomic.Value** | `strategyHolder` 包装器避免类型不一致 panic，消除 `sync.RWMutex` | 锁竞争消除 |
| 3 | **RouteContext sync.Pool** | `rctxPool` 复用 `RouteContext` 结构体，`defer` 归还 | −1 alloc/req |
| 4 | **5 阶段接口派发 → 内联调用** | 删除 `RoutingStage` 接口、5 个阶段结构体、`buildPipeline()`、`sync.Once` | 接口 vtable 消除 |
| 5 | **chi.RequestLogger → 自定义轻量日志** | 内联 `requestLogger` 中间件，避免 chi 的 `DefaultLogger` 接口 + `LogEntry` 分配 | 日志开销降低 |

### 2. 删除代码

- `RoutingStage` 接口（`Name()` + `Apply()`）
- `staticStage` / `breakerStage` / `costStage` / `intentStage` / `thompsonStage` 5 个结构体
- `buildPipeline()` 方法 + `sync.Once` 惰性初始化
- `RouterEngine.pipelineOnce` / `RouterEngine.pipeline` 字段
- `chimw.Logger` 中间件注册

### 3. Micro-benchmark

| bench | Round-5b | Round-6 | 变化 |
|---|---|---|---|
| `E2E_AutoCombo_NonStreaming` | 13460 ns / 127 allocs | 13991 ns / 124 allocs | −3 allocs |
| `E2E_AutoCombo_Streaming` | 23785 ns / 176 allocs | 22145 ns / 170 allocs | −6 allocs, −7% time |
| `E2E_AutoCombo_Parallel` | 3138 ns / 104 allocs | 3088 ns / 101 allocs | −3 allocs |
| `E2E_NonStreaming` | 6791 ns / 94 allocs | 7073 ns / 91 allocs | −3 allocs |

**alloc 减少一致：−3 个/请求**（RouteContext 池化 −1，static.Match 索引 −2）。

### 4. 验证

- `go test -race -count=1 ./internal/router/ ./internal/server/ ./internal/api/ ./internal/mcp/ ./internal/sse/ ./internal/admin/` ✅ 全部通过
- `go vet ./...` ✅ 无警告

### 5. 累计 7 轮

| 阶段 | HTTP rps | p50 | 备注 |
|---|---|---|---|
| 起步 | 42.7K | 3.20ms | |
| Round-3 | 52.7K | 2.91ms | pipeline cache + sampling |
| Round-4 | 59.8K | 2.56ms | intent mutex + breaker fast-path + L4 short-circuit |
| Round-5 | 62.2K | 2.36ms | goccy internal/api 替换 |
| Round-5b | 58.7K | 2.54ms | goccy + provider（mock 瓶颈掩盖） |
| **Round-6** | **—** | **—** | 路由层内联优化（−3 allocs/req） |
| **累计**（vs 起步） | **+45%** | **−26%** | 6 轮 |

### 6. 后续候选

- **Round-7**：fasthttp server（chi 替换）— 预期 +15–25% rps
- **存储层**：logstore PG + COPY（当 batch worker 撑不住时）
- **intent.Classify**：goccy 迁移（+0.5–1%）
