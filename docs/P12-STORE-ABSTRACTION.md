# P12 — Store 抽象层（Database Abstraction）

## 1. 动机与目标

llmRx 将引入 Postgres 外置存储（P12-CLUSTER M1），后续可能接入
ClickHouse（日志）、MySQL（用户自带后端）等。当前 store 层
（`internal/store/sqlite.go`，2235 行）每个方法手写 SQLite 方言
SQL，postgres.go 是 88 方法全 `errNotImplemented` 的 skeleton——
**每加一个新后端 = 重写 88 个方法的 SQL + 全套测试**。

目标（已拍板，路线 B）：
1. 方言差异收敛到独立 `internal/dialect` 包，新后端适配 = 实现
   `Dialect` + 跑共享测试套件；
2. **SQLite 也回填重构走 Dialect**（全库统一，不允许两套 SQL
   风格长期并存）；
3. 共享测试套件覆盖 store 接口 **88 方法全量**（不是核心组子集），
   新后端跑一遍套件即验证语义正确；
4. SQLite 行为**零变化**（重构是纯提取，回归由全量测试兜底）。

## 2. 抽象边界（必须认清）

```
SQL OLTP 族（可进 store 抽象）        OLAP 异族（不进 store 抽象）
├─ SQLite（现状）                    └─ ClickHouse：列存、MergeTree
├─ Postgres（M1）                      异步合并、无行级 UPSERT/事务语义
└─ 未来 SQL 系（MySQL 等）              → 仅作 logstore 后端（日志是
                                         追加写，无 OLTP 语义）
```

- store 抽象只覆盖"行级 CRUD + 事务 + 自增"语义一致的数据库。
- ClickHouse 进 store 抽象会变成"接口统一、实现全是特判"的伪抽象。
- ClickHouse 是否作为 logstore 后端（`logstore.backend=clickhouse`）
  属于 P12 M4 的可选交付，**本设计不为其预留 store 接口**。

## 3. 勘察证据：sqlite.go 实际 SQL 模式（决定 Dialect 形态）

对 sqlite.go（2235 行）全量勘察结论：

| 模式 | 现状 | 对 Dialect 的影响 |
|---|---|---|
| 占位符 | 全部 `?`（82 处），无 `$N` | **必须方言化**（PG 用 `$N`，`?` 在 pgx 下是 JSON 操作符） |
| 标识符引用 | **零引号**（无反引号/双引号） | **省掉**：统一不引用标识符 |
| 时间 | `toUnix(t) = t.Unix()` 统一 Unix int | **省掉**：PG 列建 `BIGINT`，读写零转换 |
| UPSERT | `ON CONFLICT(...) DO UPDATE SET x = excluded.x`（2 处） | **省掉**：SQLite 3.24+/PG 语法完全一致 |
| 自增 | `INTEGER PRIMARY KEY AUTOINCREMENT`（建表）+ `LastInsertId`（13 处） | **必须方言化**：PG 用 `BIGSERIAL` + `RETURNING id` |
| 布尔 | `status/enabled/is_default INTEGER 0/1`，scan 到 `*int` | **必须方言化**：PG 用 `BOOLEAN`，写侧 1/0↔true，读侧统一 `ParseBool` |
| 迁移 | `addColumnIfMissing`（PRAGMA 检查 + ALTER，sqlite.go:434，14 处调用） | **必须方言化**：PG 用 `ADD COLUMN IF NOT EXISTS` 单语句 |

**结论：8 个预想差异点收敛为 5 个**（占位符/自增/布尔/迁移 + 共享
INSERT helper）。

## 4. `internal/dialect` 包设计

### 4.1 接口（6 点）

```go
// Package dialect abstracts the SQL dialect differences between
// supported relational backends (SQLite, Postgres, future MySQL).
package dialect

type Dialect interface {
    // Placeholder returns the bind-parameter marker for the i-th
    // argument (1-based). SQLite: "?", Postgres: "$1".
    Placeholder(i int) string

    // RewriteQuery translates a query written with '?' bind markers
    // into this dialect's native syntax. SQLite is the identity;
    // Postgres rewrites ? → $1, $2, ... with a state machine that
    // skips single-quoted string literals (so 'a?b' is untouched).
    // This codebase never uses '?' as a JSON path operator, and a
    // test asserts no store query contains '?' inside a literal.
    RewriteQuery(q string) string

    // AutoIncrement declares the id column type in CREATE TABLE.
    // SQLite: "INTEGER PRIMARY KEY AUTOINCREMENT",
    // Postgres: "BIGSERIAL PRIMARY KEY".
    AutoIncrement() string

    // ReturningClause returns the SQL fragment appended to an
    // INSERT to return the generated id, or "" if the generated id
    // must be read via LastInsertId.
    ReturningClause() string

    // Bool converts a Go bool for storage.
    // SQLite: int64(1)/int64(0), Postgres: true/false.
    Bool(v bool) any

    // ParseBool converts a scanned raw value into a bool. Accepts
    // bool, int64/int/float64 and string/[]byte forms
    // ("1", "true", "t", "yes").
    ParseBool(v any) bool

    // BoolColumn rewrites a boolean column declaration for CREATE
    // TABLE. SQLite keeps INTEGER 0/1; Postgres uses BOOLEAN with
    // true/false defaults. Only pure boolean columns go through
    // this; multi-value enum columns (e.g. status) stay INTEGER.
    BoolColumn(decl string) string

    // AddColumnIfMissing returns an idempotent ALTER statement, or
    // "" if the dialect requires a schema-introspection guard
    // (SQLite: PRAGMA table_info before ALTER).
    AddColumnIfMissing(table, column, decl string) string
}
```

### 4.2 共享 helper（dialect 包提供，store 复用）

```go
// Placeholders renders n bind markers: "(?,?,?)" or "($1,$2,$3)".
func Placeholders(d Dialect, n int) string

// InsertOne executes an INSERT and returns the generated id.
// When ReturningClause is non-empty the query is appended with
// " RETURNING id" and the id is scanned; otherwise LastInsertId
// is used. Also bumps any per-backend retry concerns (none today).
func InsertOne(d Dialect, db *sql.DB, query string, args ...any) (int64, error)
```

### 4.3 实现

```go
type SQLite struct{}     // RewriteQuery=identity, Placeholder="?",
                         // AutoIncrement="INTEGER PRIMARY KEY
                         // AUTOINCREMENT", Returning="", Bool=1/0,
                         // BoolColumn=identity, AddColumnIfMissing=""
type Postgres struct{}   // RewriteQuery=?→$N (state machine, skips
                         // 'literal'), AutoIncrement="BIGSERIAL
                         // PRIMARY KEY", Returning=" RETURNING id",
                         // Bool=true/false, BoolColumn=INTEGER→BOOLEAN
                         // (+DEFAULT true/false), AddColumnIfMissing=
                         // "ALTER TABLE t ADD COLUMN IF NOT EXISTS c d"
```

- 无状态、无依赖，纯函数集合；`var SQLite = SQLite{}` / `var PG = Postgres{}`。
- SQLite 的 `AddColumnIfMissing` 返回 ""：store 层保留现有 PRAGMA
  检查逻辑作为 fallback（`migrate()` 中判断 `d.AddColumnIfMissing(...) == ""`）。

### 4.4 为什么是 RewriteQuery 而不是逐点占位符

sqlite.go 有 110 个 `s.db.Exec/Query/QueryRow` 调用点、82 处 `?`。
逐点把 `?` 换成 `dialect.Placeholders` 会让 SQL 难以阅读（单占位符
也得写成 `WHERE id = ` + d.Placeholder(1)），且改动面大、易错。

RewriteQuery 方案：**SQL 文本保持 `?` 零改动**，store 层加 3 个
wrapper（`exec/query/queryRow`）统一过 `d.RewriteQuery`——SQLite
恒等，PG 自动翻译。改动集中在 wrapper + 结构点（InsertOne/布尔/
DDL/迁移），PG 复用时同一方法体直接可用。风险由测试兜底：
- 状态机正确跳过单引号字面量（'a?b' 不动）
- 测试断言：所有 store SQL 字符串内不含 `?`（字面量）、
  RewritePG 输出等价于手写 `$N`

## 5. store 重构策略（路线 B：SQLite 回填）

### 5.1 结构

```go
type SQLite struct {
    db      *sql.DB
    d       dialect.Dialect   // = dialect.SQLite（构造时固定，可注入便于测试）
    ...
}
```

- `NewSQLite(dsn)` 内部固定 `dialect.SQLite`；测试可通过
  `NewSQLiteWithDialect` 注入其他实现跑同一套 SQL（早期冒烟）。
- `Postgres` 同样持有 `d = dialect.Postgres`。

### 5.2 逐点重构规则

| 现状写法 | 重构后 |
|---|---|
| `s.db.Exec/Query/QueryRow(...)`（110 处） | `s.exec/query/queryRow(...)`——内部 `d.RewriteQuery`，SQL 文本零改动 |
| `INSERT ... VALUES (?, ?)` + `LastInsertId`（13 处） | `INSERT` 不变 + `dialect.InsertOne(s.d, s.db, ...)`（PG 自动走 RETURNING id） |
| `id INTEGER PRIMARY KEY AUTOINCREMENT` | `%s` 用 `d.AutoIncrement()` 占位 |
| `enabled INTEGER NOT NULL DEFAULT 1` + scan `*int` | 列声明经 `d.BoolColumn()`；**写侧** `d.Bool(v)`，**读侧** scan 到局部 `any` 后 `d.ParseBool(raw)` |
| `addColumnIfMissing(table, col, decl)` | `d.AddColumnIfMissing(...)` 非空则直接执行该语句，否则走现有 PRAGMA 守卫 |

### 5.3 迁移 DDL 统一

- `migrate()` 中的建表 DDL 模板保持共享（一个 schema 函数生成
  两组表：SQLite 版模板参数化 `AutoIncrement`；布尔列按后端
  生成 `INTEGER` 或 `BOOLEAN`）。
- 布尔列声明也走 helper：`Dialect.BoolColumn(decl string) string`
  追加到接口（`INTEGER NOT NULL DEFAULT 1` vs `BOOLEAN NOT NULL DEFAULT true`）——**追加为第 6 个方法**。

### 5.4 行为不变约束

- 重构逐组提交（按 13 个子接口分组），每组跑该组现有测试 +
  全量测试；任何一组行为变化即回退该组。
- SQL 语义（JOIN、WHERE、索引、默认值）一律不改，只改方言点。

## 6. storetest 共享测试套件（88 方法全量）

### 6.1 组织

```
internal/store/storetest/
    suite.go          // RunSuite(t, newStore func(t) store.Store, close func(t))
    channels_test.go  // 6 方法全量
    keys_test.go      // 4
    tokens_test.go    // 8
    plans_test.go     // 7
    users_test.go     // 7
    alerts_test.go    // 10
    guardrails_test.go// 8
    byok_test.go      // 6
    providers_test.go // 3
    combos_test.go    // 8
    runtime_test.go   // 2
    security_test.go  // 3
    mcp_test.go       // 11
    store_test.go     // Store 自有: Ping/Close/RawDB 语义
```

- 每个文件一组业务语义断言：建→查→改→删→并发→迁移幂等，
  **只断言业务结果，不碰 SQL**。
- `RunSuite(t, newStore)` 由具体实现调用：

```go
// sqlite_test.go（现状测试逐步迁移进来）
func TestSQLiteSuite(t *testing.T) {
    storetest.RunSuite(t, func(t) store.Store { return newTestSQLite(t) })
}
// postgres_test.go（M1）
func TestPostgresSuite(t *testing.T) {
    dsn := os.Getenv("LLMRX_TEST_PG_DSN")
    if dsn == "" { t.Skip("set LLMRX_TEST_PG_DSN to run") }
    storetest.RunSuite(t, func(t) store.Store { return openPG(t, dsn) })
}
```

- 既有 sqlite_test.go / boundary_test.go / combo_test.go 等测试
  逐步迁移进套件（保留对 SQLite 特有的边界测试，其余并入）。

### 6.2 覆盖范围

- 88 方法 = 13 子接口 83 方法 + Store 自有 5 方法（Ping/Close/
  RawQueryRow/RawQuery/RawDB）。
- 现有 sqlite 测试覆盖度：**全量 88 方法都已有测试**（sqlite.go
  覆盖率接近 100%，P11 阶段 ≥70% 目标已达成），套件 = 迁移重组
  + 按接口补全到逐方法断言，新增方法为 0（除非勘察发现缺口）。

## 7. 里程碑拆分（M0，独立交付）

```
M0a: internal/dialect 包（接口 + SQLite/Postgres 实现 + helper + 单测）
M0b: sqlite.go 回填重构走 Dialect（13 组逐组提交, 行为零变化）
M0c: storetest 套件 88 方法全量（现有测试迁移 + 按接口补全）
M0  验证: go test -short ./internal/... 全绿 + go vet 干净
        + internal/store 覆盖率不降（≥70%）
```

M0 完成后 M1（PG store 补齐）的验收 = 实现 Postgres + 跑 storetest
套件全绿，不再需要逐方法人工核对 SQL。

## 8. 风险与缓解

| Risk | Mitigation |
|---|---|
| 2235 行重构回归 | 逐组提交 + 每组全量测试 + 套件先行（重构前套件已绿） |
| 布尔 scan 类型重构出错 | `ParseBool` 单测覆盖 int64/int/bool/string 四种形态；scan 改动局部化 |
| RewriteQuery 误翻字面 `?` | 状态机跳过单引号字面量；测试断言 store SQL 无字面 `?`；`grep -n "?'" sqlite.go` 应零命中（JSON 路径操作符不在 codebase 使用） |
| 覆盖率下降 | 重构不删测试；迁移进套件时断言套件触达 88/88 |
| 未来新后端适配慢 | 适配路径固定为：实现 Dialect → 跑 storetest → 修方言 bug（循环收敛） |

## 9. Rollout

1. 本设计文档（git first doc first）。✅
2. M0a dialect 包。✅
3. M0b store 重构（13 组，逐组测试）。✅
4. M0c storetest 套件全量。✅
5. M0 验收（全量测试 + 覆盖率）。✅
6. M1：PG store 补齐 = 共享 dbStore 嵌入 + Postgres Dialect +
   storetest 验收（docker postgres:15-alpine + LLMRX_TEST_PG_DSN，
   88 方法套件全绿）。✅
7. M2-M5 依 P12-CLUSTER.md（限额/缓存/日志/部署）。

**Status (2026-08-02)**: M0-M1 全部落地。store 覆盖率 84.6%、
dialect 75.8%。新后端适配路径已验证：实现 Dialect + 嵌入 dbStore
+ 跑 storetest。

## 10. Out of scope（本设计）

- ClickHouse store 后端（仅 logstore 可选，M4 再议）。
- 数据迁移工具 sqlite→PG（P12 M1 附加交付，另立文档）。
- logstore 的方言抽象（M4 时复用本包或独立，届时评估）。
