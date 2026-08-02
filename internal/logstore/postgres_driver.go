package logstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

// PostgresDriver is the cluster-mode logstore backend (P12 M4):
// one shared `logs` table in Postgres instead of per-date SQLite
// files, so every replica writes to a single queryable store and
// the admin log pages see cluster-wide data.
//
// Files map to days: ListFiles returns the distinct UTC dates
// present in the table; DeleteFiles removes rows in those date
// ranges (retention stays a "drop the day" operation).
//
// All SQL is written in '?' syntax and passed through the
// Postgres dialect (Rewriter -> $N) — identical query bodies to
// the SQLite driver where possible.
type PostgresDriver struct {
	db *sql.DB
	d  dialect.Dialect
}

// NewPostgresDriver opens the shared connection. The Driver.Open
// method (called by logstore.Manager.New) then creates the table.
func NewPostgresDriver(dsn string) (*PostgresDriver, error) {
	if dsn == "" {
		return nil, errors.New("logstore: empty postgres dsn")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("logstore: open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("logstore: ping: %w", err)
	}
	return &PostgresDriver{db: db, d: dialect.Postgres{}}, nil
}

// Open creates the logs table and indexes (idempotent). dir is
// ignored — there are no files in the Postgres backend.
func (p *PostgresDriver) Open(dir string) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS logs (
			id BIGSERIAL PRIMARY KEY,
			token_id BIGINT NOT NULL DEFAULT 0,
			channel_id BIGINT NOT NULL DEFAULT 0,
			key_id BIGINT NOT NULL DEFAULT 0,
			model TEXT NOT NULL DEFAULT '',
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			real_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
			billed_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
			duration_ms BIGINT NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0,
			router_path TEXT NOT NULL DEFAULT '',
			request_ip TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			units INTEGER NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_created ON logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_token_created ON logs(token_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_channel_created ON logs(channel_id, created_at)`,
	}
	for _, q := range stmts {
		if _, err := p.db.Exec(p.d.RewriteDDL(q)); err != nil {
			return fmt.Errorf("logstore: migrate: %w", err)
		}
	}
	return nil
}

// Insert appends one log entry.
func (p *PostgresDriver) Insert(entry *model.Log) error {
	_, err := p.db.Exec(p.d.RewriteQuery(
		`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		entry.TokenID, entry.ChannelID, entry.KeyID, entry.Model,
		entry.PromptTokens, entry.CompletionTokens, entry.CachedTokens,
		entry.RealCostUSD, entry.BilledCostUSD,
		entry.DurationMs, entry.StatusCode, entry.RouterPath, entry.RequestIP,
		entry.Endpoint, entry.Units,
		entry.CreatedAt.UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("logstore: insert: %w", err)
	}
	return nil
}

// BatchInsert inserts all entries in one transaction.
func (p *PostgresDriver) BatchInsert(entries []*model.Log) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, e := range entries {
		if _, err := tx.Exec(p.d.RewriteQuery(
			`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			e.TokenID, e.ChannelID, e.KeyID, e.Model,
			e.PromptTokens, e.CompletionTokens, e.CachedTokens,
			e.RealCostUSD, e.BilledCostUSD,
			e.DurationMs, e.StatusCode, e.RouterPath, e.RequestIP,
			e.Endpoint, e.Units,
			e.CreatedAt.UTC().Unix(),
		); err != nil {
			return 0, fmt.Errorf("logstore: batch insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(entries), nil
}

const logCols = "id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at"

// QueryAcross returns rows matching filter, optionally restricted
// to the given days, with total count and pagination.
func (p *PostgresDriver) QueryAcross(filter QueryFilter, days []string) ([]model.Log, int64, error) {
	where, args := buildWhere(filter)
	dayConds, dayArgs := dayRangeConds(days)
	if dayConds != "" {
		if where != "" {
			where += " AND " + dayConds
		} else {
			where = dayConds
		}
		args = append(args, dayArgs...)
	}

	from := "FROM logs"
	if where != "" {
		from += " WHERE " + where
	}

	var total int64
	if err := p.db.QueryRow(p.d.RewriteQuery("SELECT COUNT(*) "+from), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("logstore: count: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	q := "SELECT " + logCols + " " + from + " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := p.db.Query(p.d.RewriteQuery(q), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("logstore: query: %w", err)
	}
	defer rows.Close()

	remaining := total - int64(offset)
	if remaining < 0 {
		remaining = 0
	}
	cap := int64(limit)
	if remaining < cap {
		cap = remaining
	}
	out := make([]model.Log, 0, cap)
	for rows.Next() {
		var l model.Log
		var created int64
		if err := rows.Scan(&l.ID, &l.TokenID, &l.ChannelID, &l.KeyID, &l.Model,
			&l.PromptTokens, &l.CompletionTokens, &l.CachedTokens,
			&l.RealCostUSD, &l.BilledCostUSD, &l.DurationMs, &l.StatusCode,
			&l.RouterPath, &l.RequestIP, &l.Endpoint, &l.Units, &created); err != nil {
			return nil, 0, err
		}
		l.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// LogStats aggregates totals across all rows (days ignored — the
// single table is always "all files").
func (p *PostgresDriver) LogStats(days []string) (LogStatsResult, error) {
	q := `SELECT COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(real_cost_usd),0), COALESCE(SUM(billed_cost_usd),0), COUNT(*), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0) FROM logs`
	var out LogStatsResult
	if err := p.db.QueryRow(p.d.RewriteQuery(q)).Scan(
		&out.PromptTokens, &out.CompletionTokens, &out.RealCostUSD,
		&out.BilledCostUSD, &out.Total, &out.Errors); err != nil {
		return LogStatsResult{}, fmt.Errorf("logstore: stats: %w", err)
	}
	return out, nil
}

// TimeSeries groups rows into bucketSec-second buckets.
func (p *PostgresDriver) TimeSeries(filter QueryFilter, bucketSec int64, days []string) ([]SeriesBucket, error) {
	if bucketSec <= 0 {
		bucketSec = 3600
	}
	where, args := buildWhere(filter)
	dayConds, dayArgs := dayRangeConds(days)
	if dayConds != "" {
		if where != "" {
			where += " AND " + dayConds
		} else {
			where = dayConds
		}
		args = append(args, dayArgs...)
	}

	q := `SELECT (created_at / ?) * ? AS bucket, COUNT(*), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(real_cost_usd), 0), COALESCE(SUM(billed_cost_usd), 0) FROM logs`
	if where != "" {
		q += " WHERE " + where
	}
	q += " GROUP BY bucket ORDER BY bucket"
	args = append([]any{bucketSec, bucketSec}, args...)

	rows, err := p.db.Query(p.d.RewriteQuery(q), args...)
	if err != nil {
		return nil, fmt.Errorf("logstore: timeseries: %w", err)
	}
	defer rows.Close()
	out := []SeriesBucket{}
	for rows.Next() {
		var b SeriesBucket
		if err := rows.Scan(&b.Bucket, &b.Requests, &b.Errors, &b.PromptTokens, &b.CompletionTokens, &b.RealCostUSD, &b.BilledCostUSD); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// TopByField groups by model/channel_id/token_id and returns the
// top `limit` labels by row count.
func (p *PostgresDriver) TopByField(filter QueryFilter, field string, limit int, days []string) ([]NamedMetric, error) {
	where, args := buildWhere(filter)
	dayConds, dayArgs := dayRangeConds(days)
	if dayConds != "" {
		if where != "" {
			where += " AND " + dayConds
		} else {
			where = dayConds
		}
		args = append(args, dayArgs...)
	}

	q := "SELECT " + field + ", COUNT(*), COALESCE(SUM(prompt_tokens+completion_tokens),0), COALESCE(SUM(billed_cost_usd),0) FROM logs"
	if where != "" {
		q += " WHERE " + where
	}
	q += " GROUP BY " + field + " ORDER BY 2 DESC LIMIT ?"
	args = append(args, limit)

	rows, err := p.db.Query(p.d.RewriteQuery(q), args...)
	if err != nil {
		return nil, fmt.Errorf("logstore: topbyfield: %w", err)
	}
	defer rows.Close()
	out := []NamedMetric{}
	for rows.Next() {
		var m NamedMetric
		var label sql.NullString
		var labelI int64
		var scanErr error
		switch field {
		case "model":
			scanErr = rows.Scan(&label, &m.Count, &m.Tokens, &m.Cost)
		default:
			scanErr = rows.Scan(&labelI, &m.Count, &m.Tokens, &m.Cost)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		if field == "model" {
			m.Label = label.String
		} else {
			m.Label = strconv.FormatInt(labelI, 10)
		}
		if m.Label == "" {
			m.Label = "(none)"
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListFiles returns the distinct UTC dates present in the table,
// sorted ascending ("YYYY-MM-DD"). This drives retention: the
// Manager compares dates and deletes whole days.
func (p *PostgresDriver) ListFiles() ([]string, error) {
	rows, err := p.db.Query(`SELECT DISTINCT to_char(to_timestamp(created_at), 'YYYY-MM-DD') AS d FROM logs ORDER BY d`)
	if err != nil {
		return nil, fmt.Errorf("logstore: list files: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteFiles removes every row whose UTC date is in the list.
func (p *PostgresDriver) DeleteFiles(days []string) error {
	if len(days) == 0 {
		return nil
	}
	conds, args := dayRangeConds(days)
	if conds == "" {
		return nil
	}
	if _, err := p.db.Exec(p.d.RewriteQuery("DELETE FROM logs WHERE "+conds), args...); err != nil {
		return fmt.Errorf("logstore: delete files: %w", err)
	}
	return nil
}

// Close releases the shared connection.
func (p *PostgresDriver) Close() error { return p.db.Close() }

// dayRangeConds builds "(created_at >= ? AND created_at < ?) OR ..."
// conditions for the given day identifiers ("YYYY-MM-DD" or
// "YYYY-MM-DD-N"). Returns ("", nil) when no day parses.
func dayRangeConds(days []string) (string, []any) {
	var conds []string
	var args []any
	for _, d := range days {
		base := d
		// Strip any "-N" sequence suffix from rollover files.
		if i := strings.LastIndexByte(base, '-'); i >= len("2006-01-02") {
			// e.g. "2026-08-02-1": the last '-' (past the date's
			// own separators) is the sequence separator.
			if _, err := strconv.Atoi(base[i+1:]); err == nil {
				base = base[:i]
			}
		}
		t, err := time.Parse("2006-01-02", base)
		if err != nil {
			continue
		}
		start := t.UTC().Unix()
		conds = append(conds, "(created_at >= ? AND created_at < ?)")
		args = append(args, start, start+86400)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "(" + strings.Join(conds, " OR ") + ")", args
}
