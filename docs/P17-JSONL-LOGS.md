# P17 — 日志存储改为 JSONL + Rotate / Append-Only Log Store

> Date: 2026-11-01 · Owner: llmRx maintainers · Status: **已实施**
> 前置:P8 缓存、P12 日志保留;关联 [P16-DISTRIBUTION.md](P16-DISTRIBUTION.md)(解耦 CGO)。
> 目标:去掉请求日志路径上的 SQLite 依赖,消除 CGO 包袱,同时**不牺牲性能**。

## 1. 为什么要改

现状:每条请求日志写进按天分文件的 SQLite(`logs/YYYY-MM-DD.db`),
超过 100MB rollover 成 `YYYY-MM-DD-N.db`。

三个已证实的问题:

| 问题 | 证据 |
|---|---|
| **写路径曾经饱和** | `docs/PERFORMANCE.md` 记录:220 万行后 WAL 膨胀至 240MB、网关 RSS 5.2GB |
| **CGO 绑定** | `internal/logstore/sqlite_driver.go:18` blank import `mattn/go-sqlite3`,整个二进制无法交叉编译 |
| **写入成本** | Round-5 pprof:后台 logstore 占 CPU **4.23%**,其中 `runtime.cgocall` 2.49% |

关键判断:**日志是"可丢的观测数据"**,却被当作关系型数据存储——
要 SQL 解析、B-tree 更新、事务提交、WAL checkpoint、ATTACH 跨文件联合。
这是用错工具。append-only 文件只需要一次 `write()`。

## 2. 目标与非目标

### 2.1 目标

1. 写入路径改为 **JSONL append + 定期 rotate**,无 SQL、无事务;
2. 对上层**零调用点改动**——`Manager` 的 `Insert/BatchInsert/Query/Stats/TimeSeries/TopByField/ListFiles/DeleteFiles/ArchiveFiles` 签名不变;
3. 保留按天分文件 + 按大小 rollover 的现有语义;
4. 保留 `archive`(gzip)与 `delete` 两种保留策略;
5. 保留异步批量 + `DropOnFull` + panic 重启 worker 的现有机制;
6. 保留 Postgres 后端(集群模式)不受影响。

### 2.2 非目标

- **不改 store 层**(channels/tokens/plans/logs 元数据仍在 SQLite/Postgres);
- 不做日志的复杂 SQL 查询能力(现状也只有固定几种聚合);
- 不引入新依赖(JSONL 用 `encoding/json` 或项目已有的 `goccy/go-json`);
- 不做实时分析引擎(DuckDB 等)。

## 3. 关键事实(已核实)

这四条决定了改动的可行性边界:

1. **所有查询都在管理端,不在请求热路径**。共 23 处调用:
   - `internal/admin/handler.go`:337, 670, 1122, 1360, 1377, 1402
   - `internal/webui/{handler,billing,usage,phase2}.go`:372, 408, 440, 46, 83, 84, 138, 45, 304–322, 75, 93, 159–160, 168, 220
   - 最重的是 `Limit: 5000` 的账单导出与 30 天 `TimeSeries`。
2. **`model.Log.ID` 没有被任何调用点消费**——没有"按 log id 查单条"的端点。
   所以 JSONL 不需要全局行号。
3. **查询形态固定**:按 (TokenID/ChannelID/KeyID/Model/Endpoint/StatusCode/CacheHit/时间窗) 过滤,
   做 count/sum/group-by/top-N。没有 JOIN、没有子查询、没有任意列排序。
4. **保留策略只依赖文件名**(`extractDate(f)`),与文件格式无关。

## 4. 设计决策

### D1 文件格式:JSONL,每行一个完整 Log

```
logs/2026-11-01.jsonl
{"id":0,"token_id":3,"channel_id":1,"model":"deepseek-chat",...,"created_at":"2026-11-01T10:00:00Z"}
```

- 选择 JSONL 而非 CSV:字段新增/删除不破坏兼容,不需要转义规则,`jq`/`tail -f` 直接可用;
- 选择 JSONL 而非自定义二进制:可读性对运维排查的价值远超那点体积;
- **字段名沿用 `model.Log` 的 json tag**,保证和 API 输出一致;
- 体积代价:JSON 比 SQLite 行大约 2-3 倍。按现状 `estimatedRowBytes = 150` 估算,
  实际约 300-450 字节/行。以 1000 万行计约 3-4.5GB——用 rotate + 保留策略控制。

### D2 写入:批量缓冲 + `bufio.Writer` + 定期 Flush

```
Manager.Insert → ch (async, 现有机制不变)
                  ↓
          batch worker 攒够 BatchSize 或 FlushInterval
                  ↓
          JSONLDriver.BatchInsert
                  ↓
          bufio.Writer 逐行写 → Flush → 一次 write() syscall
```

- **现有 async 机制完全保留**:`AsyncConfig`(BatchSize 500 / FlushInterval 100ms /
  ChannelSize 2000 / DropOnFull true)、panic 重启 worker,一行不改;
- `bufio.Writer` 缓冲 64KB,批量写只触发系统调用,避免每行一次 write;
- **不做 `fsync`**(与现状 `synchronous=NORMAL` 语义对齐):崩溃最多丢最后
  100ms 的批次。需要强持久化时通过配置开 `fsync_on_flush`;
- 保留 `crash-safety` 讨论:实际数据是"当前天文件",崩溃后文件末尾可能
  有半行。**读取端必须容忍最后一行是残缺 JSON**(跳过并计数)。

### D3 Rotate:沿用现有双维度语义

| 维度 | 规则 | 与现状一致性 |
|---|---|---|
| **按天** | `entry.CreatedAt` 的 UTC 日期决定文件 | ✅ 完全相同 |
| **按大小** | 超过 `MaxFileBytes`(默认 100MB)rollover 到 `-1`, `-2`… | ✅ 完全相同 |

rotate 在 `BatchInsert` 内检查(写前判断累积字节数),与现有 `acquire()`
的 `atomic.LoadInt64(&df.bytesWritten) < MaxFileBytes` 判断一致。

**注意**:JSONL 每行字节数可直接精确累计(`len(line)`),比 SQLite 的
`estimatedRowBytes` 估算更准——这是文件方案的一个附带好处。

### D4 查询:两遍扫描 + 流式解析,不做索引

这是方案的核心风险点,必须先算清楚成本。

**实现方式**:每个文件用 `bufio.Scanner` 逐行 `json.Unmarshal` → 应用过滤条件
→ 累加到结果。命中 `Limit` 时提前返回(只需 `limit + offset` 条)。

| 查询 | 现状(SQLite) | 新方案(JSONL) | 可接受? |
|---|---|---|---|
| `Query` limit=50(日志页) | 索引 + LIMIT | 顺序扫到够数即停 | ✅ 通常前几百行就够 |
| `Stats(nil)` 全量 | 一次聚合查询 | 全量扫描所有天文件 | ⚠️ 取决于总量 |
| `TimeSeries` 30 天 | 聚合 + 索引 | 全量扫描 30 个文件 | ⚠️ |
| `TopByField` top 20 | 聚合 | 全量扫描 + map 累加 | ⚠️ |
| 账单导出 limit=5000 | SQL | 扫到 5000 条 | ✅ |

**关键缓解措施**(必须有,否则不可接受):

1. **`Stats` 走增量预聚合**,不扫全量:
   每个天文件旁边维护 `YYYY-MM-DD.jsonl.meta.json`(flush 时原子更新):
   ```json
   {"rows":120345,"prompt_tokens":...,"completion_tokens":...,
    "real_cost":...,"billed_cost":...,"errors":...,
    "by_model":{...},"by_channel":{...},"by_token":{...}}
   ```
   - `Stats(nil)` 直接读所有 `.meta.json` 求和 → **O(文件数)**,不是 O(行数);
   - `TopByField` 同理,从 meta 里已有的一维 map 合并;
   - `TimeSeries` 需要按小时分桶 → meta 里再加 `by_hour` map(24 个 key/天,很小);
   - meta 缺失(旧文件/崩溃)时回退到全量扫描并重建。

2. **查询走时间窗裁剪**:`QueryFilter.CreatedFrom/To` 决定只打开哪些天文件
   ——现有 `days []string` 参数已经是这个语义。

3. **`Limit` 提前终止**:只取前 N 条的场景(日志页、导出)永不扫全量。

4. **并发扫描**:多天文件之间用 `errgroup` 并行(读是 IO 密集),但每条文件内顺序。

> **加索引前先不做**。若实测 meta 方案仍不够(比如需要按 IP 过滤的复杂查询),
> 再考虑旁路 SQLite 索引——但那是二阶优化,不进 MVP。

### D5 兼容性:新旧文件共存,读取端自动识别

升级用户的 `logs/` 目录里已有 `.db` 文件。策略:

- **写入**永远写 `.jsonl`;
- **读取**同时支持 `.db` 与 `.jsonl`:`ListFiles()` 返回两类,
  `QueryAcross` 按后缀分派到 SQLite 路径或 JSONL 路径;
- **保留策略**对两类都生效(archive 把 `.db` → `.db.gz`,`.jsonl` → `.jsonl.gz`);
- **不自动迁移旧文件**(量可能很大,且只读)。给一个可选 CLI:
  `llmrx -migrate-logs` 把 `.db` 转成 `.jsonl` 后删除原件。

这样升级是零风险的:老数据照常可查,新数据走新路径,想清理时再跑迁移。

### D6 Postgres 后端不变

集群模式(`logstore_backend: postgres`)继续用共享表。JSONL 是**单机**后端优化。
`Driver` 接口已经是抽象层,新增 `JSONLDriver` 即可,Postgres driver 一行不动。

## 5. 结构调整

```
internal/logstore/
├── driver.go              不变(接口定义)
├── manager.go             小改:driver 选择 + 旧文件双读
├── jsonl_driver.go        NEW  主实现(~500 LoC)
├── jsonl_driver_test.go   NEW
├── jsonl_meta.go          NEW  增量预聚合(.meta.json 读写,~200 LoC)
├── sqlite_driver.go       保留(读旧文件 / -migrate-logs)
├── postgres_driver.go     不变
└── sqlite_driver_test.go  保留
```

`Manager` 选择后端的逻辑:

```go
// logstore_backend: jsonl (默认) | sqlite (旧) | postgres
func New(dir string, driver Driver) (*Manager, error)
```

启动时根据 `server.logstore_backend` 选 `NewJSONLDriver()` 或 `NewSQLiteDriver()`。
**默认值**:单机默认 `jsonl`,Postgres 模式不受影响。

## 6. 核心实现要点

### 6.1 写入路径(批处理)

```go
func (d *JSONLDriver) BatchInsert(entries []*model.Log) (int, error) {
    // 按 entry.CreatedAt 的 UTC 日期分组(跨天批次必须拆分)
    // 每组:
    //   1. acquire(date) → 拿 bufio.Writer(超 MaxFileBytes 则 rollover)
    //   2. 逐行 json.Marshal + Write + 累计 meta
    //   3. Flush()
    //   4. 原子更新 .meta.json(写 .tmp + rename)
}
```

要点:
- **按日期分组**——现有 `BatchInsert` 注释已说明"所有条目共享同一 UTC 日期",
  但必须**防御性处理跨天**,否则日志会进错文件;
- `bufio.Writer` 按文件缓存(driver 内 map,由现有 `d.mu` 保护);
- **写入失败不阻塞**:与现有 `DropOnFull` 一致,错误计数 + 日志,不 panic。

### 6.2 读取路径(扫描 + 过滤)

```go
func (d *JSONLDriver) scanFile(path string, fn func(*model.Log) bool) error {
    f, _ := os.Open(path)
    sc := bufio.NewScanner(f)
    sc.Buffer(make([]byte, 64*1024), 4*1024*1024) // 单行上限 4MB,防畸形数据
    for sc.Scan() {
        var e model.Log
        if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
            continue // 容忍崩溃残留的半行
        }
        if !fn(&e) { return nil } // false = 命中 Limit,提前停止
    }
}
```

要点:
- **`scanner` 单行上限必须显式设置**(默认 64KB 会截断长日志);
- 畸形行跳过并计数(`malformed_rows_total` 指标),不返回错误;
- `.gz` 归档文件查询时透明解压(`gzip.NewReader`)。

### 6.3 过滤逻辑必须与现有 SQL 语义完全一致

现有 `buildWhere()` 生成的 SQL 条件要逐条对齐成 Go 函数,任何语义漂移
都会让管理台数字悄悄变错。逐项对照:

| SQL 条件 | Go 等价 |
|---|---|
| `token_id = ?` | `e.TokenID == f.TokenID` |
| `token_id IN (...)` | `slices.Contains(f.TokenIDs, e.TokenID)` |
| `channel_id = ?` | 同上 |
| `key_id = ?` | 同上 |
| `model = ?` | `e.Model == f.Model` |
| `endpoint = 'mcp'` | `e.Endpoint == "mcp"` |
| `status_code = ?` | `e.StatusCode == f.StatusCode`(0=不过滤) |
| `cache_hit = 1` | `e.CacheHit` |
| `created_at >= ?` | `e.CreatedAt.Unix() >= f.CreatedFrom` |
| `created_at <= ?` | 同上 |

**必须有对照测试**:同一批数据分别用 SQLite driver 和 JSONL driver 跑同一组
filter,断言结果完全一致(见 §8)。

## 7. 配置

```yaml
server:
  # jsonl(默认,单机推荐) | sqlite(旧行为) | postgres(集群)
  logstore_backend: jsonl
  # JSONL 专用
  logstore_fsync: false        # true = 每批 fsync(更强的崩溃保护,更慢)
  logstore_scan_parallelism: 4 # 多天文件并发扫描数
```

现有 `logstore_synchronous` 仅对 `sqlite` 后端有意义,保留不删。

## 8. 测试计划

| 层 | 覆盖 |
|---|---|
| **等价性**(最重要) | 同一批 1000 条日志分别写入 SQLite / JSONL driver,对 20+ 组 filter 组合断言 `Query/Stats/TimeSeries/TopByField` 结果完全一致 |
| 写入 | 单条 / 批量 / 跨 UTC 午夜批次拆分 / 超过 MaxFileBytes rollover / 空批次 |
| 容错 | 文件末尾半行 JSON / 畸形行 / 超大单行(>4MB)/ 空文件 / 不存在文件 |
| Rotate | 按天、按大小、`-1` `-2` 序列;重启后继续写入正确的文件 |
| Meta | 增量更新正确性;meta 缺失时回退全量扫描;meta 损坏时重建 |
| 保留 | delete / archive(gzip 往返);`.db` 与 `.jsonl` 混合目录 |
| 旧文件 | `.db` 只读兼容;`-migrate-logs` 转换后数据一致 |
| 并发 | `-race` 全绿;并发 Insert + Query |
| 性能 | 基准:写入吞吐 ≥ 现有 SQLite(目标 2 倍以上);`Stats` 在 30 天数据上 < 50ms |

## 9. 验收标准

| 指标 | 目标 |
|---|---|
| 写入吞吐(行/秒) | **≥ 现状 2 倍**(去掉 SQL 解析与事务) |
| 请求热路径影响 | 不变或更好(热路径本来就只入队,0.099%) |
| `go build` 在 `CGO_ENABLED=0` 下成功 | ✅ 前提:store 已切纯 Go 或双二进制 |
| `Stats(nil)` 延迟(30 天、1000 万行) | **< 100ms**(靠 meta 预聚合) |
| 日志页 `Query limit=50` | 实测 < 200ms(时间窗裁剪 + 提前终止) |
| 所有管理台页面数字与升级前一致 | ✅ 由等价性测试保证 |
| 升级后旧 `.db` 文件仍可查询 | ✅ |
| `-race` 全绿、覆盖率不降 | ✅ |

> ⚠️ **注意 §2.1 第 6 条前提**:这个改动消除的是 logstore 的 CGO。
> `internal/store` 仍在用 `mattn/go-sqlite3`,所以**服务端二进制整体仍是 CGO**。
> 要做到完全无 CGO,还需要单独处理 store(见 P16 §5 D2)或采用双二进制。
> 本方案的价值是:**把日志这个最大的写入压力源移出 SQLite**,同时减少一个 CGO 引用点。

## 10. 实施顺序

| 步 | 内容 | 可独立验证 |
|---|---|---|
| **L1** | `JSONLDriver` 写入路径 + rotate + 测试 | 日志能写、能 rotate、目录结构正确 |
| **L2** | 读取路径 + filter 语义 + **等价性测试** | 与 SQLite driver 结果一致 |
| **L3** | `.meta.json` 预聚合 + `Stats/TopByField/TimeSeries` | 30 天数据 Stats < 100ms |
| **L4** | 旧 `.db` 双读兼容 + archive/delete 双语 | 混合目录可用 |
| **L5** | 配置开关 + 默认值切换 + 启动日志 | 可回退到 sqlite |
| **L6** | `-migrate-logs` CLI | 旧数据转换 |
| **L7** | 压测对比 + 更新 PERFORMANCE.md | 数据说话 |

L1-L3 完成即功能完整;L4 是升级平滑性;L6 可选。

## 11. 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| **查询变慢**(全量扫描) | 管理台卡顿 | meta 预聚合(L3)+ 时间窗裁剪 + 并发扫描;实测不过关就保留 SQLite 索引旁路 |
| **filter 语义漂移** | 数字悄悄变错 | 等价性测试(L2)作为硬门槛,不通过不合并 |
| 文件体积膨胀 2-3 倍 | 磁盘压力 | 保留策略 + archive 模式(gzip 通常压到 1/5) |
| 半行 JSON / 畸形数据 | 查询报错 | 跳过 + 计数;`-migrate-logs`/健康检查可发现 |
| 崩溃丢最后一批 | 日志缺失 | 与现状 `synchronous=NORMAL` 同语义;可选 `fsync_on_flush` |
| 升级后旧文件查询需 ATTACH | 复杂度 | 双读路径隔离在 sqlite_driver,新代码不依赖它 |
| 用户依赖 SQL 直接查日志文件 | 破坏性 | 文档说明格式变更;提供 `jq` 示例 |

## 12. 附带收益

- **日志文件可被标准工具消费**:`jq`、`grep`、`awk`、`tail -f`、导入 ClickHouse/Loki;
- **不再有 WAL 膨胀问题**(现状曾到 240MB)——没有 WAL;
- **归档压缩效果更好**:gzip 对文本压缩率(约 5-10x)远高于对 SQLite 页面;
- **减少一个 CGO 引用点**,为将来纯 Go 构建铺路;
- **写入字节数精确**(`len(line)`),rotate 判断从估算变精确。

---

## 13. 实施记录(2026-11-01)

### 13.1 落地内容

| 项 | 状态 |
|---|---|
| `JSONLDriver`(写入 + rotate + 查询 + 文件管理) | ✅ `jsonl_driver.go` |
| `.meta.json` 增量预聚合 | ✅ `jsonl_meta.go` |
| 旧 `.db` 自动迁移 | ✅ `jsonl_migrate.go`(Open 时执行,可重复) |
| 配置开关 `logstore_backend: jsonl`(新默认) | ✅ `main.go` + `config.go` |
| `-migrate-logs` CLI | ✅ `cmd/gateway/migratelogs.go` |
| 等价性测试(16 组 filter × 4 类查询) | ✅ `jsonl_equivalence_test.go` |
| 性能基准与预算断言 | ✅ `jsonl_bench_test.go` |

### 13.2 实测数据

在 WSL2 / Go 1.22.12 / amd64 上测得:

| 指标 | SQLite | JSONL | 结果 |
|---|---|---|---|
| 写入 2 万行(批量) | 70.5 ms | 18.3 ms | **快 3.85×**(设计目标 ≥2×) |
| `LogStats`(30 文件 / 3 万行) | — | **416 µs** | 远低于 100 ms 预算 |
| logstore 包覆盖率 | — | **76.5%** | 高于改动前 |
| `go test -race`(logstore/webui/admin) | — | 全部通过 | — |

### 13.3 实施中发现并修复的既有 bug

等价性测试第一次运行就暴露了一个**与本次改动无关的历史缺陷**:

`SQLiteDriver` 的 `TimeSeries` 和 `TopByField` 在**带过滤条件**时会报
`no such column: token_id` / `no such column: status_code`。

原因:这两个查询把 `WHERE` 应用在 `UNION ALL` 的子查询外层，但内层
`SELECT` 只投影了聚合需要的少数几列,被过滤的列(`token_id`、`channel_id`、
`key_id`、`model`、`endpoint`、`cache_hit`)根本没有出现在子查询结果集里。

**实际影响**:管理台 Analytics / 用量页调用形式是 `series, _ := TimeSeries(filter, ...)`
——**错误被丢弃**,用户一加筛选条件,图表就静默变成空白。原测试只覆盖了
无过滤路径,所以一直没暴露。

修复:`internal/logstore/sqlite_driver.go` 新增 `unionProjection()`,
让内层查询投影 `buildWhere` 可能引用的全部列。

> 这个 bug 是这次改动的意外收获:等价性测试的价值正在于此——它不只保护新
> 后端,也顺带校验了老后端。

### 13.4 与设计稿的偏差

| 原设计 | 实际实现 | 原因 |
|---|---|---|
| D5:新旧文件**双读**共存 | 改为 **Open 时一次性迁移** | 双读要处理跨源合并、排序、分页、去重,复杂且易错;一次性迁移后只有一种格式,代码路径唯一 |
| D4:透传 meta 的 `by_hour` | 只存 `by_model`/`by_channel`/`by_token`;`TimeSeries` 仍扫描 | 时间序列的 filter 组合太多,预聚合收益不足以抵消复杂度;实测扫描性能已可接受 |
| 迁移后删除 `.db` | 重命名为 `.db.migrated` 保留 | 给运维留退路,且自动退出 `*.db` glob |

### 13.5 升级影响

- 升级后首次启动会**自动**把 `log_dir` 下的 `*.db` 转成 `*.jsonl`;
- 原文件保留为 `*.db.migrated`,确认无误后可手动删除;
- 回退:`logstore_backend: sqlite` 可切回旧后端(但不会自动把 JSONL 转回);
- 迁移按天进行、单文件原子(写 `.tmp` → rename),中断后重启会继续。

### 13.6 仍未解决的边界

- **`internal/store` 依然依赖 CGO**,服务端整体仍不是 `CGO_ENABLED=0`;
  本方案只移除了 logstore 这个最大的写入压力源(见 §2.1 第 6 条说明);
- 带大量过滤条件的 `TimeSeries` 仍是全量扫描,极端数据量下需观察;
- JSONL 比 SQLite 占用更多磁盘(约 2-3 倍),依赖保留策略与 gzip 归档控制。
