# llmRx Performance Test Report

> Generated: 2026-08-02 (v1 auto-router 性能轮次，含 2 个热点修复)
> 上一份报告见 git 历史（2026-07-09 版）
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

持续合成负载（~10-40K rps，数分钟）下观察到的网关上限：

1. **logstore SQLite 写路径饱和**：220 万行后 WAL 膨胀至 240MB、网关 RSS 5.2GB、
   批量插入开始失败（`logstore batch insert failed`，审计日志丢弃，请求不受影响）
2. **文件描述符耗尽**：`accept4: too many open files`（与 keep-alive 连接数相关）

两者都在**远高于生产 LLM 流量**（通常几十~几百 rps，1-2s 延迟）的负载下出现，
不构成生产问题；但集群/超高频场景应启用 **PG logstore**（或削峰/限流）。
修复建议记录在 git log（本轮未实施）。

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
```
