# 真实数据测试操作指南（P21 / P22）

面向「用真实上游账号验证渠道用量监控与订阅探针」的场景。按顺序执行，每步都有**预期结果**与**失败排查**。

- 适用范围：本地或内网的 llmRx 实例；OpenRouter 官方接口 + 智谱 GLM Coding Plan 用量接口
- 预计耗时：20–40 分钟（其中大部分是等真实流量产生）
- 风险提示：**Cookie / API Key 只写进你的实例，不要贴到聊天窗口或 issue 里**

---

## 第 0 步：准备清单

| 项 | 说明 |
|---|---|
| llmRx 实例 | 已在运行，能打开 `/admin/`（版本需包含 P21+P22） |
| `LLMRX_KEY_MASTER` | 环境变量，64 位 hex。**没有它无法保存探针 Cookie** |
| 至少一个真实渠道 | 已有 API Key 的 OpenRouter 或智谱 coding plan 渠道 |
| curl | 用于手工验证厂商接口 |

确认版本：

```bash
curl -s http://127.0.0.1:8787/health
# 期望包含 "status":"ok"；管理页应为新版（有「监控 → 用量监控 / 订阅用量」两个入口）
```

---

## 第 1 步：先手工问厂商要一次数据（重要）

**先别在 llmRx 里配探针**。手工跑一次 curl，确认两件事：
① 这个 key 有权访问该接口；② 响应里有哪些字段（llmRx 的解析器要靠字段名识别窗口）。

### 1.1 OpenRouter

```bash
curl -s https://openrouter.ai/api/v1/key \
  -H "Authorization: Bearer $OPENROUTER_KEY" | head -c 2000
```

**预期**：`{"data":{"label":"...","limit":...,"limit_remaining":...,"usage":...,"usage_daily":...,"budgets":[...]}}`

llmRx 会把这些映射为窗口：

| 响应字段 | 窗口 | 单位 |
|---|---|---|
| `limit` + `limit_remaining` + `usage` | Key 限额 | USD |
| `budgets[].reset_interval=daily/weekly/monthly` | 每日/每周/每月 | USD |
| `usage_daily/weekly/monthly`（无对应 budget 时） | 仅用量窗口 | USD |
| `free_model_daily_requests` | 每日（scope=free_models） | 次数 |

### 1.2 智谱 GLM Coding Plan

已确认的真实接口（来自智谱仪表盘自身的 JS bundle）：

```
GET https://bigmodel.cn/api/paas/v4/monitor/usage/quota/limit
GET https://bigmodel.cn/api/paas/v4/monitor/credit-usage/activity
GET https://bigmodel.cn/api/paas/v4/monitor/token-usage/activity
GET https://bigmodel.cn/api/paas/v4/monitor/token-usage/usage-detail
```

**认证方式：`Authorization: Bearer <token>`**（Cookie 单独使用会被拒：接口返回「Header中未收到Authorization参数」）。

```bash
curl -s 'https://bigmodel.cn/api/paas/v4/monitor/usage/quota/limit' \
  -H "Authorization: Bearer $ZHIPU_KEY" \
  -H 'Accept: application/json' \
  -H 'Referer: https://bigmodel.cn/coding-plan/personal/usage' | head -c 2000
```

**预期**：JSON，含额度/已用/重置时间相关字段。

> **如果 401**：
> - 错误是「Header中未收到Authorization参数」→ 你没带 `-H "Authorization: Bearer ..."`
> - 错误是「令牌已过期或验证不正确」→ key 不对或没有该套餐权限。换 coding plan 专用 key（控制台 `/apikey/coding-plan-per`）
>
> **如果 200 但字段看不懂**：把响应贴给我（或自己对照第 3 步的字段族清单），我据此调整解析器。

> **想看浏览器真实请求**：打开 https://bigmodel.cn/coding-plan/personal/usage → F12 → Network → 勾 Fetch/XHR → 刷新 → 找 `usage`/`quota` 开头的请求 → 右键 Copy as cURL。这一步能直接给出带参数的完整 URL。

---

## 第 2 步：配置渠道

### 2.1 OpenRouter 渠道

1. 打开 `/admin/channels`，编辑目标渠道
2. **「源头用量探针（P22）」** 区域：
   - 探针类型：`OpenRouter（官方 API）`
   - 其它字段留空
3. 保存

或用 API：

```bash
curl -X PUT http://127.0.0.1:8787/admin/api/v1/channels/42 \
  -H "X-Session-Token: $ADMIN_SESSION" \
  -H 'Content-Type: application/json' \
  -d '{"usage_probe":{"kind":"openrouter_key"}}'
```

### 2.2 智谱 GLM Coding Plan 渠道

同上，探针类型选 `GLM Coding Plan（实验性）`：

| 字段 | 填什么 |
|---|---|
| 探针类型 | `GLM Coding Plan（实验性）` |
| 接口地址覆盖 | **留空**（用内置默认 `/monitor/usage/quota/limit`）；若第 1 步 curl 出的 URL 不同，粘贴那个完整 URL |
| 会话 Cookie | **留空**（默认用渠道 API Key 认证） |
| 检查间隔 | 先填 `300`（5 分钟，便于快速验证；稳定后改回 0 用全局 1h） |

> 若你的 key 访问不了该接口、必须用浏览器会话，在「会话 Cookie」里粘贴浏览器 DevTools → Network → 任意请求 → Request Headers → `Cookie` 整行的值。llmRx 会用 AES-256-GCM 加密存储，**永不回显**；填空白框表示不修改已存值。

---

## 第 3 步：立即刷新并核对

在 `/admin/subscriptions` 页面：

1. 找到该渠道的卡片 → 点 **「立即刷新」**
2. 期望看到「厂商权威」徽章 + 至少一个窗口行（已用 / 上限 / 剩余 / 进度 / 重置）

用 API 核对原始数据（推荐，便于比对）：

```bash
curl -s -X POST http://127.0.0.1:8787/admin/api/v1/subscriptions/42/refresh \
  -H "X-Session-Token: $ADMIN_SESSION" | python3 -m json.tool
```

**逐字段对照第 1 步的 curl 输出**：llmRx 显示的「已用/上限/剩余」应与厂商返回一致。

### 3.1 常见结果与处置

| 现象 | 含义 | 处置 |
|---|---|---|
| `探测失败：rejected (status 401)` | key 无权 / 认证头不对 | 回第 1 步 |
| `探测失败：…no usable…` | 接口通了但字段不认识 | 把响应贴给我，调解析器 |
| 卡片显示「实验性」+ 窗口正常 | 正常 | 智谱接口本就是非公开 |
| 显示「厂商权威」但数字为 0 | 厂商返回的是别的口径（如只报 detail 不报 summary） | 对照第 1 步确认 |

---

## 第 4 步：验证路由联动（可选但推荐）

P22 的核心价值：厂商数据可用时，路由优先用它。

### 4.1 制造「厂商说没额度」

把该渠道的限额调小，让厂商报告的 ratio ≥ 100%。OpenRouter 最简单：直接在 OpenRouter 后台把 key 的 limit 调小。

### 4.2 观察路由行为

```bash
# 发一个请求，看它落到哪个渠道、RouterPath 是什么
curl -s http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer $LLMRX_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"model":"<你的模型>","messages":[{"role":"user","content":"hi"}]}'

# 看最近的路由决策
curl -s 'http://127.0.0.1:8787/admin/api/v1/logs?limit=5' \
  -H "X-Session-Token: $ADMIN_SESSION" \
  | python3 -c 'import sys,json;[print(r["channel_id"], r["router_path"]) for r in json.load(sys.stdin)["data"]]'
```

**期望**：RouterPath 出现 `L2.5(quota,src)` —— `src` 表示这是**厂商数据**判定的耗尽，而非本地计数。

对比：
- `L2.5(quota)` = 按 llmRx 本地计量剔除（P21 行为）
- `L2.5(quota,src)` = 按厂商报告剔除（P22 新增）

> 若本地计数很低却被剔除，说明该上游账号**在校外也被消耗**——这正是 P22 要暴露的信息。

---

## 第 5 步：确认本地 vs 厂商的对照

`/admin/monitoring` 页底部有「本地估算（P21）」对照条。

- 本地 ≈ 厂商 → 流量基本都走 llmRx，正常
- 本地 << 厂商 → 账号被其他工具共享，考虑分流或换号

---

## 第 6 步：开启后台自动探测

```yaml
# config.yml
usage_monitor:
  enabled: true
  interval_sec: 3600      # 厂商仪表盘更新很慢，不建议低于 300
  timeout_sec: 15
  retention_days: 90
```

改完重启实例。到 `/admin/subscriptions` 确认「探测状态：已启用」。

历史趋势（可选）：

```bash
curl -s 'http://127.0.0.1:8787/admin/api/v1/subscriptions/42/history?days=7' \
  -H "X-Session-Token: $ADMIN_SESSION" | python3 -m json.tool | head -30
```

---

## 第 7 步：收尾与安全

1. **撤销智谱会话 Cookie**（若填了）：智谱控制台 → 退出登录 → 改密码会让旧会话失效；Cookie 本身也建议删掉不再需要时
2. **确认 llmRx 里没有明文凭证**：

```bash
sqlite3 data/llmrx.db "SELECT id, name, usage_probe FROM channels WHERE usage_probe != '{}';"
# 期望看到 cookie_cipher 是 base64 密文，看不到明文
```

3. **Cookie 泄漏应急**：轮换智谱密码 → 旧会话 Cookie 立即失效 → 再在 llmRx 里更新/清空

---

## 排查速查表

| 症状 | 先查 |
|---|---|
| 订阅页空（没有卡片） | 渠道是否配置了 `usage_probe.kind`；用 `filter=all` 看全部渠道 |
| 卡片显示「无数据」 | 手动刷新看错误信息；查网关日志 `grep 'subusage'` |
| 401 / 令牌无效 | 第 1 步 curl 是否同样失败；确认用的是 coding plan key |
| 数字明显不对 | 把厂商原始响应与 `/subscriptions/{id}/refresh` 的输出逐字段比 |
| 刷新后卡片形状变化 | 这是已修的 bug（P22 提交 `4105c06` 之前），升级到最新版 |
| 路由没被影响 | 确认 `usage_guard` 开启、渠道配了 `plan_quota`、厂商 ratio ≥ 1 |

网关日志里有专门的行：

```bash
grep -E 'subusage|usage tracker' gateway.log | tail -20
```

---

## 回滚

探针只是**读操作**，关掉不影响转发：

```yaml
usage_monitor:
  enabled: false
```

或对单个渠道：把 `usage_probe` 清空（表单里选「不探测」保存）。

若路由行为也要恢复为纯本地判定：把运行时配置 `quota_guard_enabled` 设为 false（`/admin/effective` 页可改）。
