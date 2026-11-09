package logstore

import (
	"github.com/sn0wfree/llmRx/internal/model"
)

// This file holds the SQL fragments shared verbatim by the SQLite
// and Postgres drivers. Everything is written in '?' bind syntax;
// the Postgres driver passes statements through dialect.RewriteQuery
// on the way out. One source of truth per query shape — the drivers
// differ only in storage layout (per-day files + ATTACH unions vs a
// single shared table).

// logsTableDDL is the canonical logs table in SQLite form. The
// Postgres driver runs it through dialect.RewriteDDL, which maps
// AUTOINCREMENT → BIGSERIAL, REAL → DOUBLE PRECISION and the
// timestamp column to BIGINT; see postgres_ddl_test.go, which pins
// the rewritten output.
const logsTableDDL = `CREATE TABLE IF NOT EXISTS logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	token_id INTEGER NOT NULL DEFAULT 0,
	channel_id INTEGER NOT NULL DEFAULT 0,
	key_id INTEGER NOT NULL DEFAULT 0,
	model TEXT NOT NULL DEFAULT '',
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	cache_hit INTEGER NOT NULL DEFAULT 0,
	real_cost_usd REAL NOT NULL DEFAULT 0,
	billed_cost_usd REAL NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	status_code INTEGER NOT NULL DEFAULT 0,
	router_path TEXT NOT NULL DEFAULT '',
	request_ip TEXT NOT NULL DEFAULT '',
	endpoint TEXT NOT NULL DEFAULT '',
	units INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
)`

// logsIndexes is the shared index set (superset of what either
// backend declared historically; both are CREATE IF NOT EXISTS).
var logsIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_logs_created ON logs(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_token_created ON logs(token_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_channel_created ON logs(channel_id, created_at)`,
}

// logsInsertSQL inserts one log row. Both backends bind the same 18
// values via logInsertArgs.
const logsInsertSQL = `INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, cache_hit, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// logInsertArgs binds entry in logsInsertSQL column order.
func logInsertArgs(e *model.Log) []any {
	return []any{
		e.TokenID, e.ChannelID, e.KeyID, e.Model,
		e.PromptTokens, e.CompletionTokens, e.CachedTokens,
		e.ReasoningTokens, int64(boolToInt(e.CacheHit)),
		e.RealCostUSD, e.BilledCostUSD,
		e.DurationMs, e.StatusCode, e.RouterPath, e.RequestIP,
		e.Endpoint, e.Units,
		e.CreatedAt.UTC().Unix(),
	}
}

// logRowCols is the SELECT list for reading a logs row into a
// model.Log. It must stay in lockstep with streamLogRows — a mismatch
// is a runtime scan error, not a compile error.
const logRowCols = "id, token_id, channel_id, key_id, model, prompt_tokens, " +
	"completion_tokens, cached_tokens, reasoning_tokens, cache_hit, " +
	"real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, " +
	"request_ip, endpoint, units, created_at"

// logStatsProjection is the LogStats aggregate projection. Callers
// append their own FROM clause: " FROM logs" (Postgres) or a
// UNION-ALL subquery over ATTACHed day files (SQLite).
const logStatsProjection = `SELECT COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(real_cost_usd),0), COALESCE(SUM(billed_cost_usd),0), COUNT(*), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0)`

// logTimeSeriesProjection is the TimeSeries bucket projection.
// Callers prepend the two bucket-sec binds in the args and append
// their FROM clause, optional WHERE, and " GROUP BY bucket ORDER BY
// bucket".
const logTimeSeriesProjection = `SELECT (created_at / ?) * ? AS bucket, COUNT(*), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(real_cost_usd), 0), COALESCE(SUM(billed_cost_usd), 0)`

// logTopProjection renders the TopByField outer projection for the
// validated grouping field; callers append their FROM clause.
func logTopProjection(field string) string {
	return "SELECT " + field + ", COUNT(*), COALESCE(SUM(prompt_tokens+completion_tokens),0), COALESCE(SUM(billed_cost_usd),0), COALESCE(SUM(real_cost_usd),0)"
}

// clampPage normalises pagination bounds shared by QueryAcross
// implementations.
func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
