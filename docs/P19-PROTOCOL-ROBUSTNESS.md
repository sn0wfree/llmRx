# P19 — 协议健壮性：边界输入一致性测试与修复

> Date: 2026-11-04 · Status: 实施中
> 前置：P18（Python 客户端）。本文合并两件事：**修复 3 个已确认的协议
> 分形缺陷**，以及建立一套**恶意/边界输入一致性测试**（conformance
> suite）把它们和同类问题钉死。

## 1. 背景与已知缺陷

复查确认 `clients/python/src/llmrx_client/proxy.py` 的 `_read_body` /
`_forward_raw` 存在 3 个真实缺陷。它们与 P18 §8 第 5 条同源——都是从
Go 客户端行为"忠实复刻"时继承的 HTTP 语义粗糙处，但 Go 的
`io.ReadAll(r.Body)` 会透明处理其中两种，所以 Python 版实际比 Go 更脆。

### 缺陷 1：`Transfer-Encoding: chunked` 请求体被静默丢弃

`_read_body` 只读 `Content-Length`。HTTP/1.1 客户端（httpx 对可迭代
body、部分 SDK、某些代理链）会分块发送——body 变成 `b""`，上游收到
空请求。Go 版 `io.ReadAll` 自动解码 chunked，无此问题。

**修复**：`_read_body` 识别 `Transfer-Encoding: chunked`，逐块解码
（size 行 → data → CRLF，0 块后消费 trailer），总量超 `MAX_BODY` 判
溢出。

### 缺陷 2：`Content-Length > 32MiB` 导致 keep-alive 错位

现状 `min(length, MAX_BODY)` 截断读取，剩余字节留在 socket。连接若
keep-alive，**下一帧从 body 中段开始解析**——数据串话、不可预测的
解析错误。Go 版同样截断（同款隐患），但 Go 服务器默认读尽 body。

**修复**：拒绝而非截断。`CL > MAX_BODY` → `413` + `Connection: close`
（不读 body，直接断连，socket 语义干净）。这是与 Go 的有意差异：
截断转发会让上游解析一个损坏的请求，对调用者毫无价值。

### 缺陷 3：带 body 的 `DELETE` 丢失 body

转发集合只有 `("POST", "PUT", "PATCH")`。RFC 7231 允许 DELETE 携带
body（elasticsearch 等真实 API 在用）。

**修复**：集合加入 `DELETE`。

## 2. 一致性测试（conformance suite）

新增 `tests/test_conformance.py`，全部走真实 socket（复用 harness），
覆盖**任何 HTTP 客户端都可能发出的合法或恶意输入**。每条断言同时
检查：状态码、响应可解析、连接语义（keep-alive 下后续请求不受污染）。

| # | 输入 | 期望 |
|---|---|---|
| C1 | chunked body 的 POST chat | 上游收到**完整解码后**的 body，响应 200 |
| C2 | chunked body 的透传 POST | 同上 |
| C3 | `CL: MAX_BODY+1` | 413，`Connection: close`，连接不复用 |
| C4 | `CL: MAX_BODY+1` 后同连接再发正常请求 | 不可能（连接已断）——验证 close |
| C5 | 带 body 的 DELETE | 上游收到完整 body |
| C6 | 畸形 JSON（截断、二进制、空 body） | 原样转发，上游的权威错误透传 |
| C7 | 消息含 emoji / CJK / 控制字符 | 缓存 key 稳定、命中路径正常 |
| C8 | 超长 header 值（8KB）、非常规 header 名 | 转发不崩溃 |
| C9 | query 里塞 CRLF 注入尝试 | 原样字节转发（http.client 拒绝非法字符则上游侧验证） |
| C10 | HTTP/1.0 客户端（无 keep-alive） | 响应正确、连接关闭 |
| C11 | `Expect: 100-continue` | 正常处理（BaseHTTPRequestHandler 默认） |
| C12 | 同连接串行 10 个混合请求 | 全部正确（keep-alive 分形完整性） |
| C13 | `/ui/settings` 跨源 POST（恶意 Origin） | 拒绝（见 §3 CSRF） |
| C14 | `/ui/cache/purge` 跨源 POST | 拒绝 |

## 3. CSRF：本地 UI 的 Origin 校验

客户端 UI 监听 127.0.0.1，无 cookie、无会话——但浏览器的同源策略
**不阻止**跨站页面向 localhost POST 表单（响应不可读，但请求会到达）。
恶意网页可以静默改写用户的客户端配置（servers 指向攻击者、清缓存）。

**修复**：写操作（`/ui/settings`、`/ui/cache/purge` 的 POST）校验
`Origin` / `Referer` 头——存在时必须是本机来源（host 为
`127.0.0.1`/`localhost` 且端口匹配）；**缺失时放行**（curl、SSH 隧道、
非浏览器调用不带这些头，P18 §"薄壳"的用法不受影响）。这是本地服务
的标准做法（VS Code、Jupyter 同思路）。

## 4. 非目标

- 不实现 HTTP/2、websocket（客户端定位是本地 HTTP/1.1 代理）
- 不做通用 fuzzing 框架（conformance 用例手工枚举边界，可复现、可读）
- Go 客户端同步修复（转维护模式；缺陷在 P18 §9.5 与本文记录）

## 5. 验收

1. `test_conformance.py` 全绿（C1–C14）
2. 既有 92 个单测无回归
3. E2E（16 项）复跑通过
4. 缺陷 1–3 各有回归测试，修复不靠"看起来对"

## 6. 实施记录

（实施中回填。）
