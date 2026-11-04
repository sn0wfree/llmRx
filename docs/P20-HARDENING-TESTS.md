# P20 — 竞态压力、故障矩阵、双实现 diff 与资源极限

> Date: 2026-11-04 · Status: 实施中
> 前置：P18（Python 客户端）、P19（协议健壮性）。四个测试面共用现有
> harness（真实 socket + mock 上游），不引入 pytest 等依赖。

## 1. 竞态压力（`tests/test_race.py`）

P18 的两个大缺陷（in-flight 死锁、记账竞态）都是"运气好才抓到"。
本节把竞态变成**故意的**：

| # | 场景 | 预期 |
|---|---|---|
| R1 | 请求持续飞行时 `apply_bundle` 更换 servers | 零失败；切换后新请求打到新 server（快照语义） |
| R2 | 请求持续飞行时循环 `cache.purge()` | 零异常；命中/未命中计数守恒 |
| R3 | 请求持续飞行时并发写 `/ui/settings` | 全部 303，无 500 |
| R4 | 上传持续失败堆到 5000 上限 → 恢复上传 | 丢最旧的语义；恢复后缓冲清空；已落盘行仍在文件里 |
| R5 | 落盘失败（OSError）后恢复 | 行保持未标记、不丢，下个 tick 补写 |

## 2. 故障矩阵（`tests/test_failures.py`）

此前只测过"连不上"一种。现在按失败模式逐个钉死：

| # | 故障 | 预期 |
|---|---|---|
| F1 | DNS 解析失败（不存在的主机名） | 503 `client_upstream_unreachable`，usage status=0 |
| F2 | 三台 server 混合故障（挂/拒/活） | 故障转移到活的那台 |
| F3 | 上游 SSE 中途断连 | 客户端转发已收到的部分，不挂起 |
| F4 | 上游 200 + 二进制垃圾 body | 原样转发 |
| F5 | 上游 200 + 空 body | 透传 `Content-Length: 0` |
| F6 | 上游 304 | 原样透传（代理不跟随上游重定向） |
| F7 | 上游 302 | Location 头透传 |
| F8 | 落盘 OSError（模拟磁盘满） | 不崩溃，行仍在缓冲待重试 |
| F9 | 熔断半开窗口的并发探测 | 冷却期内全部 503 不拨号；冷却后恢复拨号 |

连接超时（SYN 黑洞）不测：客户端与 Go 一致**不设数据面超时**（SSE 需要
长读），黑洞地址会挂 2 分钟以上。已在文档记录为已知行为。

## 3. 双实现 diff（`tests/test_diff_go.py`）

防"Go 转维护后双实现漂移"的唯一手段。同一请求序列打到 **Go 客户端**
（本地 `go build` 的 `cmd/gateway-client`）与 **Python 客户端**，diff
可观测行为。

**对齐的断言**：状态码、错误 JSON 结构（解析后比较）、
`X-Llmrx-Cache` 出现与否、`/healthz` 键集合、usage 行字段集合与数值
（request_id 只比形状——nonce 设计不同）。

**有意差异白名单**（P18 §5，diff 时显式跳过）：流式 body 转发、熔断
计数（Go 2 次 vs Py 3 次）、`cache_enabled`/`max_inflight` 强制、
413 vs 截断转发、chunked 解码、Date/Server 头去重。

**跳过条件**：PATH 或 `/home/ll/.local/go/bin` 找不到 `go` 时整组
skip——套件在无 Go 机器上仍全绿。

## 4. 资源极限（`tests/test_limits.py`）

| # | 场景 | 预期 |
|---|---|---|
| L1 | 恰好 32 MiB 的透传 body | 上游收到全部字节 |
| L2 | 32 MiB + 1 字节 | 413 + 断连（P19 行为） |
| L3 | 缓存 2000 条满载 churn + TTL 过期 | 统计守恒，无异常 |
| L4 | 慢速连接（partial request，不发完） | 默认行为：线程阻塞但其它请求正常；**加 `timeout=120` 后慢连接被回收** |

L4 顺带修一个与 Go 的差异：Go 的 `http.Server` 有
`ReadHeaderTimeout: 10s`，Python 版此前**没有**——慢连接会永久占用
线程。修法：Handler 类属性 `timeout = 120`（BaseHTTPRequestHandler
的标准做法；放宽到 120s 是为了不误伤大 body 的慢上传）。

## 5. 非目标

- 不做真·kill -9 崩溃恢复测试（id 唯一性已有单测覆盖重启场景）
- 不测 SYN 黑洞超时（见 §2 说明）
- 不在 CI 里跑 Go diff（需 Go 工具链，本地跑）

## 6. 实施记录

（实施中回填。）
