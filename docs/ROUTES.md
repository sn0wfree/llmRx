# llmRx 路由与端点全景

> 生成方式：`DUMP_ROUTES=1 go test -run 'TestDump' -v ./internal/testhelper/`
> 遍历真实装配的 chi 路由树（非 grep 拼凑）。回归保护：
> `TestAdminPageRoutes_Contract`（旧页面路径必须 404、新路径必须可达）。
> 最后核对：2026-11-12 · v0.2.0+

## 0. 总览

| 面 | 前缀 | 路由数 | 鉴权 | 公网暴露（nginx 加固） |
|---|---|---|---|---|
| LLM API | `/v1` | 7 | Bearer key + RPM/TPM/预算 | ✅ 公开（限速 30r/s） |
| 用户自服务 API | `/api/v1` | 3 | 会话 cookie / 邀请码 | ✅ 公开（限速 10r/m） |
| 管理 JSON API | `/admin/api/v1` | ~66 | 会话 + RoleAdmin | ✅ 经 /admin/（登录 5r/m） |
| 管理控制台页面 | `/admin` | **102** | 会话 + RoleAdmin | ✅ 经 /admin/ |
| 账户自助页面 | `/account` | 4 | 会话 | ✅ 经 /account/ |
| 集群 API | `/nodes` | 7 | 内网中间件 + nginx 403 | ❌ 硬拒绝 |
| 边缘客户端上报 | `/client/v1` | 5 | 签名 Authorization | ⚠️ 仅启用边缘模式时挂载 |
| liveness | `/health` | 1 | 无 | ✅ 公开 |
| Prometheus | `/metrics` | 1 | 可选 Bearer | ❌ 独立端口（默认关闭） |

约定：`{id}` 为路径参数；`?query` 为可选过滤参数。

### 鉴权失败状态码（v0.3.0 起统一，RFC 7235）

| 状态码 | 含义 | 触发场景 |
|---|---|---|
| **401** | 认证失败——凭据缺失/格式错/未知/过期 | 无 `Authorization` 头、非 Bearer、未知 token、`token_expired`、`invalid_session`、BYOK 关闭/前缀不符/上游拒绝 key |
| **403** | 授权失败——凭据有效但权限不足 | `insufficient_role`、`permission_denied`、IP 未白名单（`byok_ip_blocked`）、BYOK IP 冲突 |
| 402 | 预算耗尽 | `budget_exceeded`（plan 预算）|
| 429 | 限流 | `rate_limited`（RPM/TPM）|

错误体统一为 `{"error":{"message","type","code"}}`（OpenAI 兼容）。
契约测试：`internal/middleware/auth_contract_test.go`。

## 1. LLM API — `/v1`（兼容 OpenAI）

```
POST /v1/chat/completions          聊天补全（SSE 流式 + 非流式）
POST /v1/embeddings                嵌入
POST /v1/images/generations        图像生成
POST /v1/audio/speech              语音合成
POST /v1/audio/transcriptions      语音转录
POST /v1/rerank                    重排序
GET  /v1/models                    可用模型列表
```

## 2. 用户自服务 API — `/api/v1`

```
POST /api/v1/register              邀请码注册（自动建 token+plan 并登录）
GET  /api/v1/me                    当前用户信息（会话 cookie）
POST /api/v1/me/redeem             兑换充值码
```

## 3. 管理 JSON API — `/admin/api/v1`

**认证 / 会话**
```
POST /login                        {username,password} → session_token
POST /logout
POST /users/{id}/password          {old_password,new_password}
```

**仪表盘 / 运行时**
```
GET  /dashboard                    总览
GET  /auto-router/state            L5 自路由决策状态
GET  /config                       运行时配置读
PUT  /config                       运行时配置写（热生效）
GET  /effective                    只读合成视图（YAML+DB 叠加）
POST /reload                       广播重载（PG NOTIFY）
POST /secrets/rotate               轮换 master key（重加密存量）
```

**渠道 / Key**
```
GET|POST /channels                 列表 / 新建
PUT|DELETE /channels/{id}          更新 / 删除（级联 keys）
GET  /channels/{id}/keys           key 列表
POST /channels/{id}/keys           添加 key
DELETE /channels/{id}/keys/{keyId} 删 key
GET  /channels/{id}/spend          累计花费
POST /channels/{id}/balance        手动刷新余额（P14）
GET  /channels/{id}/balances       余额历史
GET  /channels/{id}/balance-history 余额趋势（页面图表用）
POST /channels/{id}/fetch-models   拉取上游模型清单
GET  /balances                     全渠道余额总览
```

**Token / 组合模型**
```
GET|POST /tokens                   列表 / 新建（返回 sk- 明文一次）
PUT|DELETE /tokens/{id}            更新 / 删除
GET|POST /tokens/{id}/combos       组合列表 / 新建
PUT|DELETE /combos/{id}            组合更新 / 删除
```

**用户 / 计划 / 兑换码**
```
GET|POST /users；PUT|DELETE /users/{id}
GET|POST /plans；PUT|DELETE /plans/{id}
GET|POST /redeem-codes             兑换码管理
```

**用量与订阅（P21/P22）**
```
GET  /usage/overview?range=24h|7d|30d
GET  /usage/timeseries?range=24h|7d|30d
GET  /usage/plans                  下游 Plan 预算
GET  /subscriptions                厂商窗口报告
POST /subscriptions/{id}/refresh   立即探测
GET  /subscriptions/{id}/history?days=30
GET  /subscription-parsers         探针类型清单
```

**日志 / 分析 / 缓存 / 护栏 / MCP / 模型成本 / provider / 边缘客户端**
```
GET  /logs；GET /logs/stream (SSE)
GET  /analytics/timeseries | by-model | by-channel | by-token
GET  /cache/stats；POST /cache/purge
GET|POST /guardrails；PUT|DELETE /guardrails/{id}；GET /guardrails/events
GET|POST /mcp-servers；PUT /mcp-servers/{id}；DELETE /mcp-servers/{id}
POST /mcp-servers/{id}/refresh
GET|POST /mcp-servers/{id}/tools；PUT /mcp-servers/{id}/tools/{toolId}/pricing
GET|POST /model-costs；PUT /model-costs；POST /model-costs/import
GET|POST /providers
GET  /edge-clients；DELETE /edge-clients/{id}
POST /edge-clients/enroll-tokens；PUT /edge-clients/config
```

## 4. 管理控制台页面 — `/admin`（102 条）

GET 页面（部分列示，全表见 dump 命令）：

```
/login /register                   登录 / 注册页（公开）
/dashboard                         仪表盘
/channels /channels/new /channels/{id}/edit /channels/{id}/keys
/channels/partial/list（HTMX 局部刷新）/channels/{id}/balance-trend
/tokens /tokens/new /tokens/{id}/edit /tokens/help /tokens/partial/list
/tokens/{id}/combos /tokens/{id}/combos/new /tokens/{id}/combos/{cid}/edit
/plans /plans/new /plans/{id}/edit
/users /users/new /users/{id}/password
/user-usage /user-usage/{id} /user-usage/{id}/export.csv
/channel-monitor /subscription-quota /subscription-quota/{id}/history
/health /effective /config /analytics /logs /logs/stream (SSE)
/alerts /alerts/new /alerts/{id}/edit /alerts/events/{id}/ack
/guardrails /mcp-servers /model-costs /model-sets /providers /redeem-codes /clients
POST 动作与 GET 同域：/channels/{id} /channels/{id}/keys /channels/{id}/balance-refresh
  /channels/{id}/fetch-models /subscription-quota/{id}/refresh /guardrails/delete
  /model-costs/{model}/delete /clients/{id}/revoke /clients/enroll /clients/config
  /mcp-servers/{id}/oauth/* /users/{id}/password /logout ...
```

## 5. 账户自助页面 — `/account`

```
GET /account            个人中心
GET /account/usage      用量（用户维度，保持原名）
GET /account/topup      充值
GET /account/export.csv 用量导出
```

## 6. 集群 API — `/nodes`（仅多例模式，内网中间件 + nginx 403 双保险）

```
POST /nodes/register    节点注册
POST /nodes/heartbeat   心跳（含节点指标）
GET  /nodes             节点列表
GET  /nodes/leader      主节点信息
GET  /nodes/{id}        节点详情
GET  /nodes/{id}/health 节点健康
DELETE /nodes/{id}      摘除节点
```

## 7. 边缘客户端上报 — `/client/v1`（P15，启用边缘模式时挂载）

```
POST /client/v1/usage      用量回传
GET  /client/v1/healthz    上报端存活
POST /client/v1/heartbeat  客户端心跳
GET  /client/v1/config     配置下发（签名校验）
POST /client/v1/enroll     注册（enroll-token）
```

## 8. 特殊端点

```
GET  /health    liveness（含 version 字段；无鉴权）
/metrics     Prometheus —— 独立监听口（server.metrics_addr，默认关闭）
```

## 9. 已知的不一致（梳理发现，按影响排序）

1. **同功能双端点 ×6**：页面表单动作（无 JS 的 POST）与 JSON API 并行——
   登录/登出/改密/订阅刷新/余额刷新/fetch-models 各有页面路径与 API 路径。
   属有意设计（控制台无 JS 也可用），但此前无文档说明，本文即权威对照。
2. **页面辅助端点无版本**：`/admin/api/models-by-provider`、`/admin/api/meta-providers`、
   `/admin/api/available-models`、`/admin/api/preview-models` 挂在 `/admin/api/*`
   而非 `/admin/api/v1/*`——它们是页面下拉框的数据源，非对外契约。
3. **DELETE 动词不统一**：多数资源用 `DELETE /x/{id}`，但 guardrails 与
   model-costs 用 `POST .../delete`（表单无 JS 只能 POST 的历史约束）。
4. **组合模型双路径**：`GET /tokens/{id}/combos`（嵌套）与
   `PUT|DELETE /combos/{id}`（扁平）并存。
5. **api-reference.md 是精选子集**：本文件为全量清单；两文档冲突时以
   本文件 + 路由遍历测试为准。
