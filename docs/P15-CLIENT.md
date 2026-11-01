# P15 — 客户端形态 / Client Gateway

> Date: 2026-11-01 · Owner: llmRx maintainers · Status: **规划稿(未实施)**
> 前置:P0-P14;本文讨论"服务端 + 客户端"双形态,以及两者如何连接。
> 关联:[P12 集群模式](P12-CLUSTER.md)(注意:客户端 ≠ 集群节点,见 §2)、[DEPLOY-FRP.md](DEPLOY-FRP.md)。

## 1. 背景

llmRx 目前只有一种运行形态:**服务端网关**。它监听 `:8787`,对外提供
`/v1/*`(数据面)、`/admin/*`(管理台)、`/admin/api/v1`(管理 API)、
`/nodes/*`(集群内部 API)。部署方式覆盖裸机、Docker(13MB scratch 镜像)、
Helm、frp 公网暴露。

缺的是**客户端**:部署在用户本机 / 各地机房的边缘节点。它解决服务端形态
天然解决不了的四个问题:

| 问题 | 服务端形态的现状 | 客户端要做的 |
|---|---|---|
| 接入网络 | 用户必须能直连服务端(或走 frp) | 客户端在 `127.0.0.1` 提供服务,出站连服务端 |
| 断网/服务端故障 | Agent 直接不可用 | 本地缓存 + 本地直连上游兜底 |
| 就近接入 | 跨地域 RTT 高 | 边缘缓存命中直接本地返回 |
| 上线配置 | 改服务端 config.yml / 管理台 | 服务端统一下发,客户端零手工配置 |

定位:**边缘转发代理 + 桌面 GUI 合一**。同一个 Go 二进制,两种运行模式
(`headless` 用于 Docker/机房,`desktop` 用于个人电脑)。

## 2. 与 P12 集群模式的区别(先排除混淆)

| | P12 集群模式 | P15 客户端 |
|---|---|---|
| 角色 | 服务端**副本**,彼此对等 | 服务端**下游**,单向依赖 |
| 谁连谁 | 节点互连,nginx 统一入口 | 客户端**主动出站**连服务端 |
| 有无自己的 DB | 全部共享同一个 Postgres | 本地 SQLite(缓存/缓冲/离线) |
| 提供什么 | 完整 `/v1` + `/admin` | 本地 `/v1` + 小型本地 UI,**永不提供 `/admin`** |
| 信任级别 | 同构、可信 | 半可信(可能被终端用户控制) |
| 是否需要节点表 | 是(内存注册表 + nginx 重写) | 是(持久化 clients 表 + 管理台) |

两者共用"注册 + 心跳"的**模式**,但不共用代码——客户端是外部可信边界,
校验和限流强度远高于集群内部节点。

## 3. 目标与非目标

### 3.1 目标

1. **双二进制**:`llmrx`(服务端)与 `llmrx-client`(客户端)独立构建、
   独立发布;客户端强制 `CGO_ENABLED=0`,单文件跨平台;
2. 客户端 `headless` / `desktop` 双模式,Docker 与原生等价可部署;
3. 客户端在本地暴露 OpenAI 兼容 `/v1/*`,Agent 只需把 base_url 指向本机;
4. **主动出站**连接服务端,不需要任何入站端口、不需要 frp 每客户端一条隧道;
5. 四项能力:本地响应缓存、断网兜底、本地用量采集回传、统一配置下发;
6. 服务端零数据面改动(复用现有 `/v1/*`)。

> **为什么是双二进制而不是单二进制多模式**:客户端一旦 import
> `internal/store`(或依赖它的 `internal/router`),就会通过
> `store/sqlite.go` 的 `_ "github.com/mattn/go-sqlite3"` 把 **CGO** 链进
> 二进制,导致无法交叉编译、体积按服务端标准走。拆成两个二进制后,
> 客户端可以做到零 CGO、几 MB、一次交叉出全平台。详见 §8 的复用边界。

### 3.2 非目标

- 不做订阅账号复用(Claude/ChatGPT 订阅转 provider)——那是 Magpie 的场景,
  需要驱动各家官方 CLI,与 llmRx 的公网 API 网关定位不同;
- 不做 agent 配置文件改写(改 `~/.claude/settings.json` 等)——不属于本阶段;
- 不做 P2P 客户端互联;
- 客户端不提供管理后台,不能管理 channels/tokens(只读自己的配置)。

## 4. 关键设计决策

### D1 数据面:本地监听 + 出站转发,复用现有 `/v1/*`

客户端在 `127.0.0.1:3425`(避开 Magpie 惯例端口冲突可配)起一个
OpenAI 兼容服务。请求进来后,**出站**发到服务端 `https://…/v1/chat/completions`,
把服务端的响应(含 SSE 流)透传回本地 Agent。

**为什么不做隧道/代理协议**:Agent → 服务端本来就是 HTTP,客户端只需做一次
反向代理。不需要 CONNECT、不需要自定义帧协议、不需要服务端新增数据面端点。
现有 `/v1/*` 的鉴权、路由、限速、计费、日志全部原样生效。

代价:服务端看到的客户端 IP 是客户端自身的出口 IP。用 `X-Forwarded-For`
携带真实来源(服务端已支持解析该头,见 `api/router.go` 的 `clientIP`)。

### D2 控制面:心跳拉取(pull),MVP 不引入 WebSocket/SSE

客户端每 30s `POST /client/v1/heartbeat`,请求带自己当前的 `config_version`,
响应带服务端最新 `config_version` 与在线指令。版本不一致时客户端再
`GET /client/v1/config` 拉全量配置。

**为什么不用 WebSocket**:配置变更秒级下发不是硬需求(30s 内到达完全够用);
pull 模型零新依赖、可穿透任何代理、断线即重连无需会话状态、调试只看
curl 就够。项目目前只有 8 个直接依赖,不该为此加一个 WS 库。

**升级路径**:若后续需要秒级下发/远程诊断,在 `/client/v1/events` 上挂 SSE
(项目 `internal/sse` 和 broker 已有现成实现),pull 作为降级路径保留。

### D3 客户端身份:独立的 client credential,不复用用户 Token

沿用项目既有的两级信任思路(token ≠ admin ≠ 节点),新增第三类:

- 管理员在管理台生成**一次性接入码**(`enroll_token`,TTL 15 分钟);
- 客户端用接入码 `POST /client/v1/enroll`,换取 `client_id` + `client_secret`;
- 后续所有 `/client/*` 请求用 `X-Client-ID` + `X-Client-Secret`(HMAC 签名);
- 服务端 `clients` 表只存 `secret_hash`(Argon2id,复用 `internal/auth`);
- 客户端本地把 secret 以 mode 0600 存盘,复用 `internal/secrets` 的
  AES-256-GCM 加密(local master key 首次运行自动生成)。

### D4 配置下发:签名 bundle + 版本号,cannary 可选

服务端下发的是一个**裁剪过的**配置包,不是完整 config.yml:

```jsonc
{
  "version": 42,
  "issued_at": "2026-11-01T10:00:00Z",
  "signature": "hmac-sha256:...",        // 用 client_secret 签,防 MITM 注入
  "servers": ["https://llmrx.frp.tokenhub.top"],
  "cache":   { "enabled": true, "ttl_sec": 3600, "max_items": 5000 },
  "fallback":{ "enabled": true, "mode": "cache_then_local" },
  "telemetry":{ "usage_batch_sec": 60 },
  "limits":  { "max_inflight": 64 }
}
```

客户端**校验签名失败即拒绝应用**并回退到上一版。配置包不含上游 key
(默认 key 永远在服务端),如果启用本地兜底直连(§9),本地 key 由管理员
在客户端侧单独配置或由 bundle 中的加密字段下发(见 D6)。

### D5 计费不重复:用量记录带 `source`,只有 `local` 参与计费

服务端在代理请求时已经计费并写了 `logs` 行。客户端回传的用量若再次计费
就是双重扣费。规则:

| `source` | 含义 | 服务端处理 |
|---|---|---|
| `proxy` | 经服务端转发(服务端已计费) | 只入库做边缘可观测,`billable=false` |
| `cache` | 客户端缓存命中,零 token | 入库,`billable=false` |
| `local` | 兜底直连上游,服务端从未见过 | **入库并计费**,记到该客户端绑定的 plan/token |

服务端用 `(client_id, request_id)` 唯一索引做幂等去重,客户端重传不会重复入账。

### D6 断网兜底:默认只缓存兜底,本地直连需显式开启

兜底链条(服务端不可达时,按序):

1. **缓存命中** → 直接返回(`source=cache`);
2. **本地上游直连**(仅 `fallback.mode=local` 且配了本地 key)→ 直连上游
   (`source=local`,回传计费);
3. 都不可用 → `503` + 结构化错误 `client_upstream_unreachable`,提示检查服务端。

为什么默认不开本地 key:key 落到用户机器上是**合规和安全的重大降级**。
默认 `cache_then_fail`,本地直连是运营者显式选择的运维模式。

### D7 桌面 GUI:本地 Web UI + 浏览器,不做原生 GUI

项目画像很明确:纯 Go、无 npm、`go:embed` 模板、单二进制 13MB。引入
Wails/Tauri/Fyne 会破坏这条主线。

方案:客户端 `desktop` 模式启动时
1. 起本地 HTTP 服务 `127.0.0.1:3425/ui`;
2. 自动打开默认浏览器;
3. 复用项目已有的 `html/template + HTMX + Tailwind(CDN)` 渲染方式,
   模板放独立目录 `internal/client/templates/`(不碰服务端 webui);
4. 可选托盘图标(systray,CGO)作为后续 polish,不作为 MVP 阻塞项。

先例:Syncthing / Jupyter / Grafana 都是"本地服务 + 浏览器"的桌面形态。

### D8 Docker 优先,原生二进制等价

对齐现有 docker 化方案:

```
Dockerfile.client          FROM scratch,复用同一套静态链接/CGO 约定
docker-compose.client.yml  client 容器 + 挂载本地缓存卷
Makefile                   docker-build-client / docker-run-client
```

`headless` 模式镜像 ≈ 13MB,环境变量即可完成接入(`LLMRX_SERVER`,
`LLMRX_ENROLL_TOKEN`)。桌面模式用原生二进制(goreleaser 产物),Docker
桌面上跑 GUI 没意义。

## 5. 架构总览

```
┌──────────────┐
│ Agent        │  Claude Code / Cursor / OpenAI SDK …
│ (任意客户端) │  base_url = http://127.0.0.1:3425/v1
└──────┬───────┘
       │ HTTP (本地回环)
       ▼
┌──────────────────────────────────────────────┐
│ llmRx Client (单二进制)                       │
│                                              │
│  ┌─────────────┐   ┌──────────────┐          │
│  │ 本地 /v1/*  │──▶│ 转发器        │──出站──▶ │
│  │ (OpenAI 兼容)│   │ proxy/hybrid │   HTTPS  │
│  └──────┬──────┘   └──────┬───────┘          │
│         │                 │                  │
│         ▼                 ▼                  │
│  ┌─────────────┐   ┌──────────────┐          │
│  │ 响应缓存     │   │ 本地用法缓冲  │          │
│  │ (SQLite)    │   │ (SQLite)     │          │
│  └─────────────┘   └──────────────┘          │
│                                              │
│  ┌─────────────┐   ┌──────────────┐          │
│  │ 控制面客户端 │   │ 本地 Web UI  │          │
│  │ 心跳/配置    │   │ (desktop)    │          │
│  └──────┬──────┘   └──────────────┘          │
└─────────┼────────────────────────────────────┘
          │ 出站心跳 / 用量批次 / 拉配置
          ▼
┌──────────────────────────────────────────────┐
│ llmRx Server                                 │
│                                              │
│  /v1/*            数据面(完全复用,零改动)     │
│  /client/v1/*     控制面(NEW)                 │
│  /admin/*         管理台(+ Clients 页面)      │
│  SQLite / Postgres                           │
└──────────────────────────────────────────────┘
```

## 6. 两条通道

### 6.1 数据面(复用,零新增)

| 项 | 说明 |
|---|---|
| 路径 | 客户端 `/{v1}` → 服务端 `https://server/v1/...` |
| 鉴权 | 客户端本地必须配置一个 llmRx **用户 token**(前缀 `sk-`),原样透传 |
| 流式 | SSE 直通,`io.Copy` + 每 chunk `Flush`,沿用服务端 `stream_caps` |
| 客户端 IP | 加 `X-Forwarded-For: <agent真实IP>`(服务端已支持) |
| 超时 | 客户端侧 `stream_timeout_sec` 应 ≥ 服务端,避免提前截断 |
| 用量 | 客户端本地记一条 `source=proxy`,**不参与计费**,仅观测 |

唯一需要新增的是一个**可配置的 server 列表 + 失败转移**(多服务端轮询)。

### 6.2 控制面(新增 `/client/v1/*`)

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/client/v1/enroll` | 一次性接入码换 client_id/secret |
| POST | `/client/v1/heartbeat` | 30s 一次,上报状态 + 取回 `config_version` |
| GET | `/client/v1/config` | 拉取签名配置 bundle(`?since=42` 可 304) |
| POST | `/client/v1/usage` | 批量回传用量,按 `request_id` 幂等 |
| POST | `/client/v1/deregister` | 主动下线(卸载时) |
| GET | `/client/v1/self` | 客户端自查(限流/配额/版本要求) |

鉴权中间件:独立于 `admin` 和 `token` 的第三条链路,校验
`X-Client-ID` + HMAC 签名 + 时间窗(±5min,防重放)+ 每分钟限流。
路由挂在服务端 `server.go`,**不经过** `/nodes/*` 的内网限制(客户端在公网)。

## 7. 服务端改动

### 7.1 数据表

```sql
CREATE TABLE clients (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    client_id       TEXT    NOT NULL UNIQUE,   -- 公开标识 "c_7f3a..."
    name            TEXT    NOT NULL,
    secret_hash     TEXT    NOT NULL,          -- Argon2id
    status          INTEGER NOT NULL DEFAULT 1,-- 1=enabled 2=disabled
    plan_id         INTEGER,                   -- local 模式计费用的计划
    config_version  INTEGER NOT NULL DEFAULT 1,
    last_seen       TIMESTAMP,
    last_version    TEXT,                      -- 客户端软件版本
    labels          TEXT    NOT NULL DEFAULT '{}', -- JSON: {"site":"bj"}
    created_at      TIMESTAMP NOT NULL,
    FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE SET NULL
);

CREATE TABLE client_enroll_tokens (
    token       TEXT PRIMARY KEY,   -- 一次性
    expires_at  TIMESTAMP NOT NULL,
    created_by  INTEGER,
    used_by     TEXT                -- 消费后写 client_id
);

-- 用量幂等
CREATE TABLE client_usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    client_id     TEXT NOT NULL,
    request_id    TEXT NOT NULL,
    ts            TIMESTAMP NOT NULL,
    model         TEXT,
    status        INTEGER,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    cost_usd      REAL DEFAULT 0,
    source        TEXT NOT NULL,        -- proxy | local | cache
    billable      INTEGER NOT NULL DEFAULT 0,
    latency_ms    INTEGER DEFAULT 0,
    UNIQUE (client_id, request_id)
);

-- logs 表加一列(可空),让客户端流量在现有日志 UI 里可筛
ALTER TABLE logs ADD COLUMN client_id TEXT;
```

迁移走现有 `s.addColumn()` 懒迁移模式;Postgres 双实现。

### 7.2 服务端代码

```
NEW
internal/clientapi/handler.go      /client/v1/* 端点 + 鉴权中间件
internal/clientapi/store.go        clients / enroll / usage 仓储(接口)
internal/clientapi/config.go       bundle 组装 + HMAC 签名
internal/clientapi/handler_test.go
internal/webui/clients.go          管理台 Clients 页面(列表/接入码/禁用)

MODIFY
internal/server/server.go          挂 /client/v1 路由 + 注入
internal/store/{sqlite,postgres}.go 三张表 + 迁移
internal/store/store.go            接口签名
cmd/gateway/main.go                客户端相关的周期任务(过期接入码清理)
internal/webui/handler.go          路由 + menu
```

管理台 Clients 页:列表(client_id / 名称 / 站点标签 / 在线状态 / 版本 /
最近心跳 / 缓存命中率 / local 用量)、生成接入码、禁用、查看配置版本、
强制推送新配置(递增 `config_version`)。

## 8. 客户端内部结构

```
cmd/gateway-client/main.go        入口(headless/desktop 二选一)
internal/client/
├── app.go            生命周期:装配 + 优雅退出
├── config.go         本地配置(server 列表、token、listen 地址)
├── enroll.go         接入 + secret 落盘(加密)
├── control.go        心跳循环 / 配置拉取 / 签名校验 / 版本切换
├── proxy.go          数据面:出站转发 + SSE 直通 + 失败转移
├── cache.go          本地缓存(复用 internal/cache 接口 + SQLite 后端)
├── fallback.go       兜底链:cache → local → 503
├── upstream.go       本地直连(复用 internal/provider + internal/router)
├── usage.go          本地用量缓冲 + 批量上传 + 重试
├── webui/            desktop 模式的本地 UI(模板 + handler)
└── templates/        HTML(Tailwind CDN + HTMX,与服务端同风格)
```

**复用清单**(这是本方案成本可控的关键):

| 复用 | 来源 | 用途 | CGO |
|---|---|---|---|
| `internal/cache` | P8 | 缓存接口 + `Key()` 规范化;本地后端优先 `MemoryCache`,持久化可选独立的纯 Go sqlite | 无 |
| `internal/provider` | P7+ | 本地直连兜底时的协议适配 | 无 |
| `internal/secrets` | P7+ | 本地 secret 加密 | 无 |
| `internal/logging` | P0 | 结构化日志 | 无 |
| `internal/sse` | P6 | 桌面 UI 的实时状态(可选) | 无 |
| `internal/webui` 的渲染方式 | P2 | `html/template + HTMX + CDN`,不复制框架 | 无 |

**明确不复用**(重要):

| 不复用 | 原因 |
|---|---|
| `internal/router` | 全 L1-L5 引擎、Thompson、熔断,客户端本地兜底只需"按配置挑一个 channel";且它 import `internal/store` |
| `internal/store` | `store/sqlite.go` 含 `_ "github.com/mattn/go-sqlite3"` 驱动注册,引用即把 **CGO** 链进二进制,交叉编译全平台失败 |
| 服务端 `webui` | 客户端不提供管理台,只做自己的本地状态页 |

本地兜底的选路逻辑(约 40 行)在 `client/upstream.go` 内自实现:
遍历配置里的 channel → 检查模型匹配与启用状态 → 取第一个可用 key 明文
→ 调 `provider.Chat`。不需要 store、不需要路由引擎。

这个边界让**客户端可以 `CGO_ENABLED=0` 编译**,一次交叉出全平台、
体积从十几 MB 降到几 MB——这是双二进制方案的核心理由。

## 9. 断网兜底细节

```
请求 → 服务端可达?
  ├─ 是 → 转发(source=proxy) → 响应写回
  └─ 否(连接失败 / 超时 / 5xx)
       ├─ cache.enabled 且命中 → source=cache
       ├─ fallback.mode=local 且有本地 key
       │    → 本地 router+provider 直连(source=local,回传计费)
       └─ 否则 → 503 client_upstream_unreachable
                  {"error":{"code":"client_upstream_unreachable",
                            "message":"llmRx server unreachable and no local fallback"}}
```

健康判定:连接错误 / `>=500` / 首字节超时。**不**把 `429`、`4xx` 当服务端故障
(那是业务语义,必须原样返回给 Agent)。

**熔断**:连续 N 次失败进入 open 状态 30s,期间直接走兜底链,避免每个请求
都等超时。半开时放一个探测请求。

## 10. 本地缓存

- 只对**非流式、确定性请求**缓存:`temperature == 0` 且无 `stream`、
  无 `cache_control: no-store`(复用 `internal/cache.Key()` 的既有判定);
- 后端:**默认 `cache.MemoryCache`**(零 CGO、零文件);需要跨进程持久化时
  才启用磁盘后端,且必须用**纯 Go** 的 `modernc.org/sqlite`
  (不能用 `mattn/go-sqlite3`,否则客户端变成 CGO 依赖);
- key 用 `internal/cache.Key(req)`,与**服务端缓存同构**,便于"服务端缓存
  purge 时同步下发 purge 指令"(bundle 里带 `purge_before` 时间戳);
- 命中率写进心跳,管理台可见。

## 11. 用量采集与回传

1. 每个请求结束(含流式结算后)写一条本地 `pending_usage` 行;
2. 每 `usage_batch_sec`(默认 60s)或攒满 200 条,`POST /client/v1/usage`;
3. 失败退避重试(1s/5s/30s),本地保留 7 天,超期丢弃并计数告警;
4. 服务端按 `(client_id, request_id)` 去重;`source=local` 才落 spend
   (`IncrementTokenSpend` / `IncrementPlanSpend` 复用现有原子更新);
5. 回传成功标记 `uploaded_at`,本地定期清理。

**时钟**:客户端时钟不可信。服务端以**接收时间**为准记录 `received_at`,
`ts` 仅作参考;批次超过 24h 的 `local` 记录拒绝计费并告警(防篡改)。

## 12. 桌面 GUI

| 页面 | 内容 |
|---|---|
| 状态 | 服务端连通性(绿/黄/红)、当前 server、延迟、配置版本 |
| 缓存 | 命中率、条目数、占用体积、清空按钮 |
| 用量 | 今日请求/成本(proxy/local/cache 分列)、最近 50 条 |
| 设置 | server 列表、listen 地址、token、缓存开关、本地兜底开关 |
| 关于 | 版本、client_id、重新接入、复制 base_url |

交互全部 HTMX 局部刷新,单页即可,不做多标签管理台。
`desktop` 模式自动打开浏览器;`--no-browser` 可禁用(适合开机自启)。

## 13. 部署

### 13.1 headless(Docker,机房/边缘)

```yaml
# docker-compose.client.yml
services:
  llmrx-client:
    image: ghcr.io/sn0wfree/llmrx-client:latest
    environment:
      LLMRX_MODE: headless
      LLMRX_SERVER: https://llmrx.frp.tokenhub.top
      LLMRX_ENROLL_TOKEN: ${LLMRX_ENROLL_TOKEN}
      LLMRX_LISTEN: 0.0.0.0:3425
      LLMRX_TOKEN: ${LLMRX_USER_TOKEN}   # 数据面透传的用户 token
    volumes:
      - client-data:/data
volumes: { client-data: }
```

### 13.2 desktop(个人电脑)

```
llmrx-client --mode desktop
# → 打开 http://127.0.0.1:3425/ui,首次引导接入
```

### 13.3 与 frp 的关系(澄清:不是替代关系)

两者**方向相反**,解决不同问题,不可互相替代:

| 问题 | 方向 | 谁解决 |
|---|---|---|
| 没有公网 IP 的服务端如何被访问(浏览器开 `/admin/`、第三方云服务调 `/v1/`) | **入站** | frp(或任何反向隧道) |
| Agent 如何方便地接入、断网时如何不中断 | **出站** | 客户端 |

关键点:**客户端自己仍要连到服务端**,而服务端当前唯一的公网入口就是
frp 隧道。因此典型链路是

```
Agent → 本机客户端 ──出站──> https://<frp 域名> → frps → 内网 nginx → llmRx
```

即客户端**换掉了终点(从公网 URL 换成 127.0.0.1),没有绕过 frp**。
只有当客户端与服务端处于同一内网(可直连 nginx:8080)时才不需要 frp。

客户端真正带来的收益不是"去掉 frp",而是:

1. Agent 侧不再需要持有公网地址和 API key(配置收成 `http://127.0.0.1:3425/v1`);
2. 断网/服务端抖动时有缓存兜底;
3. **可收窄 frp 暴露面**:公网只暴露 `/client/v1/*`(控制面)与受严格限制的
   `/admin/`,`/v1/*` 不再对外——即使域名泄露也拿不到推理入口。

若目标是从根本上不需要 frp,则需要一个有公网 IP 的中转节点承接客户端出站,
那本质上就是重新实现一个 frp(或 WireGuard/Tailscale),不建议在本项目内自建。

## 14. 安全

| 面 | 措施 |
|---|---|
| 接入 | 一次性接入码 + TTL(15min);换出的 secret 只在生成时可见一次 |
| 传输 | 强制 HTTPS;`--insecure` 仅 dev |
| 配置完整性 | bundle HMAC-SHA256 签名,校验失败拒绝应用 |
| 重放 | 控制面请求带 `X-Timestamp` + `X-Nonce`,服务端 ±5min 窗口 + nonce 表 |
| 客户端存储 | secret 用 AES-256-GCM 加密,master key 首次运行生成,mode 0600 |
| 本地直连 key | 默认不允许;开启后 key 只存客户端,不上传、不进日志 |
| 越权 | `/client/*` 独立中间件,**不能**访问 `/admin/*`、`/v1/*` 以外的管理面 |
| 滥用 | 每客户端 QPS 上限、批量回传条数上限、body 上限 |

**明确记录的风险**:客户端机器是半可信的。若开启本地直连,上游 key 暴露在
终端上;`source=local` 的用量由客户端自报,存在少报空间——缓解是靠
服务端侧的 `client_usage` 与上游真实账单定期对账(与 P14 余额监控天然互补)。

## 15. 可观测性

- 服务端 Prometheus 新增:`llmrx_clients_online`,
  `llmrx_client_usage_total{source}`, `llmrx_client_last_seen_seconds`;
- 管理台 Dashboard 增加"边缘节点"卡片:在线数、聚合 QPS、缓存命中率;
- 告警复用 P14 思路:新增 `client_offline` 类型(4 处小改,同 P14 §16)。

## 16. 测试

| 层 | 覆盖 |
|---|---|
| `internal/clientapi` | 接入码一次性/TTL/重复使用;HMAC 校验、重放拒绝;bundle 签名;usage 幂等去重与 `source` 计费规则 |
| `internal/client/proxy` | SSE 直通、失败转移、`X-Forwarded-For`、`4xx` 不触发兜底 |
| `internal/client/fallback` | cache→local→503 三级链;熔断开合;半开探测 |
| `internal/client/cache` | TTL/LRU、非确定性请求不缓存、purge、`CGO_ENABLED=0` 可编译 |
| `internal/client/usage` | 批次边界、重试退避、超期丢弃 |
| `tests/scenarios` | 端到端:mock server + 客户端 + mock 上游,在线/断网/断网+本地 key 三条路径 |
| Web UI | 管理台 Clients 页 CRUD;客户端本地 UI 渲染 |
| 竞态 | `go test -race` 全绿;覆盖率沿用 CI 门槛 |
| 构建边界 | **强约束**:`go build ./cmd/gateway-client` 在 `CGO_ENABLED=0`
  下必须成功;CI 加一条 `GOOS/GOARCH` 矩阵构建检查,防止有人误引
  `internal/store` / `internal/router` 把 CGO 拖进来 |

## 17. 分期实施

| 期 | 内容 | 估算 | 可独立验证 |
|---|---|---|---|
| **P15.1** | 服务端:三张表 + `/client/v1/{enroll,heartbeat,config}` + 管理台 Clients 页 | ~700 LoC | 用 curl 完成接入+心跳 |
| **P15.2** | 客户端骨架:单二进制、本地 `/v1` 纯转发、接入、心跳、优雅退出 | ~600 LoC | Agent 指向客户端可正常对话 |
| **P15.3** | 配置下发:bundle 组装/签名/校验/版本切换 | ~350 LoC | 管理台改配置 → 客户端 30s 内生效 |
| **P15.4** | 本地缓存(SQLite 后端 + purge 下发) | ~450 LoC | 断网重复请求命中缓存 |
| **P15.5** | 断网兜底 + 本地直连(provider/router 复用) | ~500 LoC | 杀掉服务端仍能服务 |
| **P15.6** | 用量采集与回传(幂等 + 计费规则) | ~450 LoC | `source=local` 正确入账、proxy 不重复计费 |
| **P15.7** | 桌面 GUI(本地 Web UI) | ~500 LoC | `--mode desktop` 浏览器可用 |
| **P15.8** | 打包(Dockerfile.client / compose / goreleaser / CI) | ~250 LoC | `docker run` 一条命令接入 |

累计 ≈ **3800 LoC**,8 个可独立验收的小 PR。P15.1-P15.2 完成即形成最小可用闭环。

## 18. 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 客户端成为新单点(Agent 只指向它) | Agent 全部不可用 | 客户端极轻、进程托管/开机自启;`503` 返回可操作错误 |
| `source=local` 自报用量可篡改 | 少计费 | 与上游账单对账(P14);异常批次拒绝 |
| 本地直连泄露上游 key | 合规风险 | 默认关闭;开启需管理员显式操作;key 不上传不落日志 |
| 配置 push 误操作(推坏配置) | 客户端全量故障 | 版本号递增 + 客户端保留上一版可回滚;先 canary(按 label 试点) |
| 多服务端轮询导致用量散落 | 观测不完整 | 用量记录带 `server` 字段;以 client_id 聚合 |
| 控制面与数据面共用服务端,互相影响 | 数据面抖动 | `/client/*` 独立限流与并发上限;不占数据面 inflight 配额 |
| 客户端版本碎片化 | 兼容成本 | heartbeat 上报版本;管理台按版本筛选;bundle 带 `min_version` |

## 19. 待拍板问题

1. **本地直连兜底的 key 来源**:管理员在客户端单独配?还是由服务端 bundle
   加密下发?前者简单、后者运维统一但扩大服务端信任面。建议 **一期只做
   客户端本地配置**,bundle 下发留二期。
2. **客户端是否允许服务端反向拉取本地诊断**(日志片段/pprof)?建议不做,
   避免服务端主动连客户端、破坏"纯出站"模型。
3. **多服务端轮询策略**:按顺序(主备)还是按延迟(择优)?建议一期主备,
   二期按心跳延迟择优。
4. **`/v1/models` 语义**:客户端返回服务端列表缓存,还是本地静态?建议
   缓存服务端上一次响应,`/models` 不是热路径。
