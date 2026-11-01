# P14 — 上游渠道余额监控 / Upstream Balance Monitor

> Date: 2026-11-01 · Owner: llmRx maintainers · Status: **设计稿(未实施)**
> 前置:P0-P13 已完成;参照 `internal/prober`(健康探测)骨架。
> 调研来源:Magpie(usemagpie.ai)功能盘点、one-api/new-api 余额实现、各家 provider 官方文档。

## 1. Why now

llmRx 目前的"用量"视角只有**本地侧**:`channels.total_cost_usd` 由
`RecordChannelSpend` 在每次请求后原子累加,Dashboard 有通道花费 Top10。
但对运营者真正要紧的是**上游侧**的问题:

- DeepSeek / 智谱 / Moonshot / SiliconFlow 这类预付费渠道,**key 还剩多少钱**?
- 哪个渠道快耗尽了?要不要现在充值/切换?
- 渠道配置里的 `input_price_per_1m` 是配置价,不是上游真实扣费——本地记账
  与上游扣费会漂移,需要一个"对账锚点"。

竞品均已内置此能力:one-api/new-api 在渠道列表展示余额列 + "更新余额"
按钮(管理端 `GET /api/channel/update_balance`,后端按渠道分派
`FetchUpstreamBalance`);Magpie(本地网关)按账号/上游 key 拆账、按
"用量最少"路由。llmRx 缺的是同一块拼图。

**定位:独立的运营侧小功能**——不进请求热路径、不影响路由、不烧推理配额
(余额端点不计费)。

## 2. 上游余额接口调研(2026-11 摸底)

### 2.1 可用端点矩阵

| Provider | 端点 | 关键字段 | 可行性 |
|---|---|---|---|
| DeepSeek | `GET {base}/user/balance` | `is_available`, `balance_infos[].{currency,total_balance,granted_balance,topped_up_balance}` | ✅ 官方支持 |
| 智谱 GLM | `GET https://open.bigmodel.cn/api/paas/v4/users/me/balance` | 余额 JSON(格式曾有第三方解析踩坑记录) | ✅ 可用,实现期核对 schema |
| Moonshot/Kimi | `GET {base}/users/me/balance` | `data.{available_balance,voucher_balance,cash_balance}` | ✅ 官方文档明确 |
| SiliconFlow | `GET {base}/v1/user/info` | `data.{balance,chargeBalance,totalBalance,status}` | ✅ **取 `balance`(可用余额),勿取 `totalBalance`**(社区有取错字段显示负数的案例) |
| one-api/new-api 中转站 | `GET {base}/dashboard/billing/subscription` + `GET {base}/dashboard/billing/usage?start_date=&end_date=` | `hard_limit_usd`、`total_usage`(美分) | ✅ 余额 = limit − usage/100,国内中转大量兼容此协议 |
| OpenAI | `GET /v1/organization/usage/costs`(Admin Key) | 按日花费明细 | ⚠️ 需**独立 Admin Key**;预付费剩余额度无公开端点(旧 `dashboard/billing` 已废弃)→ **二期** |
| Anthropic | `GET /v1/organizations/usage_report/messages`(Admin Key) | 按 key/model 分组用量 | ⚠️ 同样需 Admin Key → **二期** |
| MiniMax | `…/v1/api/openplatform/coding_plan/remains` | Plan 剩余 | ❌ 鉴权受限(反馈需 Cookie);国际站无公开端点 → 不支持 |
| Gemini | 无公开余额端点 | — | ❌ 不支持,回退显示本地 `total_cost_usd` |

### 2.2 采样命令

```bash
# DeepSeek
curl -s https://api.deepseek.com/user/balance -H "Authorization: Bearer $KEY"
# → {"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00",
#    "granted_balance":"10.00","topped_up_balance":"100.00"}]}

# Moonshot
curl -s https://api.moonshot.cn/v1/users/me/balance -H "Authorization: Bearer $KEY"
# → {"data":{"available_balance":"12590.00","voucher_balance":"0.00","cash_balance":"12590.00"},"code":0}

# SiliconFlow
curl -s https://api.siliconflow.cn/v1/user/info -H "Authorization: Bearer $KEY"
# → {"data":{"balance":"10.00","chargeBalance":"…","totalBalance":"…","status":"normal"}}
```

### 2.3 调研结论

1. **余额接口无法按协议统一**:DeepSeek/智谱/Moonshot/SF 全是 OpenAI
   chat 协议,但余额端点路径与响应格式各不相同 → **必须按 provider 名分派**,
   而非按 `channel.protocol` 分派。
2. 主流国内渠道都有"查余额"端点,适合一期;OpenAI/Anthropic 是"查用量"
   (且要 Admin Key),是另一套逻辑,放二期。
3. 余额是**账号(=key)级**的,不是渠道级的;多 key 渠道天然失真(见 D1)。
4. 币种不统一(CNY/USD),展示与告警阈值都要带币种标注(见 D4)。
5. 上游计量分两类,监控模型完全不同:**预付费余额型**(DeepSeek/智谱/
   Moonshot/SF,一次 GET 查静态余额,花完为止)与**订阅配额窗口型**
   (各家 Coding Plan / M Plan,额度按窗口周期重置,要监控的是"窗口剩余量
   + 重置时间",如 MiniMax `coding_plan/remains`、智谱 GLM Coding Plan
   配额)。一期只覆盖前者;后者见 §16 二期。

## 3. 业界参照

| 项目 | 机制 |
|---|---|
| one-api | 渠道列表"余额"列;`GET /api/channel/update_balance`(可带 id)后端分派;定时自动刷新 |
| new-api | 同上,新增 New API 渠道原生余额查询;多 key 渠道查询仍在 [issue #7858](https://github.com/QuantumNous/new-api/issues/7858) 讨论中 |
| Magpie | 按账号/上游 key 拆账、同轮会话钉账号保 prompt cache;核心路由模式全部建立在**配额感知**上——smart(优先即将续期的)、pace(窗口匀速)、usage(用量最少)、rotate,多账号 failover 配额用完静默切换。注:Magpie 是个人本地桌面网关(改写 agent 配置文件、复用订阅账号),与服务端网关定位不同;可借鉴的只有"配额感知"这一层 |
| cc-switch 等 | `balance` vs `totalBalance` 字段混淆导致显示负数的教训 → 字段语义必须写死在 parser 里 |

## 4. Scope

### 4.1 一期(MVP,本设计主体)

- `provider` 层新增可选 `BalanceChecker` 能力接口 + 按 provider 名分派;
- 内置 5 种 parser:`deepseek` / `moonshot` / `zhipu` / `siliconflow` / `oneapi`;
- `internal/balance` 后台监控器(周期刷新 + 手动触发),结果持久化到
  `channels` 新增列;
- WebUI:Channels 列表"上游余额"列 + 单渠道"刷新"按钮 + Dashboard 余额卡片;
- Admin JSON API:批量快照 + 单渠道强制查询;
- 不支持的 provider(Gemini/MiniMax/OpenAI/Anthropic)UI 回退显示本地
  `total_cost_usd`,并标注"该渠道不支持余额查询"。

### 4.2 二期(不在本设计实现范围)

- `balance_low` 告警类型(阈值 + webhook,复用 alert 子系统);
- 余额历史趋势(独立 `channel_balances` 时序表);
- per-key 余额(多 key 渠道逐 key 查询、key 级展示);
- OpenAI `/v1/organization/usage/costs`、Anthropic Admin usage_report
  (需要 Admin Key 的用量型查询,与余额型并存);
- 订阅配额窗口型渠道监控(Coding Plan / M Plan 类,窗口剩余量 + 重置时间);
- LiteLLM 上游 `/key/info` / `/user/info` 适配。

### 4.3 明确不做

- 不进请求热路径:路由、限速、计费逻辑零改动;
- 余额查询不计费、不写 `logs` 表;
- 不做余额自动充值、不做按余额的自动路由降权(留给告警 + 人工决策)。

## 5. 设计决策

### D1 渠道级 vs key 级 → 一期渠道级,取首个 active key

余额本质是账号级的。但一期引入 per-key 存储(新表 + Key 模型扩展 + UI)
成本高;而 `ChannelFetchModels`(`internal/webui/providers.go`)已有
"取渠道第一个 key 打上游"的先例。一期**取第一个 active key 查询,UI 标注
"按首个 key 查询"**;多 key 渠道的精确拆账留二期(issue #7858 同款取舍)。

### D2 查"余额"还是查"用量" → 一期只做余额型

DeepSeek/智谱/Moonshot/SF/one-api 都是"一次 GET 拿剩余额度"的同构调用;
OpenAI/Anthropic 的 Admin 用量 API 需要独立 key、返回的是时序账单,模型
完全不同。混在一期会把接口设计拖复杂。

### D3 存储位置 → `channels` 加列,不建新表

最新快照放 `channels` 行上(4 列,见 §6),lazy migration 走现成的
`s.addColumn()` 模式。**不建内存缓存**——快照持久化后,UI/ API 直接读
DB,重启不丢,实现比 prober 更简单(prober 需要内存 cache 是因为探测
结果高频且无需跨重启)。历史趋势才是新表的活,留二期。

### D4 币种 → 显式存储 + UI 标注

DeepSeek 返回 CNY、one-api 返回 USD、部分渠道不返回币种。快照存
`balance_currency`(ISO 4217,未知存空串),**所有合计/展示按币种分行,
绝不跨币种相加**;二期告警阈值也必须 per-channel 配置或带币种。

### D5 分派机制 → provider descriptor 声明,channel 可覆盖 URL

在 `providers.yaml` 的 provider 描述符上加 `balance: {path, format}`;
`channel.balance_url` 可覆盖(适配中转站/自建代理换路径)。parser 按
`format` 名注册(kind 表驱动),新增一家 = 加一段 descriptor + (若格式
全新)一个 parser,不动 Go 分派逻辑。

## 6. 存储

```sql
ALTER TABLE channels ADD COLUMN balance TEXT;              -- 可用余额,保留上游精度(字符串)
ALTER TABLE channels ADD COLUMN balance_currency TEXT;     -- ISO 4217;'' = 上游未标明
ALTER TABLE channels ADD COLUMN balance_updated_at TIMESTAMP;  -- NULL = 从未查询成功
ALTER TABLE channels ADD COLUMN balance_error TEXT;        -- 最近一次失败原因(成功时清空;截断 512 字符)
ALTER TABLE channels ADD COLUMN balance_url TEXT;          -- 可选:覆盖 descriptor 的余额端点完整 URL
```

`model.Channel` 追加对应 5 个字段(`json:"balance,omitempty"` 等);
`channels/list` 与 admin API 的 SELECT 列表同步扩列。

```go
// store.Store 新增
UpdateChannelBalance(ctx context.Context, channelID int64,
    balance, currency string, checkedAt time.Time, balanceErr string) error
```

SQLite 与 Postgres 双实现;均为单行 UPDATE,天然幂等(集群多节点
last-write-wins,见 §15 风险)。

## 7. Provider 层

### 7.1 能力接口(沿用 ModelLister 的可选接口模式)

```go
// internal/provider/balance.go
type Balance struct {
    Available float64 // 可用余额
    Currency  string  // ISO 4217;"" = 未知
    Granted   float64 // 赠送余额(可选,0 = 未报告)
    Source    string  // parser kind,调试用("deepseek" / "oneapi" / ...)
}

// 实现方按 kind 注册;不支持余额查询的 provider 不实现
type BalanceParser interface {
    ParseBalance(body []byte) (Balance, error)
}

// 包级分派入口(工厂 + descriptor),对齐 ListModels 的用法
func QueryBalance(ctx context.Context, providerName, apiKey, baseURL string) (Balance, error)
```

`QueryBalance` 流程:registry 按 providerName 找 descriptor → 取
`balance.path/format`(channel 层可传 override URL)→ `GET` +
provider 惯用鉴权头(`Authorization: Bearer`,三种现有协议的 key 均如此)
→ 按 `format` 找注册的 `BalanceParser` 解析。

### 7.2 Descriptor 扩展(providers.yaml)

```yaml
- name: deepseek
  protocol: openai
  base_url: https://api.deepseek.com/v1
  balance: { path: /user/balance, format: deepseek }      # 注意:挂在 API root,不带 /v1

- name: moonshot
  protocol: openai
  base_url: https://api.moonshot.cn/v1
  balance: { path: /users/me/balance, format: moonshot }

- name: zhipu
  protocol: openai
  base_url: https://open.bigmodel.cn/api/paas/v4
  balance: { path: /users/me/balance, format: zhipu }

- name: siliconflow
  protocol: openai
  base_url: https://api.siliconflow.cn/v1
  balance: { path: /user/info, format: siliconflow }

# one-api/new-api 系中转:无内建 descriptor,靠 channel.balance_url + format: oneapi
```

### 7.3 内置 parser 语义(字段选择是硬编码的教训集)

| kind | 端点 | 可用余额取值 | 币种 | 备注 |
|---|---|---|---|---|
| `deepseek` | `{root}/user/balance` | `balance_infos[0].total_balance` | `balance_infos[0].currency` | `is_available=false` 视为查询成功但置 error 提示 |
| `moonshot` | `{base}/users/me/balance` | `data.available_balance` | 无(按站点 CNY) | `code != 0` 报错 |
| `zhipu` | `{base}/users/me/balance` | 实现期核对官方 schema | 实现期核对 | 上线前用真实 key 验一次 |
| `siliconflow` | `{base}/user/info` | `data.balance`(**不是 totalBalance**) | 无(按站点 CNY) | `status != "normal"` 附加提示 |
| `oneapi` | `{root}/dashboard/billing/subscription` + `/dashboard/billing/usage` | `hard_limit_usd − total_usage/100` | `USD` | 两次 GET;usage 的 `start_date` 取 90 天前 |

base URL 拼接规则:`balance.path` 挂在**去掉了 `/v1` 尾缀的 root** 上
(DeepSeek 的余额端点不在 `/v1` 下),descriptor 里写全路径避免歧义——
实现时用 `strings.TrimSuffix(baseURL, "/v1")` 一类规整,单测覆盖
`…/v1`、`…/`(带尾斜杠)、裸 root 三种 base_url 形态。

## 8. internal/balance 监控器

复刻 prober 的 goroutine 惯例,但**无内存缓存、无指数退避**(低频 +
结果落库,失败下个周期自然重试):

```go
// internal/balance/monitor.go
type Config struct {
    Interval   time.Duration // 默认 1h;0 = 禁用自动刷新
    Timeout    time.Duration // 单次查询超时,默认 10s
    JitterPct  int           // 每 tick 错峰,默认 10
}

func New(cfg Config, st store.Store, pl *pool.ChannelPool) *Monitor
func (m *Monitor) Start(ctx context.Context)          // go loop();tick 到期逐渠道查
func (m *Monitor) QueryChannel(ctx context.Context, ch *model.Channel) (Snapshot, error) // 手动,同步
func (m *Monitor) Stop()
```

每轮筛选:`Status==Enabled` 且有 active key 且(descriptor 有 balance 或
channel 配了 `balance_url`)的渠道;取第一个 active key 明文(pool 已
解密持有)→ `provider.QueryBalance` → `UpdateChannelBalance` 落库。
查询失败同样落库(`balance_error`,截断 512 字符),成功时清空。
**panic-safe**:单渠道查询包 recover,一个渠道炸不掉循环。

注入方式对齐 prober:`internal/server/server.go` 构造后
`webUI.SetBalanceMonitor(m)` / `adminH.SetBalanceMonitor(m)`;
`main.go` 优雅停机段 `m.Stop()`。

## 9. API 与 WebUI

### 9.1 Admin JSON API(`/admin/api/v1`)

```
GET  /balances                    → [{channel_id,name,provider,balance,currency,
                                     updated_at,error,supported}]  (读 channels 行,纯 DB)
POST /channels/{id}/balance       → 同步触发一次查询,返回快照(409 = 该渠道不支持)
```

### 9.2 WebUI(全部读 channels 行,不引新页面)

- `channels/list.html`:新增"上游余额"列——值 + 币种小字、
  `title` 显示 `balance_updated_at`、error 时红字 tooltip;
  行操作加"刷新余额"按钮(HTMX POST,对齐"立即探针"的交互);
- 不支持的渠道:该列显示 `—`(title 提示原因),并有本地累计花费
  `total_cost_usd` 可看,不误导;
- `dashboard.html`:余额卡片——按币种分行的合计 + 低余额渠道计数;
- Channel 编辑表单 Advanced 区:`balance_url` override 输入框;
- 按钮走现有 RBAC(Admin 可查,Root/Admin 均可手动触发,沿用渠道编辑权限)。

## 10. 配置

```yaml
# config.yml(新段;进 runtime_settings 可热更,对齐 log_retention 模式)
balance_monitor:
  enabled: true          # false = 完全关闭(不建 goroutine)
  interval_sec: 3600     # 自动刷新周期
  timeout_sec: 10
```

`channel.balance_url` 是渠道级配置,随 Channel CRUD 走现有表单/API。

## 11. 测试计划

| 测试 | 覆盖点 |
|---|---|
| `internal/provider/balance_test.go` | 5 种 parser × httptest mock 上游(真实响应样例固化为 fixture);base_url 三种形态拼接;错误响应/非 JSON/字段缺失 |
| `internal/provider/balance_test.go` | `QueryBalance` 分派:descriptor 命中、channel override URL、未知 kind 报错 |
| `internal/balance/monitor_test.go` | tick 筛选(enabled/key/descriptor);成功落库 + 清 error;失败落 error;单渠道 panic 不影响循环;手动 QueryChannel |
| `internal/store` | `UpdateChannelBalance` 双实现;5 列 lazy migration;NULL `balance_updated_at` |
| `internal/webui` / `internal/admin` | 列表列渲染(支持/不支持/出错三态);POST refresh 路由与权限;`/balances` 快照形状 |
| 场景(`tests/scenarios`) | mock 上游 + httptest 全链路:查询 → 落库 → 列表可见 |

全部走现有惯例:httptest + `t.TempDir()` SQLite + mock 上游,不打外网。

## 12. 验收标准

| 指标 | 目标 |
|---|---|
| DeepSeek/Moonshot/SF 真实 key 查询成功且余额正确 | ✅(zhipu 以官方文档核对后同标准) |
| 不支持渠道 UI 显示 `—` 且不报错 | ✅ |
| 余额查询不计费、不写 logs、不影响路由 | ✅ |
| 监控器 race-clean(`go test -race`) | ✅ |
| 新增代码覆盖率 ≥ 70%,整体 ≥ 65%(CI gate) | ✅ |
| `balance_monitor.enabled=false` 时零 goroutine、零行为变化 | ✅ |

## 13. 文件清单与工作量

```
NEW
internal/provider/balance.go            ~200 LoC(类型 + 分派 + HTTP + 5 parser)
internal/provider/balance_test.go       ~350 LoC(fixture + 分派)
internal/balance/monitor.go             ~200 LoC
internal/balance/monitor_test.go        ~250 LoC

MODIFY
internal/provider/registry.go           +balance descriptor 读取(~30)
internal/provider/providers.yaml        +4 条 balance 段(~16)
internal/model/types.go                 +Channel 5 字段(~15)
internal/store/store.go                 +UpdateChannelBalance 签名(~4)
internal/store/sqlite.go                +5 列迁移 + UPDATE(~50)
internal/store/postgres.go              +同上(~40)
internal/server/server.go               +构造/注入/停机(~20)
internal/webui/handler.go               +Setter + 路由(~15)
internal/webui/channels.go              +刷新 handler(~40)
internal/webui/templates/channels/list.html  +列 + 按钮(~30)
internal/webui/templates/dashboard.html +卡片(~25)
internal/admin/handler.go               +2 端点(~60)
config.yml / internal/config            +balance_monitor 段(~25)
docs/P14-BALANCE-MONITOR.md             本文件
```

代码合计 ≈ **1200 LoC(含测试 ~800)**,一个 PR 的量。

## 14. 实施顺序

1. schema 迁移 + `UpdateChannelBalance`(store 双实现)+ 单测;
2. provider `BalanceParser` + `deepseek`/`moonshot`/`siliconflow` 三家
   (官方 schema 明确)→ httptest 单测;
3. `zhipu` + `oneapi` parser(实现期核对真实响应);
4. `internal/balance` 监控器 + server 接线;
5. Admin API 两端点 + WebUI 列/按钮/卡片;
6. `balance_monitor` 配置段 + runtime 热更;
7. 真实 key 手工验证(DeepSeek/Moonshot/SF 各一);
8. README 文档导航 + CHANGELOG。

## 15. 风险

| 风险 | 缓解 |
|---|---|
| 上游改余额接口 schema(第三方格式踩坑先例) | parser fixture 单测;失败落 `balance_error` 不静默;修复 = 改一个 parser |
| 多 key 渠道余额失真(只查首个 key) | UI 明确标注"按首个 key 查询";二期 per-key |
| 字段语义混淆(SF `balance` vs `totalBalance` 负数案例) | 语义写死在 parser 注释 + fixture 断言 |
| 集群多节点同时刷新(重复打上游) | interval 加 jitter;UPDATE 幂等 last-write-wins;MVP 单实例为主 |
| 智谱响应 schema 未最终核对 | 标记为实施期首个验证项,不阻塞其他四家 |
| 币种混算误导运营 | D4:按币种分行,禁止跨币种合计 |
| key 明文暴露面(查询需解密) | 复用 pool 内存中已解密 key,不落日志、不进 error 文本;`balance_error` 只存 HTTP 状态 + 截断消息 |

## 16. 二期展望(与 §4.2 对应)

- **balance_low 告警**:`model/alert.go` 加 `AlertBalanceLow` 常量 +
  `alert/rules.go` 加 case(数据源即 `channels.balance`)+
  `webui/phase2.go` 白名单 + 表单 option——4 处小改;阈值必须带币种
  (per-channel `balance_threshold` 列或全局 per-currency);
- **余额趋势**:`channel_balances(channel_id, ts, balance, currency)` 时序表,
  复用 Analytics 时序图组件;
- **per-key 余额**:keys 表加余额列,多 key 渠道逐 key 查询与展示,
  对齐 new-api issue #7858 的诉求;
- **用量型查询**:OpenAI Admin costs / Anthropic Admin usage_report,
  需在 Channel 上引入独立的 admin key 概念;
- **配额窗口型渠道**(见 §2.3 结论 5):订阅/Coding Plan 类上游的额度按
  窗口重置,监控维度是"窗口剩余量 + 重置时间"而非静态余额。数据源如
  MiniMax `coding_plan/remains`(有 Cookie 鉴权受限问题,需逐一解决)、
  智谱 GLM Coding Plan 配额接口。拿到数据后可对齐 Magpie 已验证的路由
  模式,作为 L3/L5 的加权信号:pace(窗口内匀速消耗,避免前期烧完)、
  smart(优先即将重置/续期的渠道)、usage(用量最少优先);
- **按余额路由**:L3 策略增加"余额加权"信号(余额低降权),与
  Thompson/L5 blend——需先有趋势数据。
