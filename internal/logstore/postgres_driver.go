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
//
// The DDL is the canonical SQLite-form logsTableDDL translated by
// dialect.RewriteDDL — the same source the SQLite driver uses, so
// the schema lives in exactly one place (postgres_ddl_test.go pins
// the translated output).
func (p *PostgresDriver) Open(dir string) error {
	stmts := append([]string{logsTableDDL}, logsIndexes...)
	for _, q := range stmts {
		if _, err := p.db.Exec(p.d.RewriteDDL(q)); err != nil {
			return fmt.Errorf("logstore: migrate: %w", err)
		}
	}
	return nil
}

// Insert appends one log entry.
func (p *PostgresDriver) Insert(entry *model.Log) error {
	_, err := p.db.Exec(p.d.RewriteQuery(logsInsertSQL), logInsertArgs(entry)...)
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
		if _, err := tx.Exec(p.d.RewriteQuery(logsInsertSQL), logInsertArgs(e)...); err != nil {
			return 0, fmt.Errorf("logstore: batch insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(entries), nil
}

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

	limit, offset := clampPage(filter.Limit, filter.Offset)
	q := "SELECT " + logRowCols + " " + from + " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
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
	if err := streamLogRows(rows, func(l *model.Log) bool {
		out = append(out, *l)
		return true
	}); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ScanSince streams rows with CreatedAt >= since to fn, ascending by
// creation time. The created_at index makes this a range scan.
func (p *PostgresDriver) ScanSince(since time.Time, fn func(*model.Log) bool) error {
	q := "SELECT " + logRowCols + " FROM logs WHERE created_at >= ? ORDER BY created_at ASC, id ASC"
	rows, err := p.db.Query(p.d.RewriteQuery(q), since.Unix())
	if err != nil {
		return fmt.Errorf("logstore: scan-since: %w", err)
	}
	defer rows.Close()
	return streamLogRows(rows, fn)
}

// LogStats aggregates totals across all rows (days ignored — the
// single table is always "all files").
func (p *PostgresDriver) LogStats(days []string) (LogStatsResult, error) {
	q := logStatsProjection + " FROM logs"
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

	q := logTimeSeriesProjection + " FROM logs"
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
	// Validate field to prevent SQL injection (defense-in-depth).
	switch field {
	case "model", "channel_id", "token_id":
		// allow
	default:
		return nil, fmt.Errorf("logstore: TopByField invalid field %q", field)
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

	q := logTopProjection(field) + " FROM logs"
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
			scanErr = rows.Scan(&label, &m.Count, &m.Tokens, &m.Cost, &m.RealCost)
		default:
			scanErr = rows.Scan(&labelI, &m.Count, &m.Tokens, &m.Cost, &m.RealCost)
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

// ArchiveFiles is a no-op for the Postgres backend: there are no
// per-date files to compress, the logs live in a shared table.
// Retention in "archive" mode therefore keeps rows forever (nothing
// is deleted); operators who want compressed long-term storage for
// Postgres should export the table with pg_dump / COPY separately.
func (p *PostgresDriver) ArchiveFiles(days []string) error { return nil }

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
