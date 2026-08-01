# P12 — 集群模式 / 多节点同步

## 1. Why now

llmRx 单机版功能闭环（P8-P13）已完成，但所有状态都是**进程内**：

| 状态 | 现状 | 多节点问题 |
|---|---|---|
| token RPM/TPM 限额 | internal/ratelimit 内存滑窗 | 每节点独立计数，限额被放大 N 倍 |
| spend 记账 | SQLite tokens.used_usd 原子 UPDATE | 多写者争锁 + 单写瓶颈 |
| plan 预算 gate | SQLite 读取 + 进程内 | 同上 |
| 配置 runtime_settings | SQLite 单行 | DB 共享天然可行 |
| 响应缓存 | 内存 backend（P8） | 命中率 N 节点独立，重复上游调用 |
| 请求日志 | 本地按日 SQLite 文件 | 无聚合视图 |
| MCP client / stdio 子进程 | 节点本地 | token 状态节点本地（可接受） |
| broker（SSE 日志流/告警） | 进程内 internal/broker | 跨节点无订阅 |

目标：K8s 多副本横向扩展，限额/花费/配置全局一致，日志可聚合。

## 2. Scope

| Feature | In scope |
|---|---|
| 外置存储（Postgres 或保留 SQLite+共享卷） | 决策点 1 |
| 分布式限额（Redis 或 Postgres 原子） | 决策点 2 |
| 共享缓存（Redis backend 复用 P8 cache 接口） | ✅ |
| 日志聚合（中央表或对象存储） | 决策点 4 |
| 无状态 API 节点拓扑 | 决策点 3 |
| 配置天然共享（runtime_settings 已在 DB） | ✅ 验证即可 |

Out of scope：跨节点 SSE broker 订阅（告警/实时日志流，量小，
后续 P12.1）、多区域部署、自动故障转移（依赖 K8s 自愈）。

## 3. 决策点（必须先拍板）

### D1 数据库
- **A. Postgres 外置（推荐）**：internal/store/postgres.go 已有
  skeleton，补齐 CRUD；spend/log/配置全迁。零锁争议，天然多写。
  代价：迁移工作量（store 接口 60+ 方法）、部署加 Postgres 依赖。
- **B. SQLite + 共享卷（WAL 只读副本）**：主节点写，副本只读。
  代价：只有 1 个写节点，谈不上水平扩展，违背 P12 初衷。

**推荐 A。**

### D2 分布式限额
- **A. Redis（推荐）**：ratelimit 加 Redis 后端（滑窗 Lua：
  ZADD + ZREMRANGEBYSCORE + EXPIRE，原子）；本地桶做缓存预扣、
  Redis 做权威。cache 接口同时复用 Redis backend（P8）。
  代价：新依赖 go-redis。
- **B. 纯 Postgres**：SELECT used FROM buckets WHERE key=$1 FOR UPDATE
  行锁滑窗。零新依赖但每请求一次 DB 往返，热点 token 有锁竞争。

**推荐 A**（一个 Redis 同时解决限额 + 缓存）。

### D3 拓扑
- **A. 无状态副本（推荐）**：N 个 llmRx API 副本直连
  Postgres/Redis，K8s Service 负载均衡。无独立存储节点概念。
- **B. API 节点 + 存储节点分离**：存储节点串行化写路径，多一层运维。

**推荐 A**——Postgres 原子 UPDATE + Redis 原子计数已足够，
不需要串行化节点。

### D4 日志
- **A. 中央日志表（推荐）**：logstore 加 Postgres 后端
  （INSERT 攒批、SELECT 走索引 created_at）。
  代价：logstore 抽象层改动（Driver 接口已有，加 PostgresDriver）。
- **B. 本地文件 + 对象存储归档**：保留现状 + 周期上传。
  代价：实时查询只能按节点。

**推荐 A**（Driver 接口就是为多后端设计的，manager.go 不改）。

## 4. Design

### 4.1 总体拓扑（D1A + D2A + D3A + D4A）

```
            ┌─────────────── K8s Service ───────────────┐
            │                                           │
   /v1 ──►  ├── llmRx pod A ──┐                        │
   /admin ─►├── llmRx pod B ──┼──► Postgres (store+logs+spend)
            ├── llmRx pod C ──┘        ▲
            └──────────────────────────┼──► Redis (limit+cache)
                                       │
                               runtime_settings 共享 (已有)
```

- 所有节点无状态；写路径全走 Postgres 原子操作 / Redis 原子计数。
- 配置（runtime_settings）天然共享。
- 启动时 addColumnIfMissing 迁移只在 Postgres 上跑（幂等）。

### 4.2 里程碑

#### M1 外置存储 Postgres（≈1 周）
- internal/store/postgres.go：从 skeleton 补齐。**关键决策**：
  - 策略一（推荐）：Postgres 为唯一存储，SQLite 代码保留但运行时
    二选一（dsn 前缀 postgres:// 选 Postgres）。
  - 策略二：双写迁移窗口（SQLite+Postgres 同时写，校验一致后
    切 Postgres）——风险高、工作量大，不推荐。
- store.Store 接口（60+ 方法）逐组迁移：channels/keys/tokens/plans/
  users/alerts/guardrails/byok/providers/combos/mcp/runtime。
- 与 SQLite 的差异点：
  - AUTOINCREMENT → SERIAL/BIGSERIAL
  - INTEGER boolean → BOOLEAN
  - TEXT 数组（models/intents）→ JSONB + app 层编解码（复用
    encodeStrings/decodeStrings）
  - ON DELETE CASCADE 语法一致
  - addColumnIfMissing → ALTER TABLE ... ADD COLUMN IF NOT EXISTS
- 测试：postgres_test.go 用 testcontainers 或本机 PG 跑 CRUD 全量
  （CI 加 Postgres service）。

#### M2 分布式限额 Redis（≈3-5 天）
- internal/ratelimit 抽接口：
  ```go
  type Backend interface {
      Allow(key int64, rpm, tpm, promptTokens int) (ok bool, reason string)
  }
  ```
  现有内存实现 → MemoryBackend（现状）；新增 RedisBackend。
- RedisBackend 用 Lua 原子滑窗：
  - 请求计数 ZSET(key, now, member=now:rand) + ZREMRANGEBYSCORE
  - 令牌计数同 ZSET（score=now, value=tokens）
  - EXPIRE 60s
- 双读路径：Allow 先查本地桶（预扣）→ Redis 权威；Redis 不可用
  降级本地桶（fail-open + 日志），与 P8 缓存降级一致。
- 测试：redis_test.go（可用 skip 标记，无 Redis 时 t.Skip）。

#### M3 共享缓存 Redis（≈2-3 天）
- internal/cache 加 RedisCache（P8 接口已抽象：Get/Set/Delete/
  Purge/Stats/Close）。
- 键加前缀 llmrx:cache:；TTL 透传；Purge 用 SCAN 按前缀删。
- server.go 的 cache_backend 配置加 redis（host/port/password/db）。
- 测试：复用 P8 的 cache 测试套件（接口一致，换 backend）。

#### M4 日志聚合（≈3-5 天）
- internal/logstore 加 PostgresDriver（实现 Driver 接口）：
  - Insert/BatchInsert → INSERT INTO logs（攒批复用 async worker）
  - QueryAcross → SELECT + WHERE（QueryFilter 已抽象）
  - LogStats/TimeSeries/TopByField → SQL 聚合（SQLite 版已有，
    语法差异小）
  - ListFiles/DeleteFiles → 保留逻辑（Postgres 版返回空/按
    retention DELETE）
- logs 表加索引（created_at, token_id, channel_id）。
- 配置：logstore.backend = sqlite|postgres。
- 测试：postgres_driver_test.go（可 skip）。

#### M5 无状态化收尾 + 部署（≈2-3 天）
- 审计所有进程内状态，确认仅剩：router 的 breaker/Thompson
  posterior（可接受：路由是尽力而为，节点间不一致无害）、
  tokencache（数据来自 DB，重启重建）、MCP client 缓存（同上）、
  guardrail 引擎缓存（Reload 读 DB）。
- K8s：deploy/helm/llmrx 加 Postgres + Redis 依赖（可选子 chart
  或 values 指向外部实例）；deployment 副本数参数化。
- 文档：docs/OPERATIONS.md 加集群章节（拓扑、故障、扩缩容）。

## 5. Config（新增）

```yaml
store:
  driver: sqlite | postgres      # 默认 sqlite（单机不变）
  postgres_dsn: postgres://user:pass@host:5432/llmrx
redis:
  addr: redis:6379
  password: ""
  db: 0
cache:
  backend: memory | redis        # 新增 redis
logstore:
  backend: sqlite | postgres     # 新增 postgres
ratelimit:
  backend: memory | redis        # 新增 redis
```

单机默认值全部 memory/sqlite，行为与现状完全一致（向后兼容）。

## 6. Tests

- store：Postgres 全量 CRUD 对照 SQLite 语义（CI 加 PG service）。
- ratelimit：Redis 滑窗正确性 + 降级路径（无 Redis 时 skip）。
- cache：Redis backend 复用 P8 测试套件。
- logstore：PostgresDriver 与 SQLiteDriver 结果一致性（同数据断言）。
- 集成：一个 testcontainers 起 PG+Redis，跑端到端（限额跨"节点"
  = 两个 Handler 实例共享 Redis 计数）。

## 7. Acceptance criteria

| Metric | Target |
|---|---|
| N 节点限额全局一致（RPM/TPM/spend） | ✅ |
| N 节点共享缓存命中 | ✅ |
| 日志中央查询（/admin/logs + analytics） | ✅ |
| 配置单点修改全节点生效 | ✅ |
| 单机默认配置行为不变 | ✅ |
| 全量 go test + go vet 干净 | ✅ |

## 8. Files to add / touch（按里程碑）

```
M1: internal/store/postgres.go (+ 分组 *_pg_test.go), go.mod (+ pgx)
M2: internal/ratelimit/backend.go, redis_backend.go, redis_test.go
M3: internal/cache/redis.go, redis_test.go, internal/server/server.go
M4: internal/logstore/postgres_driver.go, postgres_driver_test.go
M5: internal/server/server.go, deploy/helm/llmrx/values.yaml,
    docs/OPERATIONS.md
```

## 9. New dependencies

- github.com/jackc/pgx/v5（Postgres 驱动）
- github.com/redis/go-redis/v9（Redis 客户端）
- CI：Postgres + Redis service

## 10. Rollout

1. M1 外置存储（PG 全量 CRUD + 切换开关）。
2. M2 分布式限额（Redis 滑窗 + 降级）。
3. M3 共享缓存。
4. M4 日志聚合。
5. M5 无状态审计 + Helm + 文档。

每个 M 独立可交付、可回滚（后端可切回 memory/sqlite）。

## 11. Risks

| Risk | Mitigation |
|---|---|
| PG 迁移量大 | 分 store 方法组迁移；每组合并请求即可上生产 |
| Redis 单点故障 | K8s 多副本 + 降级到本地桶/内存 cache（fail-open） |
| 双写不一致窗口 | 避免策略二；直接切换，灰度前先在 staging 跑满 1 天 |
| 延迟增加（DB 往返） | 限额走 Redis（亚毫秒）；PG 连接池调优（SetMaxOpenConns） |
