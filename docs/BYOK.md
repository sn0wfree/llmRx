# BYOK（Bring Your Own Key）设计方案

> 2026-07-08 · 白名单版 · **当前状态：⏸️ 暂缓实施，仅接口预留**

---

## 目录

1. [背景与目标](#1-背景与目标)
2. [数据流 Flow 3](#2-数据流-flow-3)
3. [实施模块清单](#3-实施模块清单)
4. [分阶段实施](#4-分阶段实施)
5. [接口预留（Phase 1.5）](#5-接口预留phase-15)
6. [配置与存储](#6-配置与存储)
7. [风险与缓解](#7-风险与缓解)
8. [未来补完路径](#8-未来补完路径)

---

## 1. 背景与目标

### 1.1 业务场景

**BYOK** = Bring Your Own Key，消费者自带上游 Provider 的 key 走 llmRx 网关。

**典型使用场景**：
- 企业版客户用自己采购的 OpenAI key，llmRx 不收费（透传）
- 客户担心数据隔离，用自己的 key 走自家网关做审计
- 内部多团队共享网关，每个团队用自己的 key

### 1.2 当前决策

| 决策 | 内容 |
|------|------|
| 是否实施 | ⏸️ **暂缓** |
| 接口预留 | ✅ 4 处接口已规划 |
| 触发条件 | 未来需要支持外部服务时 |
| 预留成本 | ~50 行额外代码 |
| 未来补完成本 | 1-2 天 |

### 1.3 与 Flow 1 / Flow 2 的关系

| 流 | Token 类型 | 状态 |
|----|-----------|------|
| Flow 1 | `sk-customer-xxx`（llmRx 内部）| ✅ 实施 |
| Flow 2 | session cookie（admin 鉴权）| ✅ 实施 |
| **Flow 3** | `sk-openai-xxx`（上游 key）| ⏸️ 暂缓 |

**核心区别**：Flow 1 中 llmRx 持有上游 key 并代为调用；Flow 3 中消费者自带 key，llmRx 充当代理/审计层。

---

## 2. 数据流 Flow 3

### 2.1 完整时序

```
Consumer                 Go Gateway                      Upstream LLM
   │                          │                                │
   │ POST /v1/chat/completions │                                │
   │ Bearer sk-openai-xxx      │                                │
   ├─────────────────────────→│                                │
   │                          │ [1] tokencache.Lookup() miss   │
   │                          │ [2] BYOK 路径启用?             │
   │                          │ [3] CheckWhitelist(client_ip)  │
   │                          │     → 在白名单                  │
   │                          │ [4] max_keys_per_ip 未超限     │
   │                          │ [5] DetectProvider()           │
   │                          │     sk- + 长度 > 40 → openai   │
   │                          │ [6] TestKey(provider, key)     │
   │                          │                                │
   │                          │ GET /v1/models                 │
   │                          │ Bearer sk-openai-xxx           │
   │                          ├───────────────────────────────→│
   │                          │←───────────────────────────────┤
   │                          │     200 OK → 验证通过           │
   │                          │ [7] AutoCreateChannel()        │
   │                          │     secrets.Encrypt(key)       │
   │                          │     store.CreateBYOK()         │
   │                          │ [8] pool.LoadByokChannels()    │
   │                          │ [9] RouterEngine.RouteWith()   │
   │                          │     L1 含 byok_channels        │
   │                          │     → 选该 byok channel        │
   │                          │ [10] pool.NextKey()            │
   │                          │     → 拿刚才的 key             │
   │                          │                                │
   │                          │ POST /chat/completions         │
   │                          │ Bearer sk-openai-xxx           │
   │                          ├───────────────────────────────→│
   │                          │←───────────────────────────────┤
   │                          │ [11] emitLog(is_byok=true)     │
   │ 200 OK + OpenAI 格式      │                                │
   │←─────────────────────────┤                                │
```

### 2.2 关键步骤

| 步骤 | 动作 | 失败处理 |
|------|------|---------|
| 1 | `tokencache.Lookup()` miss | 进入 BYOK 路径 |
| 2 | 检查 `byok.enabled` | false → 401 |
| 3 | 检查 IP 白名单 | 不在白名单 → 403 |
| 4 | 检查单 IP key 数 | 超限 → 429 |
| 5 | 启发式检测 Provider | 未知 → 400 |
| 6 | TestKey 调上游 `/v1/models` | 失败 → 401 |
| 7 | AutoCreateChannel（加密+持久化）| DB 错误 → 500 |
| 8 | 加载到 byok_pool | — |
| 9 | L1 路由（含 byok_channels）| — |
| 10 | pool.NextKey | — |
| 11 | 调上游 + 记日志 | 透传 |

### 2.3 与 Flow 1 的对比

| 维度 | Flow 1 | Flow 3 |
|------|--------|--------|
| Token 来源 | llmRx 生成 | 消费者自备 |
| 鉴权 | llmRx tokencache | IP 白名单 |
| 上游 key 位置 | llmRx DB（admin 配置）| llmRx DB（自动创建）|
| 计费 | 计入 plan.used_usd | **不计费** |
| 路由优先级 | 主 channels 池 | byok_channels 池（独立）|
| 清理 | 不清理 | 30 天未用自动清理 |

---

## 3. 实施模块清单

### 3.1 总览

| # | 模块 | 难度 | LOC | 风险点 | 依赖 |
|---|------|------|-----|--------|------|
| 1 | 数据库表（byok_channels）| 低 | ~30 | 无 | — |
| 2 | Provider 检测（前缀启发式）| 中 | ~50 | 多 provider 前缀相同 | — |
| 3 | IP 白名单（CIDR 解析）| 中 | ~80 | 边界判断 | — |
| 4 | Key 验证（test API）| 中 | ~100 | 额外 API 成本 | #2 |
| 5 | 自动创建 channel（加密+持久化）| 中 | ~80 | 并发安全 | #1, #4 |
| 6 | 路由层 L1 集成 | 中 | ~60 | 性能影响 | #5 |
| 7 | 限流（max_keys_per_ip）| 低 | ~40 | 无 | #5 |
| 8 | 清理 goroutine（30 天 TTL）| 低 | ~30 | goroutine 泄漏 | #5 |
| 9 | Admin UI（/admin/byok 页）| 中 | ~200 | 模板复杂度 | #5 |
| 10 | 日志标记 + 统计 | 低 | ~30 | 无 | #5 |
| 11 | 单元 + 集成测试 | 中 | ~300 | mock 上游 | 全部 |
| **总计** | | **中** | **~1000** | **~2-3 天** | |

### 3.2 数据库表设计

```sql
CREATE TABLE byok_channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider TEXT NOT NULL,           -- openai/anthropic/gemini
  key_ciphertext TEXT NOT NULL,
  key_masked TEXT NOT NULL,
  owner_ip TEXT NOT NULL,
  owner_email TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 1,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  use_count INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_byok_last_used ON byok_channels(last_used_at);
CREATE INDEX idx_byok_owner_ip ON byok_channels(owner_ip);
```

### 3.3 Go 类型定义

```go
// internal/model/byok.go
type BYOKChannel struct {
    ID            int64
    Provider      string  // openai/anthropic/gemini
    KeyCiphertext string  // AES-256-GCM 加密
    KeyMasked     string  // "sk-...xxxx"
    OwnerIP       string
    OwnerEmail    string
    Status        int     // 1=active
    LastUsedAt    int64
    UseCount      int64
    CreatedAt     int64
}
```

### 3.4 Provider 检测规则

```go
// internal/byok/detect.go
func DetectProvider(key string) (string, error) {
    // 长度检查
    if len(key) < 20 {
        return "", errors.New("key too short")
    }
    
    // 前缀匹配
    switch {
    case strings.HasPrefix(key, "sk-ant-"):
        return "anthropic", nil
    case strings.HasPrefix(key, "AIza"):
        return "gemini", nil
    case strings.HasPrefix(key, "sk-") && len(key) > 40:
        return "openai", nil  // 默认 OpenAI 兼容
    default:
        return "", errors.New("unknown key format")
    }
}
```

### 3.5 IP 白名单

```go
// internal/byok/whitelist.go
type Whitelist struct {
    cidrs []*net.IPNet
}

func NewWhitelist(cidrs []string) (*Whitelist, error) {
    w := &Whitelist{}
    for _, c := range cidrs {
        _, ipNet, err := net.ParseCIDR(c)
        if err != nil {
            return nil, fmt.Errorf("invalid CIDR %s: %w", c, err)
        }
        w.cidrs = append(w.cidrs, ipNet)
    }
    return w, nil
}

func (w *Whitelist) Allow(ip string) bool {
    if len(w.cidrs) == 0 {
        return false  // 空白名单 = 禁止
    }
    parsed := net.ParseIP(ip)
    if parsed == nil {
        return false
    }
    for _, c := range w.cidrs {
        if c.Contains(parsed) {
            return true
        }
    }
    return false
}
```

### 3.6 Key 验证

```go
// internal/byok/validate.go
func TestKey(ctx context.Context, provider, key string) error {
    switch provider {
    case "openai":
        return testOpenAI(ctx, key)
    case "anthropic":
        return testAnthropic(ctx, key)
    case "gemini":
        return testGemini(ctx, key)
    default:
        return errors.New("unsupported provider")
    }
}

func testOpenAI(ctx context.Context, key string) error {
    req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+key)
    
    client := &http.Client{Timeout: 5 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode == 200 {
        return nil
    }
    if resp.StatusCode == 401 {
        return ErrInvalidKey
    }
    return fmt.Errorf("upstream returned %d", resp.StatusCode)
}
```

### 3.7 自动创建

```go
// internal/byok/create.go
func AutoCreateChannel(ctx context.Context, s store.Store, sm *secrets.Manager,
    provider, key, ownerIP, ownerEmail string) (*model.BYOKChannel, error) {
    
    // 加密
    ciphertext, err := sm.Encrypt(key)
    if err != nil {
        return nil, err
    }
    
    // 构造记录
    ch := &model.BYOKChannel{
        Provider:      provider,
        KeyCiphertext: ciphertext,
        KeyMasked:     secrets.Mask(key),
        OwnerIP:       ownerIP,
        OwnerEmail:    ownerEmail,
        Status:        1,
        LastUsedAt:    time.Now().Unix(),
        UseCount:      0,
        CreatedAt:     time.Now().Unix(),
    }
    
    // 写 DB
    id, err := s.CreateBYOKChannel(ctx, ch)
    if err != nil {
        return nil, err
    }
    ch.ID = id
    
    return ch, nil
}
```

---

## 4. 分阶段实施

### 4.1 阶段拆分（精细）

| 原阶段 | 新阶段 | 工作量 | 主题 |
|--------|--------|--------|------|
| 1.5.A | **1.5.A1** | 0.5 天 | 数据层（表 + 类型 + Store 接口）|
| 1.5.A | **1.5.A2** | 0.5 天 | 检测层（Provider + Whitelist）|
| 1.5.A | **1.5.A3** | 0.5 天 | 验证层（TestKey + 缓存）|
| 1.5.A | **1.5.A4** | 0.5 天 | 创建层（AutoCreate + 加密）|
| 1.5.A | **1.5.A5** | 0.5 天 | 路由集成（L1 + Limiter + 日志）|
| 1.5.B | **1.5.B** | 0.5 天 | 清理（Cleanup goroutine）|
| 1.5.C | **1.5.C1** | 0.5 天 | UI 列表（list + 搜索）|
| 1.5.C | **1.5.C2** | 0.5 天 | UI 详情（stats + 详情）|
| 1.5.C | **1.5.C3** | 0.5 天 | UI 操作（手动删除 / 触发清理）|
| 1.5.D | **1.5.D1** | 0.5 天 | 单元测试 |
| 1.5.D | **1.5.D2** | 0.5 天 | 集成测试 |
| 1.5.D | **1.5.D3** | 0.5 天 | 基准测试 |
| **总工作量** | | **~6 天** | |

### 4.2 模块重新分组

| 层级 | 模块 | 阶段 |
|------|------|------|
| **Data** | #1 表 + #2 类型 | A1 |
| **Detect** | #3 Provider + #4 Whitelist | A2 |
| **Validate** | #5 TestKey | A3 |
| **Create** | #6 AutoCreate + 加密 | A4 |
| **Integrate** | #7 Router + #8 Limiter + #9 日志 | A5 |
| **Maintain** | #10 Cleanup | B |
| **UI** | #11 List + #12 Detail + #13 Ops | C1/C2/C3 |
| **Test** | #14 Unit + #15 Integration + #16 Bench | D1/D2/D3 |

### 4.3 依赖关系（允许并行）

```
A1 (Data) ─┬─→ A2 (Detect) ─→ A3 (Validate) ─→ A4 (Create) ─→ A5 (Integrate)
           │                                                     │
           │                                                     ├─→ B (Maintain)
           │                                                     ├─→ C1 (UI List)
           │                                                     ├─→ C2 (UI Detail)
           │                                                     └─→ C3 (UI Ops)
           │                                                     │
           │                                                     └─→ D1/D2/D3 (Tests，可与 C 并行)
```

**并行规则**：
- A5 完成后，B / C / D 三个方向可并行
- D1（单元）可最早开始（不依赖 UI）
- D2（集成）依赖 A5 完成
- D3（基准）依赖 A5 完成

---

## 5. 接口预留（Phase 1.5）

> 当前阶段：⏸️ **仅留接口，不实现**

### 5.1 Store 接口扩展

**位置**：`internal/store/store.go`

```go
type Store interface {
    // ... 现有方法 ...

    // BYOK 接口（暂未实现，返回 ErrNotImplemented）
    CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error)
    ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error)
    GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error)
    DeleteBYOKChannel(ctx context.Context, id int64) error
}
```

**SQLite 实现**（v1 占位）：
```go
func (s *SQLite) CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error) {
    return 0, errors.New("BYOK not yet implemented, see docs/BYOK.md")
}
```

### 5.2 Router L1 钩子

**位置**：`internal/router/engine.go`

```go
type RouterEngine struct {
    // ... 现有字段
    extraChannels []func() []*model.Channel  // BYOK 通道源（v1 空）
}

func (e *RouterEngine) RegisterExtraChannels(src func() []*model.Channel) {
    e.extraChannels = append(e.extraChannels, src)
}

// L1 步骤：
func (e *RouterEngine) staticMatch(model string) []*model.Channel {
    chans := e.static.Match(model)  // 现有
    for _, src := range e.extraChannels {
        chans = append(chans, src()...)
    }
    return chans
}
```

### 5.3 Middleware 未知 Token 处理

**位置**：`internal/middleware/auth.go`

```go
type WithLimitsConfig struct {
    LookupToken      func(key string) (*model.Token, bool)
    Limits           func() *runtime.Limits
    UnknownTokenHook func(w http.ResponseWriter, r *http.Request)  // 新增，默认 nil
}

// 在 middleware 中：
if !found {
    if config.UnknownTokenHook != nil {
        config.UnknownTokenHook(w, r)  // BYOK 路径（v1 不注册）
        return
    }
    http.Error(w, "invalid token", 401)  // 默认行为
}
```

### 5.4 Config `byok:` 段

**位置**：`config.yml` + `internal/config/config.go`

```yaml
# config.yml
byok:
  enabled: false  # 暂未实现，必须保持 false
  # 以下配置项预留（暂不使用）
  # whitelist_ips: []
  # max_keys_per_ip: 5
  # ttl_days: 30
```

```go
// internal/config/config.go
type BYOKConfig struct {
    Enabled bool `yaml:"enabled"`
    // 后续字段预留
}

type Config struct {
    // ... 现有字段
    BYOK BYOKConfig `yaml:"byok"`
}
```

### 5.5 预留成本 vs 未来重构成本

| 方案 | 优势 | 劣势 |
|------|------|------|
| **当前选: 暂缓+接口预留** | 未来补 BYOK 仅 1-2 天 | 预留代码略增复杂度 (~50 行) |
| 完全不做 | 0 复杂度 | 未来补 BYOK 需重构 1-2 天 |

**结论**：预留成本远低于未来重构成本。

---

## 6. 配置与存储

### 6.1 Config

```yaml
# config.yml
byok:
  enabled: false              # 必须保持 false 直到 BYOK 实施
  whitelist_ips: []           # CIDR 列表，如 ["10.0.0.0/8"]
  whitelist_emails: []        # 邮箱列表（请求带 X-User-Email）
  max_keys_per_ip: 5          # 单 IP 最多 5 个 key
  ttl_days: 30                # 30 天未用自动清理
  test_on_create: true        # 创建时验证
  providers:                  # 支持的 provider
    - openai
    - anthropic
    - gemini
```

### 6.2 存储

| 表 | 用途 | 大小预估 |
|----|------|---------|
| `byok_channels` | 自动创建的 channel | 1 行 = ~200B |
| `logs` | 加 `is_byok` + `byok_ip` 字段 | 增量小 |

### 6.3 日志字段扩展

```sql
-- logs 表新增字段
ALTER TABLE logs ADD COLUMN is_byok INTEGER NOT NULL DEFAULT 0;
ALTER TABLE logs ADD COLUMN byok_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE logs ADD COLUMN byok_channel_id INTEGER NOT NULL DEFAULT 0;
```

---

## 7. 风险与缓解

| 风险 | 严重度 | 缓解 |
|------|--------|------|
| **Provider 前缀冲突** | 中 | 调 test API 验证最终确认 |
| **验证 API 调用失败/超时** | 中 | 5s 超时 + 5 分钟缓存结果 |
| **IP CIDR 解析错误** | 低 | 用 `net.ParseCIDR` 标准库 |
| **并发 BYOK 创建** | 低 | SQLite UNIQUE 约束防重复 |
| **路由层性能** | 低 | 内存查找，无 IO |
| **清理 goroutine 泄漏** | 低 | context 取消 + WaitGroup |
| **滥用（黑产用偷的 key）** | 中 | IP 频率限制 + alert + 审计 |
| **持久化 key 长期占用** | 低 | 30 天未使用自动清理 |

---

## 8. 未来补完路径

### 8.1 触发条件

满足以下任一条件时启动 BYOK 实施：
- 业务方明确要求支持外部服务
- 多租户 SaaS 化需求
- 客户自带 key 的合规要求

### 8.2 补完步骤

| 步骤 | 工作量 | 风险 |
|------|--------|------|
| 1. 实现 `model.BYOKChannel` 类型 | 0.5 天 | 低 |
| 2. 实现 byok 5 大模块（detect/whitelist/test/create/integrate）| 2-3 天 | 中 |
| 3. 实现 cleanup goroutine | 0.5 天 | 低 |
| 4. Admin UI（/admin/byok）| 1 天 | 中 |
| 5. 单元 + 集成 + 基准测试 | 1 天 | 中 |
| 6. 文档 + 部署指南 | 0.5 天 | 低 |
| **总计** | **~6-8 天** | |

### 8.3 风险

| 风险 | 缓解 |
|------|------|
| 验证 API 调用污染上游统计 | 加 header 标记 `X-llmRx-BYOK: true` |
| 误启用造成滥用 | 部署前必须人工审计白名单 |
| 持久化 key 泄露 | AES-256-GCM 加密 + 30 天 TTL |

---

## 附录 A：示例请求

### 启用 BYOK 后的请求示例

```bash
# 消费者自带 OpenAI key，IP 在白名单内
curl -X POST http://gateway.example.com:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-openai-abc123..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**Go 网关内部流程**：
1. tokencache.Lookup("sk-openai-abc123") → miss
2. 启用 BYOK 路径 → IP 白名单检查通过
3. DetectProvider → "openai"
4. TestKey → 200 OK
5. AutoCreateChannel → 加密 + 写 DB
6. 调 upstream 使用消费者自带 key
7. 记 logs（is_byok=true, byok_ip=client_ip）

### Admin 查看 BYOK 列表

```
GET /admin/byok
```

页面内容：
- BYOK channels 列表（owner_ip, provider, key_masked, use_count, last_used_at）
- 搜索 / 筛选
- 删除按钮
- 统计卡片（总 key 数 / 总调用数 / Top 5 IP）
- 手动触发清理

---

## 附录 B：术语表

| 术语 | 含义 |
|------|------|
| **BYOK** | Bring Your Own Key，消费者自带上游 key |
| **White list** | IP 白名单，控制 BYOK 准入 |
| **AutoCreate** | 自动创建临时 channel |
| **TTL** | Time To Live，未用自动清理 |
| **Provider Detection** | 启发式识别 key 对应的 provider |
