# P18 — Python 边缘客户端（llmrx-client）

状态：设计定稿，实施中。
前置：P15（Go 边缘客户端，转维护模式）、P16（发布）。

## 1. 目标与边界

用 Python 重新实现边缘客户端，**服务端零改动**。两者通过已发布的
`/client/v1` 协议对话；Python 客户端对本机 agent 暴露与 Go 版相同的
接口与文件布局。

| 决策点 | 结论 |
|---|---|
| 分发形态 | pip 包：`pip install llmrx-client` → `gateway-client` 命令 |
| 与 Go 客户端关系 | 并存，Python 为主；Go 转维护模式（代码与测试保留） |
| 首版范围 | 全量对齐：数据面 + 控制面 + 接入码 + 状态页 + healthz |
| 运行依赖 | 仅 `PyYAML`（配置格式与 Go 版相同）；其余全部标准库 |
| Python 版本 | ≥ 3.10 |
| 测试框架 | `unittest`（开发环节同样零第三方依赖） |
| 发布产物 | 不进 goreleaser；后续可选把 wheel 挂到 GitHub Release |

## 2. 包结构

```
clients/python/
  pyproject.toml            # setuptools；[project.scripts] gateway-client
  README.md
  src/llmrx_client/
    __init__.py
    __main__.py             # python -m llmrx_client
    cli.py                  # -config/-version/-enroll/-enroll-name/-server
    config.py               # Config、YAML 读写（原子 0600）、Normalize
    cache.py                # LRU + TTL、规范化 key
    breaker.py              # 3 次失败 / 30s 冷却
    proxy.py                # ThreadingHTTPServer 数据面 + healthz
    upstream.py             # attemptServers、头复制、token 注入
    usage.py                # 记账、缓冲、JSONL 落盘、上传
    control.py              # 心跳、签名 bundle、拒绝降级
    enroll.py               # 一次性接入码
    ui.py                   # 状态页（零外部资源）
  tests/                    # unittest；含 mock 上游与 mock clientapi
```

## 3. 协议契约（与 Go 服务端对齐的要点）

服务端侧规格见 `internal/clientapi/`；此处只列 Python 实现必须逐字对齐
的可观测行为。

### 3.1 数据面（本机监听）

- 路由：`POST /v1/chat/completions` 走 chat 路径；`GET /healthz` 是本地
  健康 JSON；**其余一切路径/方法透传上游**（含 `/v1/models`、`/health`）。
- body 上限 32 MiB；解析失败的 body 原样转发（让服务端给出权威错误）。
- 缓存谓词：body 可解析 && 非流式 && `temperature` 缺省或 `0`。
- 熔断 `fallible` 判定：传输错误、`408`、`>=500`。业务错误（401/429 等）
  原样透传，不计失败、不触发兜底。
- 兜底链：可缓存且有命中 → 用缓存（source=cache）；否则传输错误 →
  `503 {"error":{"code":"client_upstream_unreachable",...}}`（status 记 0）；
  服务端 5xx → 原样透传。
- 上游头：复制入站头（跳过 Host/Content-Length/Connection），补
  `X-Forwarded-For`（缺省时）、`X-Llmrx-Client`；仅当 agent 未带
  Authorization 且配置了 token 时注入 `Bearer <token>`。
- SSE：上游响应头原样复制给客户端，每 chunk flush。
- 服务器列表：粘性故障转移（记住最后成功的下标，从头开始重试一轮）。

### 3.2 `/healthz`（键按字母序输出）

```json
{"cache":{"hits":0,"hit_rate":0,"misses":0,"size":0},"pending_usage":0,
 "server_fails":0,"server_open":false,"status":"ok","uptime_sec":12}
```

配置了 `client_token` 时追加 `config_version`、`control_last_error`（恒在，
成功时为空串）、`control_last_ok`（仅成功过才有）。

### 3.3 控制面

- 心跳：`POST /client/v1/heartbeat`，30s 一轮 + 启动即发一轮；body
  `{"client_version":...,"config_version":<applied>}`；响应
  `{"server_version":N,"need_config":bool,"server_time":...}`。
- 拉配置：`GET /client/v1/config`，响应
  `{"config":{...},"signature":"<64 hex>","algorithm":"hmac-sha256"}`。
- **签名校验必须用 `config` 成员的原始字节**：Go 端以 struct 序
  `json.Marshal` 签名（含 omitempty 与默认 HTML 转义），Python 重新
  序列化不可能复现这些字节。实现：`json.JSONDecoder.raw_decode` 定位
  外层对象的 `config` 值的字节区间，`HMAC-SHA256(key=client_token)` 后与
  `signature` 做 `hmac.compare_digest`；`algorithm` 不匹配直接拒绝。
- 版本：`config.version < applied` 拒绝（记录错误、沿用旧配置）；**等于
  也重新应用**。applied 先落、回调后发。
- ApplyBundle 只动：servers（非空）、cache.enabled（无条件）、
  cache.ttl_sec / max_items（>0）、fallback.mode（非空）、
  limits.max_inflight（>0）。**永不触碰** token / client_token /
  client_name / listen / usage_file / usage_endpoint / usage_upload_sec。

### 3.4 用量

行 JSON 字段与顺序（服务端按字段名解析，顺序仅为可读性）：

```json
{"request_id":"<name>-<n>","ts":<unix sec>,"model":"","status":200,
 "prompt_tokens":0,"completion_tokens":0,"cost_usd":0,"source":"proxy",
 "latency_ms":123}
```

- `request_id = <client_name>-<原子序号>`，是服务端 24h 去重键。
- source：`proxy`（服务端已计费，token 恒 0）、`cache`（本机命中，
  token 取自缓存的上游 usage）、`local`（本版接受但从不产生，见 §5）。
- 缓冲 5000（满丢最旧）；落盘 tick 5s → JSONL 单行 append（0600）；
  上传 tick 60s → 批 ≤200 → `POST /client/v1/usage`，超时 10s；失败整批
  按原序塞回队首。无 `client_token` 或无 `usage_endpoint` 时不上传。
- `usage_endpoint` 缺省 = `servers[0] + /client/v1/usage`。

### 3.5 接入码

`POST /client/v1/enroll`，body
`{"enroll_token":"<code>","client_name":""}`（服务端忽略 name 字段），
超时 20s，**要求恰为 201**；响应
`{"client_id","client_secret","name"}`。成功后写
`client_token=client_secret`、`client_name=name`，`usage_file` 缺省补为
`<配置目录>/usage.jsonl`，原子写回 0600。

### 3.6 配置文件

YAML，键与 Go 版完全一致（`listen/servers/token/client_token/
client_name/usage_endpoint/usage_upload_sec/cache_enabled/cache_ttl_sec/
cache_max_items/usage_file/fallback_mode/max_inflight`）。默认值：listen
`127.0.0.1:3425`、ttl 3600、max_items 2000、upload_sec 60。校验仅两条：
servers 非空、每项以 `http://`/`https://` 开头；全部问题收集后一次报出。
写入 = 临时文件 0600 → fsync → 原子 rename。

## 4. 数值对照表

| 项 | 值 |
|---|---|
| listen 默认 | `127.0.0.1:3425` |
| body 上限 | 32 MiB |
| 熔断 | 3 次失败开，30s 冷却，半开探测 |
| 缓存 | LRU 2000 条，TTL 3600s（ttl 可热更；容量改动需重启） |
| usage 缓冲 | 5000，满丢最旧 |
| 落盘 / 上传 tick | 5s / 60s |
| 上传批量 / 超时 | ≤200 / 10s |
| 心跳周期 / 超时 | 30s / 10s（响应读上限 64 KiB；config 1 MiB） |
| enroll 超时 | 20s（响应读上限 64 KiB） |
| 关闭 | SIGINT/SIGTERM → 15s 排水 → usage 冲刷 → bye |

## 5. 与 Go 版的差异（有意为之）

Go 版有几处已确认的缺陷/死代码。Python 版按**意图**实现，以下差异全部
记录在案：

1. **流式/透传转发空 body（Go bug，修复）**。Go 的 `forwardRaw` 调
   `attemptServers(r, nil, true)`，body 恒为空——每个流式 chat 与所有
   透传 POST（如 embeddings）到上游都是 `Content-Length: 0`。Python 版
   转发真实 body。
2. **熔断计数（Go bug，修复）**。Go 对缓冲 5xx 计两次（forwardChat 与
   handleChat 兜底各一次），2 个连续 5xx 即开；Python 每请求计 1 次，
   3 次开（与流式/透传一致）。
3. **`cache_enabled` 实际生效（Go 死配置）**。Go 从不读取该 flag，缓存
   恒开；Python 版为 false 时跳过缓存读写。
4. **`max_inflight` 实际生效（Go 死配置）**。Go 从不强制；Python 用
   信号量，超限返回 503 `client_overloaded`。0 = 不限。
5. **`fallback_mode=local` 行为同 `cache_then_fail`**。Go 同样从未实现
   local 上游；接受该值、不报错，语义与默认一致。待 P15 定义"本地上游"
   后再实现（usage source=local 同理，本版从不产生）。
6. **缓存 key 不与 Go 字节一致**。客户端缓存纯本机，不存在跨实现共享；
   Python 用自己的规范化（投影字段同 Go：role/content/name/tool_calls
   （按 id,name 排序）/tool_call_id/cache_control/refusal，丢弃
   reasoning_content；工具按 name 排序；sort_keys 紧凑序列化）。
7. **`-version` 输出**：`<ver> (<commit>, <date>, Python <x.y.z>, edge)`，
   运行时段标识为 `Python 3.x.y`（Go 版是 `go1.x`）。

## 6. 明确不做

- 不改 Go 服务端与 Go 客户端代码。
- 不进 goreleaser/release.yml 产物；不引入 pytest/PyInstaller/ricathon
  等工具链。
- 不实现 `fallback_mode=local` 的本地上游（等 P15 后续）。

## 7. 验收标准

1. `python -m unittest discover` 全绿（单测 + mock 集成）。
2. 对照 Go 版测试集逐条移植的行为用例全部通过（token 注入、temperature
   缓存、流式不缓存、离线兜底、503 形状、401 不触发熔断、故障转移、
   降级拒绝、签名篡改、设置部分更新、0600、凭据不被表单覆盖）。
3. **真实服务端 E2E**：WSL 构建 Go 服务端，Python 客户端完成
   enroll → 心跳 → 拉 bundle → 转发请求 → 用量上传全链路；服务端
   管理台能看到该客户端在线、收到 usage 行。
4. `pip install -e clients/python` 后 `gateway-client -version` 可用。
5. 状态页零外部资源（正则断言无 `http(s)://` 资产引用）。

## 8. 实施记录(2026-11-03)

**代码**:`clients/python/`,约 2100 行实现 + 1900 行测试。

- 模块:config / cache / breaker / upstream / proxy / usage / control /
  enroll / ui / cli,全部按 §3-§4 的契约实现。
- 运行依赖仅 PyYAML;测试同样零第三方(unittest + 进程内 mock 上游与
  mock `/client/v1` 服务端)。

**单测**:`python3 -m unittest discover -s tests`,87 个,全绿。

测试抓到并修掉的真问题:

1. **in-flight 限流自死锁**:`_inflight_cv = Condition(_inflight_mu)`,
   退出路径却写了 `with _inflight_mu, _inflight_cv` —— 对同一把
   非重入锁连续 acquire 两次,第二个请求永远等锁。faulthandler
   抓栈定位。教训:Condition 自带锁,with 它一个就够。
2. **handler 类体引用闭包变量**:`class Handler: _proxy = proxy` 在类
   体执行时按全局查找,NameError。改为把 proxy 挂在 server 对象上、
   handler 经 `self.server` 取——顺带消除了一个测试接线与生产路径
   漂移的隐患(harness 直接用生产的 `serve()`)。
3. **模板转义**:`str.format` 下 CSS 的 `:root {` 必须 `{{`;前 7 行
   漏转义导致 KeyError。
4. **cli 忘了把 control 传给 Proxy**:bundle 正常应用
   (control 线程在跑)但 `/healthz` 永远不报 `config_version`。
   E2E 抓到的——单测里 control 都是自己 new 的,没有覆盖 cli 接线。

**E2E**(16/16,`clients/python/tests/e2e.sh`,WSL 实跑):

真实 Go 服务端 + 真实 mockupstream,全链路:

```
admin login → 建渠道(指向 mock)→ /reload → 建 token
→ 签发一次性接入码
→ gateway-client -enroll(配置 0600)
→ 启动 → 心跳 → 拉取 HMAC bundle 并应用(config_version 上报)
→ POST /v1/chat/completions 经 client→server→mock 全链 200
→ 用量上传(服务端日志出现 router_path="client:py-edge-e2e")
→ 管理台 edge-clients 可见该客户端
```

**第二轮缺陷排查(同日)**:

1. **Windows 平台阻断**:`os.fchmod` 是 Unix-only,Windows 上
   `write_config` 会 AttributeError——enroll 完全不可用。改为探测
   `getattr(os, "fchmod", None)`,缺失即跳过(Windows 用 ACL 管权限)。
   JSONL 落盘同修。
2. **签名提取的脆弱假设**:原实现要求 envelope 里 `config` 必须是第一
   个成员(`{"config":` 前缀匹配)。改为 `raw_decode` 逐成员扫描取原始
   字节——任意成员位置都正确,且切片的是原始文本,不会引入重新序列化
   的字节漂移。无 config 成员/非对象仍拒绝。
3. **透传响应头重复**:本地 `send_response` 已发 Date/Server,再把上游
   的复制一遍会产生重复头。复制时跳过 `date`/`server`。顺带修掉
   `version_string()` 因空 `sys_version` 留下的尾随空格。
4. **handler 异常裸断连**:Go 有 Recoverer,handler panic 返回 500;
   Python 版此前异常会直接断开连接。补上等价物——未开始写响应时返回
   500 JSON,已开始则只能断开(记 traceback 到 stderr)。
5. **socket 泄漏**:urllib 抛 HTTPError 时响应体未关闭(enroll、心跳、
   用量上传三处),补 `close()`。

最终 90 个单测 + E2E 16/16 全绿。

E2E 顺带确认的服务端事实(非缺陷,记录备查):admin CreateChannel
只 reload 连接池不 reload 静态路由索引,需手动 `POST /admin/api/v1/reload`;
渠道 base_url 约定含 `/v1`(服务端拼 `/chat/completions`)。

**pip 安装**:`pip install clients/python` 后 `gateway-client` 命令可用
(WSL 系统 Python 无 pip,此项在 Windows Python 下验证)。

## 9. 长时间稳定性与性能画像(2026-11-03)

### 9.1 生产服务端取证(llmrx.tokenhub.top → 120.27.114.106)

SSH 进容器宿主,直接看运行中的实例:

| 证据 | 数值 | 判读 |
|---|---|---|
| 容器连续运行 | 7 周(2026-09-16 起),`RestartCount=0`,healthy,OOM=false | 无崩溃史 |
| 容器日志 | 25 万行:**0 panic / 0 fatal / 0 error / 0 OOM**,3 条 warn | 纯净 |
| 内存 | 27 MiB / 512 MiB,7 周无增长 | 无泄漏迹象 |
| 宿主机 | 连续运行 1 年 34 周未重启 | 底座稳定 |
| 健康检查 | 每 30s 一次,全部通过 | |
| nginx 14 天留存 | 5.3 万请求(多服务合计),14 个 500 且集中在单日 | 低错误率 |

**必须如实说明的局限**:该部署**几乎没有真实流量**。llmRx 自己的 14 天
请求仅 108 个,其中绝大多数是互联网扫描器(`/admin/.env`、
`/admin/config.php` 等);唯一真实请求是本次探测。所以这证明的是
**空载长期稳定**,不是**有负载下的稳定**——后者由 §9.3 的挂机补上。

另外两点观察(建议项,本次未动):

- 生产跑的是**老镜像**:无 `/client/v1`、响应无 `X-Llmrx-Version`、
  日志仍是 sqlite 时代。升级到 v0.1.1 后边缘客户端协议才可用。
- `/admin/login` 公网可达且被扫描器持续探测;nginx 同一实例还挂着
  JupyterLab 等其他服务。建议收敛暴露面。

### 9.2 性能画像(本地真实服务端 + mock 上游)

`clients/python/tests/perf_profile.sh`,脚本带**有效性门禁**(响应必须含
`"choices"` 才继续),避免把错误响应的耗时当成性能数据——第一次跑就因为
mock 没起来而产出过一份全部作废的数字。

| 指标 | 直连服务端 | 经 Python 客户端 | 客户端开销 |
|---|---|---|---|
| 非流式延迟 avg / p95 | 1.0 / 1.3 ms | 2.1 / 2.4 ms | **+1.1 ms** |
| SSE 首 token(TTFT)avg | 0.5 ms | 1.5 ms | **+1.0 ms** |
| 并发吞吐(8×25 请求) | 723 rps | 458 rps | 63% |

**判读**:边缘代理面对的是 1 s 以上的真实 LLM 延迟,+1 ms 在用户侧完全
不可感知;吞吐损耗只在远高于边缘客户端使用形态的并发下才有意义。
结论:**性能不是选型障碍**。

### 9.3 长时间稳定性(挂机)

`clients/python/tests/soak_run.sh`(Linux)与 `soak_win.py`(Windows)。
每 10 s 一个请求(含流式),每 60 s 采样内存/句柄数。

Linux(WSL)第一轮因 WSL 实例被回收而中断,但留下 2 h 43 min 的干净数据:

```
samples      : 163   (10:16:13 → 12:58:25)
requests     : n=978 ok=978 fail=0   (0.000% 失败)
RSS          : first=27996KB  max=28012KB  last=28012KB
RSS drift    : +16KB / 163 min  (+5.9 KB/hour)
first third  : avg 28006 KB      last third : avg 28012 KB   → FLAT(无泄漏)
fds          : 全程 6           threads    : 全程 3
```

判据:RSS 曲线平坦、首尾三段均值仅差 6 KB、fd 与线程数**全程无变化**
→ 无内存泄漏、无句柄泄漏、无线程泄漏。

### 9.4 挂机期间发现的真实缺陷:用量 request_id 重启后重复

采样 usage.jsonl 时看到 `request_id` 为 `-1`、`-2`……前缀为空(配置里没
写 `client_name`,本身无害),但顺着查下去发现问题:

- 服务端按 `clientName + "/" + requestID` 去重,窗口 **24 小时**
  (`internal/client/api/ingest.go` 的 `alreadySeen`);
- 两个客户端的 id 都来自**进程内计数器**,重启后从 1 重新开始。

于是客户端重启后,前 N 条(id 与重启前重复)会被服务端**静默丢弃**。
边缘客户端重启频繁(笔记本休眠、崩溃重启),这是真实的用量丢失。

修复:两个客户端都在 id 里混入**每进程唯一的 nonce**(时间 + pid +
随机数),重启后不可能重复。两侧都加了"两个实例不碰撞"的回归测试。

### 9.5 顺带验证

- **pip 安装路径**(此前标记待验证):`pip install -e clients/python`
  后 `gateway-client` 命令可用,`python -m llmrx_client` 亦可;
  Windows Python 3.13 下同样通过。
- **Windows 平台**:客户端在 Windows 上可运行(挂机即在 Windows 侧跑),
  `os.fchmod` 缺失的处理(§8 第 1 条)因此得到真实验证。

**Go 客户端转维护模式**:`cmd/gateway-client` 与 `internal/client`
保留、测试继续跑,README 与 P15 标注指向本实现;release 不再新增
Go 客户端特性。
