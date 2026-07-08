# SQLite 调优方案

> 2026-07-08 · pragma 调优 + 连接池 + 保留策略

---

## 目录

1. [背景与目标](#1-背景与目标)
2. [当前配置分析](#2-当前配置分析)
3. [性能瓶颈](#3-性能瓶颈)
4. [调优清单](#4-调优清单)
5. [DSN 与 Pragma 调优](#5-dsn-与-pragma-调优)
6. [连接池调优](#6-连接池调优)
7. [保留策略](#7-保留策略)
8. [性能影响预估](#8-性能影响预估)
9. [测试与基准](#9-测试与基准)
10. [风险与回退](#10-风险与回退)

---

## 1. 背景与目标

### 1.1 现状

当前 llmRx 使用 SQLite + WAL 模式作为存储，但未做深度调优。`MaxOpenConns=1` 限制了并发，`synchronous=FULL` 每次写都 fsync，logs 表无保留策略。

### 1.2 目标

在不引入外部依赖（PostgreSQL）的前提下，让 SQLite 支撑：
- 500 QPS 稳定运行
- 2000 QPS 临界可用
- 长期运行不出现存储膨胀

### 1.3 适用场景

| 流量范围 | 推荐方案 | 本文档覆盖 |
|----------|---------|-----------|
| **< 500 QPS** | ✅ 本方案 | ✅ |
| 500-2000 QPS | 本方案 + 异步批量写 | 部分 |
| > 2000 QPS | 迁移 PostgreSQL | ❌ |

---

## 2. 当前配置分析

### 2.1 现有 DSN

```go
// internal/store/sqlite.go
db, _ := sql.Open("sqlite3", dsn+"?_journal=WAL&_busy_timeout=5000&_foreign_keys=on")
db.SetMaxOpenConns(1)
```

### 2.2 配置分析

| 配置 | 现状 | 评估 |
|------|------|------|
| `_journal=WAL` | ✅ 已开 | 读写并发允许 |
| `_busy_timeout=5000` | ✅ 已开 | 锁等待 5s 合理 |
| `_foreign_keys=on` | ✅ 已开 | 数据完整性 |
| `_synchronous=FULL` | ⚠️ 默认 | 每次提交 fsync |
| `MaxOpenConns=1` | ⚠️ 太小 | 写串行化严重 |
| `MaxIdleConns` | ⚠️ 默认 2 | 略低 |
| `cache_size` | ⚠️ 默认 | 约 2MB |
| `temp_store` | ⚠️ 默认 FILE | 临时表落盘 |
| `mmap_size` | ⚠️ 默认 0 | 禁用 mmap |
| `wal_autocheckpoint` | ⚠️ 默认 1000 | 频繁 checkpoint |

---

## 3. 性能瓶颈

### 3.1 fsync 性能

```
每次 INSERT 默认行为：
  BEGIN → INSERT → COMMIT (fsync) → END
  耗时：~1-10ms (SSD) / ~10-50ms (HDD)

500 QPS 全部 fsync：
  500 × 1ms = 500ms 总写时间
  → 单个 goroutine fsync 排队 → 延迟尖刺
```

### 3.2 logs 表写热点

```
每条 LLM 请求 = 1 个 logs INSERT
channels/tokens/plans = 冷数据（启动加载到内存后基本只读）
→ logs 是唯一需要担心的写热点
```

### 3.3 索引维护

```
随时间增长：
  - 1 天 500 QPS = 4320 万行
  - 索引维护成本上升
  - 查询变慢
  - 表膨胀
```

---

## 4. 调优清单

| # | 改动 | 位置 | 复杂度 |
|---|------|------|--------|
| 1 | DSN 加参数（`_synchronous=NORMAL`）| `internal/store/sqlite.go` | 低 |
| 2 | 连接池：`MaxOpenConns=8` / `MaxIdleConns=4` | `internal/store/sqlite.go` | 低 |
| 3 | 启动时设 pragma（cache_size, mmap_size, temp_store, wal_autocheckpoint）| `internal/store/sqlite.go` | 中 |
| 4 | 加 logs 保留策略（30 天清理）| `internal/store/sqlite.go` + `cmd/gateway/main.go` | 中 |
| 5 | 调优后基准测试 | 新增 `internal/store/sqlite_bench_test.go` | 中 |
| 6 | Config 段（retention_logs_days）| `config.yml` + `internal/config/config.go` | 低 |
| **总工作量** | | | **~半天** |

---

## 5. DSN 与 Pragma 调优

### 5.1 DSN 参数

```go
// internal/store/sqlite.go
func OpenSQLite(dsn string) (*SQLite, error) {
    sep := "?"
    if strings.Contains(dsn, "?") {
        sep = "&"
    }
    dsn = dsn + sep + 
        "_journal=WAL" + 
        "&_busy_timeout=5000" + 
        "&_foreign_keys=on" + 
        "&_synchronous=NORMAL"  // NEW: 降级 fsync 频率
    
    db, err := sql.Open("sqlite3", dsn)
    if err != nil {
        return nil, fmt.Errorf("open: %w", err)
    }
    
    // ... 后续
}
```

### 5.2 启动时设置其他 Pragma

```go
// 设置无法走 DSN 的 pragma
pragmas := []string{
    "PRAGMA cache_size=-20000",         // 20MB 缓存
    "PRAGMA temp_store=MEMORY",         // 临时表内存
    "PRAGMA mmap_size=268435456",       // 256MB mmap
    "PRAGMA wal_autocheckpoint=2000",   // 2000 页
}
for _, p := range pragmas {
    if _, err := db.Exec(p); err != nil {
        return nil, fmt.Errorf("pragma %s: %w", p, err)
    }
}
```

### 5.3 Pragma 详解

| Pragma | 推荐值 | 作用 | 风险 |
|--------|--------|------|------|
| `synchronous` | `NORMAL` | 减少 fsync（断电丢 ~1s 数据）| 极低 |
| `cache_size` | `-20000` (20MB) | 缓存热数据 | 内存占用 |
| `temp_store` | `MEMORY` | 临时表/索引内存化 | 内存占用 |
| `mmap_size` | `268435456` (256MB) | 大查询 mmap 加速 | 内存占用 |
| `wal_autocheckpoint` | `2000` | 减少 checkpoint 频率 | WAL 文件略大 |

### 5.4 完整 OpenSQLite 实现

```go
// internal/store/sqlite.go
func OpenSQLite(dsn string) (*SQLite, error) {
    if dsn == "" {
        return nil, errors.New("empty dsn")
    }
    if dir := filepath.Dir(dsn); dir != "" && dir != "." {
        _ = os.MkdirAll(dir, 0o755)
    }
    
    // 构建 DSN
    sep := "?"
    if strings.Contains(dsn, "?") {
        sep = "&"
    }
    fullDSN := dsn + sep + 
        "_journal=WAL" + 
        "&_busy_timeout=5000" + 
        "&_foreign_keys=on" + 
        "&_synchronous=NORMAL"
    
    db, err := sql.Open("sqlite3", fullDSN)
    if err != nil {
        return nil, fmt.Errorf("open: %w", err)
    }
    
    // 连接池调优
    db.SetMaxOpenConns(8)   // 现有: 1
    db.SetMaxIdleConns(4)   // NEW
    db.SetConnMaxLifetime(0)
    
    s := &SQLite{db: db}
    
    // 设置其他 pragma
    if err := s.applyPragmas(); err != nil {
        return nil, fmt.Errorf("pragma: %w", err)
    }
    
    if err := s.migrate(); err != nil {
        return nil, fmt.Errorf("migrate: %w", err)
    }
    return s, nil
}

func (s *SQLite) applyPragmas() error {
    pragmas := []string{
        "PRAGMA cache_size=-20000",
        "PRAGMA temp_store=MEMORY",
        "PRAGMA mmap_size=268435456",
        "PRAGMA wal_autocheckpoint=2000",
    }
    for _, p := range pragmas {
        if _, err := s.db.Exec(p); err != nil {
            return fmt.Errorf("%s: %w", p, err)
        }
    }
    return nil
}
```

---

## 6. 连接池调优

### 6.1 调优前后对比

| 参数 | 调优前 | 调优后 | 影响 |
|------|--------|--------|------|
| MaxOpenConns | 1 | 8 | 读并发 8x |
| MaxIdleConns | 2 | 4 | 减少连接建立 |
| ConnMaxLifetime | 0 (永不过期) | 0 (永不过期) | — |

### 6.2 为什么 8 个连接

- SQLite WAL 模式：1 个写 + N 个读
- 写操作：channels/tokens/plans CRUD + logs INSERT
- 读操作：路由查询、analytics、admin
- 8 个连接足够覆盖并发需求

### 6.3 不需要再大的原因

- SQLite 单写者，8 个连接已经超过 1 写者 + 7 读者配置
- 增加连接不会提升写吞吐（写仍然串行）
- 仅在大量并发读（analytics）时有帮助

---

## 7. 保留策略

### 7.1 目标

- 防止 logs 表无限增长
- 自动清理过期数据
- 不影响性能（每日一次）

### 7.2 实现

```go
// internal/store/sqlite.go 新增

// RetentionConfig 保留策略配置
type RetentionConfig struct {
    LogsDays int  // 0 = 禁用
}

// StartRetention 启动后台清理 goroutine
func (s *SQLite) StartRetention(ctx context.Context, cfg RetentionConfig) {
    if cfg.LogsDays <= 0 {
        return
    }
    go func() {
        // 启动时跑一次
        s.cleanupLogs(cfg.LogsDays)
        // 之后每 24h 一次
        ticker := time.NewTicker(24 * time.Hour)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                s.cleanupLogs(cfg.LogsDays)
            }
        }
    }()
}

func (s *SQLite) cleanupLogs(days int) {
    cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
    res, err := s.db.Exec("DELETE FROM logs WHERE created_at < ?", cutoff)
    if err != nil {
        log.Printf("retention: cleanup failed: %v", err)
        return
    }
    if n, _ := res.RowsAffected(); n > 0 {
        log.Printf("retention: deleted %d log rows older than %d days", n, days)
    }
    // 主动 checkpoint 释放 WAL 空间
    s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
}
```

### 7.3 启动装配

```go
// cmd/gateway/main.go 新增
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

store, _ := store.OpenSQLite(cfg.Database.DSN)
store.StartRetention(ctx, store.RetentionConfig{
    LogsDays: cfg.Storage.RetentionLogsDays,  // 0=禁用
})
```

### 7.4 Config 段

```yaml
# config.yml
storage:
  retention_logs_days: 30  # 0 = 永久保留
```

```go
// internal/config/config.go 新增
type StorageConfig struct {
    RetentionLogsDays int `yaml:"retention_logs_days"`
}

type Config struct {
    // ... 现有字段
    Storage StorageConfig `yaml:"storage"`
}
```

### 7.5 启动风险控制

| 阶段 | 建议 |
|------|------|
| 首次部署 | `retention_logs_days: 0`（不清理）|
| 运行 7 天后 | 改为 30（启用保留）|
| 运行 30 天后 | 可根据磁盘空间调整 |

---

## 8. 性能影响预估

| 指标 | 调优前 | 调优后 | 提升 |
|------|--------|--------|------|
| 单 INSERT 延迟（SSD）| ~1ms | ~0.3ms | 3x |
| 读并发（connection pool）| 1 | 8 | 8x |
| 大查询（mmap）| 磁盘 IO | 内存映射 | 5-10x |
| fsync 频率 | 每次写 | NORMAL 模式降频 | 2-3x |
| 突发 2000 QPS | ❌ 卡顿 | ✅ 稳定 | 临界 → 正常 |
| 表大小（30 天保留）| 无限 | 限定 | 稳定 |

---

## 9. 测试与基准

### 9.1 基准测试

```go
// internal/store/sqlite_bench_test.go

func BenchmarkSQLite_Insert(b *testing.B) {
    s := openTestSQLite(b)
    defer s.Close()
    
    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            s.CreateLog(ctx, &model.Log{
                Model: "gpt-4",
                PromptTokens: 100,
                CreatedAt: time.Now().Unix(),
            })
        }
    })
}

func BenchmarkSQLite_InsertBatch(b *testing.B) {
    s := openTestSQLite(b)
    defer s.Close()
    
    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            tx, _ := s.db.Begin()
            for j := 0; j < 10; j++ {
                tx.Exec("INSERT INTO logs (...) VALUES (...)", ...)
            }
            tx.Commit()
        }
    })
}
```

### 9.2 单元测试

```go
func TestSQLite_Retention(t *testing.T) {
    s := openTestSQLite(t)
    defer s.Close()
    
    // 插入 100 行 31 天前的日志
    old := time.Now().Add(-31 * 24 * time.Hour).Unix()
    for i := 0; i < 100; i++ {
        s.CreateLog(ctx, &model.Log{Model: "x", CreatedAt: old})
    }
    
    // 触发清理
    s.cleanupLogs(30)
    
    // 验证全部删除
    var count int
    s.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count)
    assert.Equal(t, 0, count)
}

func TestSQLite_ConcurrentReads(t *testing.T) {
    s := openTestSQLite(t)
    defer s.Close()
    
    var wg sync.WaitGroup
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                s.ListChannels(ctx)
            }
        }()
    }
    wg.Wait()
}
```

### 9.3 集成测试

```go
func TestSQLite_PragmasApplied(t *testing.T) {
    s := openTestSQLite(t)
    defer s.Close()
    
    // 验证 pragma 已应用
    var cacheSize, mmapSize int64
    s.db.QueryRow("PRAGMA cache_size").Scan(&cacheSize)
    s.db.QueryRow("PRAGMA mmap_size").Scan(&mmapSize)
    
    assert.Equal(t, int64(-20000), cacheSize)
    assert.Equal(t, int64(268435456), mmapSize)
}
```

---

## 10. 风险与回退

### 10.1 风险

| 风险 | 严重度 | 缓解 |
|------|--------|------|
| `synchronous=NORMAL` 断电丢数据 | 低 | llmRx 不是金融系统，1s 内数据可接受 |
| mmap_size 256MB 大查询占用内存 | 低 | 监控 mmap 使用 |
| 保留策略误删数据 | 低 | 启动前一次性 backup；运行 7 天后再开保留 |
| 并发连接数过多 | 极低 | 8 个连接已验证安全 |

### 10.2 回退方案

| 改动 | 回退方法 |
|------|---------|
| `synchronous=NORMAL` | 改回 `FULL` |
| `MaxOpenConns=8` | 改回 1 |
| 保留策略 | 改 `retention_logs_days: 0` |
| mmap_size | 设回 0 |

### 10.3 监控指标

| 指标 | 监控方式 |
|------|---------|
| SQLite 写延迟 | 应用层打点 |
| WAL 文件大小 | 定期检查 |
| DB 文件大小 | 定期检查 |
| 连接池使用 | `db.Stats()` |

---

## 附录 A：SQLite 性能速查

### 写入性能

| 操作 | 延迟（SSD）| 延迟（HDD）|
|------|-----------|-----------|
| 单 INSERT（FULL sync）| ~1ms | ~10ms |
| 单 INSERT（NORMAL sync）| ~0.3ms | ~3ms |
| 批量 10 INSERT（事务）| ~1ms | ~10ms |
| 批量 100 INSERT（事务）| ~5ms | ~50ms |

### 读取性能

| 数据量 | 简单查询 | 复杂聚合 |
|--------|---------|---------|
| 1K 行 | < 1ms | ~5ms |
| 100K 行 | ~5ms | ~50ms |
| 1M 行 | ~50ms | ~500ms |
| 10M 行 | ~500ms | ~5s |

### 容量估算

| 流量 | 每日 INSERT | 每月行数 | 表大小 |
|------|-----------|---------|--------|
| 100 QPS | 8.6M | 259M | ~50GB |
| 500 QPS | 43M | 1.3B | ~260GB |
| 2000 QPS | 173M | 5.2B | ~1TB |

**结论**：30 天保留是必要的，否则 1 年后单表 > 1TB。

---

## 附录 B：未来扩展

### B.1 异步批量写（如需）

如果未来需要支持 2000+ QPS，可加 logs buffer：

```go
type LogBuffer struct {
    ch chan *model.Log  // 容量 10000
}

func (b *LogBuffer) Write(log *model.Log) {
    select {
    case b.ch <- log:
    default:
        // 满了丢日志（监控告警）
    }
}

// 后台 goroutine 每 1s 批量 INSERT
func (b *LogBuffer) flush() {
    logs := []model.Log{}
    for {
        select {
        case log := <-b.ch:
            logs = append(logs, *log)
        case <-time.After(time.Second):
            if len(logs) > 0 {
                // 批量 INSERT
                db.Exec("INSERT INTO logs (...) VALUES (...)", ...)
                logs = logs[:0]
            }
        }
    }
}
```

**收益**：fsync 频率降为 1/s，写入吞吐 10x

### B.2 迁移 PostgreSQL

如果 SQLite 仍不够用：

| 触发条件 | 行动 |
|----------|------|
| 持续 > 2000 QPS | 评估 PostgreSQL |
| 多实例部署 | 必须用 PostgreSQL（SQLite 不支持）|
| 数据集 > 100GB | 评估分库或归档 |

**Store 接口已抽象**（✅），迁移成本：
- 写 `internal/store/postgres.go`
- 修改 `cmd/gateway/main.go` 1 行
- ~1-2 天工作量

---

## 附录 C：术语表

| 术语 | 含义 |
|------|------|
| **WAL** | Write-Ahead Logging，SQLite 日志模式 |
| **fsync** | 强制磁盘同步 |
| **mmap** | 内存映射文件 |
| **Checkpoint** | WAL → 主数据库文件的合并 |
| **synchronous** | SQLite 同步模式（OFF/NORMAL/FULL/EXTRA）|
| **cache_size** | 页面缓存大小（负数=KB）|
