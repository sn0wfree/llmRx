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

### 6.1 覆盖度梳理与补充单测（2026-11-05）

逐模块梳理 131 个单测未触达的路径，发现一个**真实启动缺陷**和一批
未覆盖分支：

**缺陷 6：`listen` 缺端口时启动崩溃。** `make_server` 用
`rpartition(":")` 解析 `listen`——`listen: 127.0.0.1`（只写主机）时
`port='127.0.0.1'`，`int()` 抛 ValueError 且无人捕获，客户端启动即
裸 traceback。Go 版会给出 "missing port in address" 的清晰错误。
修复：`normalize()` 校验 listen 必须是 `host:port` 且端口为数字
（缺失冒号、端口非法都进 ConfigError，与其它校验一样一次报出）；
`make_server` 兜底抛带说明的错误。

**补测清单**（新增 5 个测试文件 + 扩充 6 个既有文件，约 40 用例）：

| 模块 | 此前未覆盖 | 补测 |
|---|---|---|
| `upstream.py` | `remote_ip`（空值/无端口）、`copy_request_headers`（XFF 已存在不覆盖、token 让位、Host/CL/Connection 跳过、多值收敛）、`attempt_servers` 空 servers、粘性下标 | `test_upstream.py` |
| `config.py` | `from_dict` 类型防御（bool 冒充 int、servers 非列表）、非映射 YAML、`fallback_mode_or_default`、`usage_url` 尾斜杠、**listen 校验** | `test_config.py` 扩充 |
| `usage.py` | `valid_source`、`requeue_front` 超限截断、300–399 上传状态、cost_usd 浮点 | `test_usage.py` 扩充 |
| `control.py` | envelope 成员间空白、心跳空 body、响应超上限 | `test_control.py` 扩充 |
| `enroll.py` | 201 + 畸形 JSON、响应超 64KiB、401 body 不可解析 | `test_enroll.py` 扩充 |
| `proxy.py` | `fallible` 表、`_temp_zero` 字符串温度、HEAD 无 body、listen 解析、错误常量形状 | `test_proxy.py` 扩充 |
| `cli.py` | `build_version`、`-version` 主路径、`run_enroll` 缺 server、enroll 写配置+usage 默认、enroll 失败 exit 1 | `test_cli.py`（新） |
| `ui.py` | `_same_origin` 纯函数矩阵、`dispatch` 路由、POST /ui 405、`_html_escape` | `test_ui.py` 扩充 |

## 6. 实施记录（2026-11-04）

四个套件全部落地：`test_race.py`（R1–R5）、`test_failures.py`（F1–F9）、
`test_diff_go.py`、`test_limits.py`（L1–L4）。全套 131 个单测 + E2E 16 项全绿。

**修了一个真实差异**：Python 版此前没有对应 Go `http.Server`
`ReadHeaderTimeout: 10s` 的机制——慢连接（打开后不发数据）会永久占用
线程。补 `BaseHTTPRequestHandler.timeout = 120`（放宽以免误伤大 body
慢上传），L4 用真实 slowloris socket 验证：超时后服务端关闭连接，
期间其它请求不受影响。

**测试自身的三个坑**（记录避免重踩）：

1. bash `$(( HOURS * 3600 ))` 不能乘浮点——`0.033h` 会让循环一次都
   不跑（见 `soak_remote.sh` 改整数分钟的提交）；
2. L2 用 http.client 发 33MB 时服务端会先回 413 断连，客户端要捕获
   `BrokenPipeError` 后再 `getresponse()` 读回 413；
3. F9 的关键断言是**透传路径**被 `allow()` 门控（缓冲 chat 按设计
   恒拨号，Go 对齐），写错路径会测出与实现相反的"失败"。

**diff 测试的断言面**：状态码、错误 JSON 键集合、`X-Llmrx-Cache`
标记、healthz 键集合、usage 行字段集与 source/status 序列。request_id
只比字段集（nonce 设计不同）。这是双实现漂移的唯一自动化防线。

