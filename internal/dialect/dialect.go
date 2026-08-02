// Package dialect abstracts the SQL dialect differences between
// the supported relational backends (SQLite today, Postgres in
// P12 M1, future SQL-family backends).
//
// A dialect is a stateless collection of SQL generation helpers.
// Store implementations (internal/store, later internal/logstore)
// write their queries against Dialect so that adopting a new
// backend means implementing Dialect + running the shared
// test suite, not rewriting SQL.
package dialect

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Dialect captures every portability difference observed in the
// store layer (see docs/P12-STORE-ABSTRACTION.md §3-4). Notably
// absent are identifier quoting (the codebase never quotes
// identifiers), timestamps (stored as unix int64 everywhere) and
// upserts (ON CONFLICT ... DO UPDATE is portable).
type Dialect interface {
	// Placeholder returns the bind-parameter marker for the i-th
	// argument (1-based). SQLite: "?", Postgres: "$1".
	Placeholder(i int) string

	// RewriteQuery translates a query written with '?' bind markers
	// into this dialect's native syntax. SQLite is the identity;
	// Postgres rewrites ? -> $1, $2, ... with a state machine that
	// skips single-quoted string literals so 'a?b' is untouched.
	RewriteQuery(q string) string

	// RewriteDDL translates CREATE TABLE statements written in
	// SQLite form into the dialect's DDL conventions: the id
	// column (AutoIncrement) and pure boolean columns
	// (BoolColumn). SQLite is the identity.
	RewriteDDL(q string) string

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

// SQLite is the canonical implementation for mattn/go-sqlite3.
type SQLite struct{}

// Postgres is the implementation for the Postgres backend
// (internal/store/postgres.go, P12 M1).
type Postgres struct{}

// Placeholders renders n bind markers: "(?,?,?)" or "($1,$2,$3)".
// n <= 0 returns "".
func Placeholders(d Dialect, n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = d.Placeholder(i + 1)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// InsertOne executes an INSERT and returns the generated id. When
// the dialect has a ReturningClause the query is appended with it
// and the id is scanned from the returned row; otherwise the id is
// read via LastInsertId. The query is passed through
// d.RewriteQuery first so callers keep '?' syntax.
func InsertOne(d Dialect, db *sql.DB, query string, args ...any) (int64, error) {
	query = d.RewriteQuery(query)
	if rc := d.ReturningClause(); rc != "" {
		var id int64
		if err := db.QueryRow(query+rc, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	res, err := db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertOneContext is InsertOne with an explicit context. It also
// runs the query through d.RewriteQuery.
func InsertOneContext(d Dialect, db *sql.DB, ctx context.Context, query string, args ...any) (int64, error) {
	query = d.RewriteQuery(query)
	if rc := d.ReturningClause(); rc != "" {
		var id int64
		if err := db.QueryRowContext(ctx, query+rc, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (SQLite) Placeholder(int) string { return "?" }

// RewriteQuery is the identity for SQLite — the canonical queries
// are written in SQLite's own '?' syntax.
func (SQLite) RewriteQuery(q string) string { return q }

// RewriteDDL is the identity for SQLite.
func (SQLite) RewriteDDL(q string) string { return q }

func (SQLite) AutoIncrement() string { return "INTEGER PRIMARY KEY AUTOINCREMENT" }

func (SQLite) ReturningClause() string { return "" }

func (SQLite) Bool(v bool) any {
	if v {
		return int64(1)
	}
	return int64(0)
}

func (SQLite) BoolColumn(decl string) string { return decl }

func (SQLite) AddColumnIfMissing(string, string, string) string { return "" }

func (Postgres) Placeholder(i int) string { return fmt.Sprintf("$%d", i) }

// RewriteQuery converts '?' bind markers to Postgres' $N syntax.
// A small state machine skips single-quoted string literals
// (including '' escapes) so literal '?' inside strings is preserved.
// The codebase never uses '?' as a JSON path operator; a test
// asserts no store query contains a literal '?'.
func (Postgres) RewriteQuery(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 1
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			j := i + 1
			for j < len(q) {
				if q[j] == '\'' {
					if j+1 < len(q) && q[j+1] == '\'' {
						j += 2 // escaped ''
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(q[i:j])
			i = j - 1
			continue
		}
		if c == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			n++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (Postgres) AutoIncrement() string { return "BIGSERIAL PRIMARY KEY" }

func (Postgres) ReturningClause() string { return " RETURNING id" }

func (Postgres) Bool(v bool) any { return v }

// BoolColumn maps an INTEGER boolean declaration to the Postgres
// BOOLEAN form, rewriting DEFAULT 1/0 to true/false.
func (Postgres) BoolColumn(decl string) string {
	s := strings.Replace(decl, "INTEGER", "BOOLEAN", 1)
	s = strings.Replace(s, "DEFAULT 1", "DEFAULT true", 1)
	s = strings.Replace(s, "DEFAULT 0", "DEFAULT false", 1)
	return s
}

func (Postgres) AddColumnIfMissing(table, column, decl string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", table, column, decl)
}

// boolColumns are the pure boolean columns whose SQLite INTEGER
// declaration is rewritten to BOOLEAN on Postgres. Multi-value enum
// columns (users.role, channels.status, keys.status, plans.status,
// tokens.status, byok_channels.status, mcp_servers...) stay INTEGER.
var boolColumns = []string{
	"alerts.enabled",
	"alert_events.delivered_webhook",
	"alert_events.acknowledged",
	"token_combo_models.enabled",
	"token_combo_models.is_default",
	"guardrails.enabled",
	"guardrail_events.verdict",
}

// timeColumns store unix-epoch seconds and need BIGINT on Postgres
// (INTEGER would overflow in 2038). SQLite's INTEGER is 64-bit.
var timeColumns = []string{
	"created_at", "updated_at", "last_used_at", "last_fired_at",
	"fired_at", "expires_at", "session_exp",
}

// RewriteDDL translates SQLite-form CREATE TABLE statements:
//   - id INTEGER PRIMARY KEY AUTOINCREMENT -> id BIGSERIAL PRIMARY KEY
//   - <timecol> INTEGER -> <timecol> BIGINT (per timeColumns)
//   - <boolcol> INTEGER NOT NULL DEFAULT 1|0 -> <boolcol> BOOLEAN NOT
//     NULL DEFAULT true|false (per boolColumns)
func (Postgres) RewriteDDL(q string) string {
	q = strings.Replace(q, "id INTEGER PRIMARY KEY AUTOINCREMENT", "id BIGSERIAL PRIMARY KEY", 1)
	// SQLite REAL is 64-bit; Postgres REAL is float32 — use
	// DOUBLE PRECISION so money/prices roundtrip exactly.
	q = strings.Replace(q, " REAL", " DOUBLE PRECISION", -1)
	for _, col := range timeColumns {
		q = strings.Replace(q, col+" INTEGER", col+" BIGINT", 1)
	}
	for _, col := range boolColumns {
		cn := col[strings.IndexByte(col, '.')+1:]
		q = strings.Replace(q, cn+" INTEGER NOT NULL DEFAULT 1", cn+" BOOLEAN NOT NULL DEFAULT true", 1)
		q = strings.Replace(q, cn+" INTEGER NOT NULL DEFAULT 0", cn+" BOOLEAN NOT NULL DEFAULT false", 1)
	}
	return q
}

// ParseBool is the reader-side counterpart of Bool. Both
// implementations share the same tolerant conversion.
func (SQLite) ParseBool(v any) bool   { return ParseBool(v) }
func (Postgres) ParseBool(v any) bool { return ParseBool(v) }

// ParseBool converts a scanned raw value into a bool. It is the
// shared reader-side counterpart of Dialect.Bool.
func ParseBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case []byte:
		s := strings.TrimSpace(string(x))
		return s == "1" || s == "true" || s == "t" || s == "yes" || s == "TRUE" || s == "True"
	case string:
		s := strings.TrimSpace(x)
		return s == "1" || s == "true" || s == "t" || s == "yes" || s == "TRUE" || s == "True"
	default:
		return false
	}
}
