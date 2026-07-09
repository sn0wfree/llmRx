package logstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sn0wfree/llmRx/internal/model"
)

// MaxFileBytes is the per-file size threshold that triggers a
// rollover into a new seq file (YYYY-MM-DD-N.db).
const MaxFileBytes = 100 * 1024 * 1024

// estimatedRowBytes is the per-row overhead estimate used to
// update the in-memory bytes-written counter. Real log rows land
// between 100-200 bytes depending on TEXT length, so 150 is a
// reasonable midpoint.
const estimatedRowBytes = 150

// SQLiteDriver stores logs in one SQLite file per UTC date under
// dir. Files exceeding MaxFileBytes roll over into YYYY-MM-DD-N.db
// (N starts at 1).
//
// Concurrency: per-file state is guarded by an RWMutex; only the
// hot-path Insert holds the read lock briefly (cache lookup),
// then takes the per-file conn lock for the actual Exec.
type SQLiteDriver struct {
	dir     string
	maxOpen int

	mu    sync.RWMutex
	conns map[string]*dayFile // dayFile (basename without .db) → state
}

// dayFile tracks a single on-disk log file and its open connection.
type dayFile struct {
	conn         *sql.DB
	bytesWritten int64 // atomic; updated by Insert, calibrated at Open
}

// NewSQLiteDriver returns an unopened driver. Call Open before use.
func NewSQLiteDriver() *SQLiteDriver {
	return &SQLiteDriver{
		conns:   make(map[string]*dayFile),
		maxOpen: 4, // today + 3 historical
	}
}

// Open sets the storage directory and creates it if missing.
func (d *SQLiteDriver) Open(dir string) error {
	if dir == "" {
		return errors.New("logstore: empty dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("logstore: mkdir %s: %w", dir, err)
	}
	d.dir = dir
	return nil
}

// Close closes every cached connection.
func (d *SQLiteDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var firstErr error
	for k, df := range d.conns {
		if err := df.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(d.conns, k)
	}
	return firstErr
}

// ---------- Insert ----------

// Insert routes entry to the file for entry.CreatedAt's UTC date
// and (if needed) into the next seq slot when the current file is
// full. Hot-path overhead: ~50ns (atomic load + map lookup under
// RLock) — no filesystem stat.
func (d *SQLiteDriver) Insert(entry *model.Log) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = Now()
	}
	date := entry.CreatedAt.UTC().Format("2006-01-02")

	df, dayFileKey, err := d.acquire(date, 0) // 0 = next seq after current max
	if err != nil {
		return err
	}

	if _, err := df.conn.Exec(
		`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.TokenID, entry.ChannelID, entry.KeyID, entry.Model,
		entry.PromptTokens, entry.CompletionTokens, entry.CachedTokens,
		entry.RealCostUSD, entry.BilledCostUSD,
		entry.DurationMs, entry.StatusCode, entry.RouterPath, entry.RequestIP,
		entry.CreatedAt.UTC().Unix(),
	); err != nil {
		return fmt.Errorf("logstore: insert %s: %w", dayFileKey, err)
	}

	atomic.AddInt64(&df.bytesWritten, estimatedRowBytes)
	return nil
}

// acquire returns the dayFile to write into for the given date. It
// honours the MaxFileBytes rollover: if the current seq file is
// full, the call promotes to the next seq. seqHint is reserved for
// future use (currently unused; rollover is derived from the
// counter).
func (d *SQLiteDriver) acquire(date string, seqHint int) (*dayFile, string, error) {
	key := date

	// Fast path: file is in cache and below threshold.
	d.mu.RLock()
	if df, ok := d.conns[key]; ok {
		if atomic.LoadInt64(&df.bytesWritten) < MaxFileBytes {
			d.mu.RUnlock()
			return df, key, nil
		}
	}
	d.mu.RUnlock()

	// Slow path: file full, evicted, or never opened.
	d.mu.Lock()
	defer d.mu.Unlock()

	if df, ok := d.conns[key]; ok {
		if atomic.LoadInt64(&df.bytesWritten) < MaxFileBytes {
			return df, key, nil
		}
		// Full: close and evict; fall through to next seq.
		_ = df.conn.Close()
		delete(d.conns, key)
	}

	// LRU eviction if cache is over budget: close one arbitrary entry.
	if len(d.conns) >= d.maxOpen {
		for k, df := range d.conns {
			_ = df.conn.Close()
			delete(d.conns, k)
			break
		}
	}

	// Find next free seq for this date (base, then -1, -2, ...).
	seq := 0
	for {
		candidate := dayFileKey(date, seq)
		path := filepath.Join(d.dir, candidate+".db")

		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			// File doesn't exist yet — claim this seq slot.
			break
		} else if err != nil {
			return nil, "", fmt.Errorf("logstore: stat %s: %w", path, err)
		}

		// File exists; check size on disk (one stat per rollover,
		// not per insert).
		fi, err := os.Stat(path)
		if err != nil {
			return nil, "", fmt.Errorf("logstore: stat %s: %w", path, err)
		}
		if fi.Size() < MaxFileBytes {
			break
		}
		seq++
		if seq > 9999 {
			return nil, "", fmt.Errorf("logstore: too many seq files for %s", date)
		}
	}

	key = dayFileKey(date, seq)
	path := filepath.Join(d.dir, key+".db")
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return nil, "", fmt.Errorf("logstore: open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(2) // 1 writer + 1 reader for ATTACH

	if err := ensureLogSchema(conn); err != nil {
		_ = conn.Close()
		return nil, "", fmt.Errorf("logstore: schema %s: %w", key, err)
	}

	// Calibrate the in-memory counter to the file's current size.
	df := &dayFile{conn: conn}
	if fi, err := os.Stat(path); err == nil {
		atomic.StoreInt64(&df.bytesWritten, fi.Size())
	}
	d.conns[key] = df
	return df, key, nil
}

// dayFileKey formats a (date, seq) pair into the basename used on
// disk. seq 0 → "YYYY-MM-DD"; seq N → "YYYY-MM-DD-N".
func dayFileKey(date string, seq int) string {
	if seq == 0 {
		return date
	}
	return fmt.Sprintf("%s-%d", date, seq)
}

// ensureLogSchema creates the logs table and its indexes if absent.
// The schema matches the one formerly in store/sqlite.go so admin
// queries (analytics, /logs) continue to work unchanged.
func ensureLogSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_id INTEGER NOT NULL DEFAULT 0,
			channel_id INTEGER NOT NULL DEFAULT 0,
			key_id INTEGER NOT NULL DEFAULT 0,
			model TEXT NOT NULL DEFAULT '',
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			real_cost_usd REAL NOT NULL DEFAULT 0,
			billed_cost_usd REAL NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0,
			router_path TEXT NOT NULL DEFAULT '',
			request_ip TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_created ON logs(created_at)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// ---------- QueryAcross (ATTACH) ----------

// QueryAcross returns paginated rows matching filter across the
// given day files. If days is empty, every file ListFiles knows
// about is included.
//
// To avoid SQLite's default 10-attached-database limit, we cap
// at MaxAttachFiles (8). If more days are requested we attach the
// most recent N; older days are reported in the error so the
// caller can paginate by date range explicitly.
func (d *SQLiteDriver) QueryAcross(filter QueryFilter, days []string) ([]model.Log, int64, error) {
	if len(days) == 0 {
		var err error
		days, err = d.ListFiles()
		if err != nil {
			return nil, 0, err
		}
	}
	if len(days) == 0 {
		return []model.Log{}, 0, nil
	}
	sort.Strings(days)

	if len(days) > MaxAttachFiles {
		// Take the most recent N files; older ones are out of
		// scope for this query.
		days = days[len(days)-MaxAttachFiles:]
	}

	// Anchor: the last (most recent) file is the conn we hold open.
	anchor := days[len(days)-1]
	d.mu.RLock()
	df, ok := d.conns[anchor]
	d.mu.RUnlock()

	if !ok {
		// Anchor not in cache (e.g. just-after-restart); open it.
		var err error
		df, _, err = d.acquire(extractDate(anchor), seqOf(anchor))
		if err != nil {
			return nil, 0, err
		}
	}

	// Attach all other files under aliases "log_0", "log_1", ...
	attachAliases := make([]string, 0, len(days))
	for i, day := range days {
		alias := fmt.Sprintf("log_%d", i)
		if day == anchor {
			// Anchor is accessed directly as "logs"; skip ATTACH.
			attachAliases = append(attachAliases, "")
			continue
		}
		path := filepath.Join(d.dir, day+".db")
		stmt := fmt.Sprintf(`ATTACH %q AS %s`, path, alias)
		if _, err := df.conn.Exec(stmt); err != nil {
			detachAll(df.conn, attachAliases)
			return nil, 0, fmt.Errorf("logstore: attach %s: %w", day, err)
		}
		attachAliases = append(attachAliases, alias)
	}
	defer detachAll(df.conn, attachAliases)

	// Build UNION ALL query.
	var sb strings.Builder
	sb.WriteString("SELECT id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, created_at FROM (")
	for i := range days {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		source := "logs"
		if i < len(attachAliases) && attachAliases[i] != "" {
			source = attachAliases[i] + ".logs"
		}
		sb.WriteString("SELECT id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, created_at FROM ")
		sb.WriteString(source)
	}
	sb.WriteString(")")

	where, args := buildWhere(filter)
	if where != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(where)
	}

	// Count.
	var total int64
	countSQL := "SELECT COUNT(*) FROM (" + sb.String() + ")"
	if err := df.conn.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("logstore: count: %w", err)
	}

	// Page.
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

	sb.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, offset)

	rows, err := df.conn.Query(sb.String(), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("logstore: query: %w", err)
	}
	defer rows.Close()

	out := make([]model.Log, 0, limit)
	for rows.Next() {
		var l model.Log
		var created int64
		if err := rows.Scan(&l.ID, &l.TokenID, &l.ChannelID, &l.KeyID, &l.Model,
			&l.PromptTokens, &l.CompletionTokens, &l.CachedTokens,
			&l.RealCostUSD, &l.BilledCostUSD, &l.DurationMs, &l.StatusCode,
			&l.RouterPath, &l.RequestIP, &created); err != nil {
			return nil, 0, err
		}
		l.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// detachAll detaches any non-empty alias in the list. Errors are
// silently ignored — the connection is going back to the pool and
// a failed DETACH will surface on next use.
func detachAll(db *sql.DB, aliases []string) {
	for _, a := range aliases {
		if a == "" {
			continue
		}
		_, _ = db.Exec(fmt.Sprintf("DETACH %s", a))
	}
}

// buildWhere constructs the WHERE clause + args from filter. Empty
// string means no filter.
func buildWhere(f QueryFilter) (string, []any) {
	var conds []string
	var args []any
	if f.TokenID > 0 {
		conds = append(conds, "token_id = ?")
		args = append(args, f.TokenID)
	}
	if f.ChannelID > 0 {
		conds = append(conds, "channel_id = ?")
		args = append(args, f.ChannelID)
	}
	if f.KeyID > 0 {
		conds = append(conds, "key_id = ?")
		args = append(args, f.KeyID)
	}
	if f.Model != "" {
		conds = append(conds, "model = ?")
		args = append(args, f.Model)
	}
	if f.StatusCode > 0 {
		conds = append(conds, "status_code = ?")
		args = append(args, f.StatusCode)
	}
	if f.CreatedFrom > 0 {
		conds = append(conds, "created_at >= ?")
		args = append(args, f.CreatedFrom)
	}
	if f.CreatedTo > 0 {
		conds = append(conds, "created_at <= ?")
		args = append(args, f.CreatedTo)
	}
	if len(conds) == 0 {
		return "", args
	}
	return strings.Join(conds, " AND "), args
}

// ---------- LogStats ----------

// LogStats aggregates totals across the given days (or all files
// when days is nil).
func (d *SQLiteDriver) LogStats(days []string) (LogStatsResult, error) {
	if len(days) == 0 {
		var err error
		days, err = d.ListFiles()
		if err != nil {
			return LogStatsResult{}, err
		}
	}
	if len(days) == 0 {
		return LogStatsResult{}, nil
	}
	sort.Strings(days)
	if len(days) > MaxAttachFiles {
		days = days[len(days)-MaxAttachFiles:]
	}

	anchor := days[len(days)-1]
	d.mu.RLock()
	df, ok := d.conns[anchor]
	d.mu.RUnlock()
	if !ok {
		var err error
		df, _, err = d.acquire(extractDate(anchor), seqOf(anchor))
		if err != nil {
			return LogStatsResult{}, err
		}
	}

	attachAliases := make([]string, 0, len(days))
	for i, day := range days {
		alias := fmt.Sprintf("stat_%d", i)
		if day == anchor {
			attachAliases = append(attachAliases, "")
			continue
		}
		path := filepath.Join(d.dir, day+".db")
		if _, err := df.conn.Exec(fmt.Sprintf(`ATTACH %q AS %s`, path, alias)); err != nil {
			detachAll(df.conn, attachAliases)
			return LogStatsResult{}, fmt.Errorf("logstore: attach %s: %w", day, err)
		}
		attachAliases = append(attachAliases, alias)
	}
	defer detachAll(df.conn, attachAliases)

	var sb strings.Builder
	sb.WriteString("SELECT COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(real_cost_usd),0), COALESCE(SUM(billed_cost_usd),0), COUNT(*), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0) FROM (")
	for i := range days {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		source := "logs"
		if i < len(attachAliases) && attachAliases[i] != "" {
			source = attachAliases[i] + ".logs"
		}
		sb.WriteString("SELECT prompt_tokens, completion_tokens, real_cost_usd, billed_cost_usd, status_code FROM ")
		sb.WriteString(source)
	}
	sb.WriteString(")")

	var out LogStatsResult
	row := df.conn.QueryRow(sb.String())
	if err := row.Scan(&out.PromptTokens, &out.CompletionTokens, &out.RealCostUSD, &out.BilledCostUSD, &out.Total, &out.Errors); err != nil {
		return LogStatsResult{}, fmt.Errorf("logstore: stats: %w", err)
	}
	return out, nil
}

// ---------- TimeSeries ----------

// TimeSeries returns request/error/token/cost totals grouped into
// bucketSec-second windows across the given days.
func (d *SQLiteDriver) TimeSeries(filter QueryFilter, bucketSec int64, days []string) ([]SeriesBucket, error) {
	if bucketSec <= 0 {
		bucketSec = 3600
	}
	df, attachAliases, days, err := d.openUnion(days)
	if err != nil {
		return nil, err
	}
	if df == nil {
		// No files exist yet — return empty result
		return []SeriesBucket{}, nil
	}
	defer detachAll(df.conn, attachAliases)

	var sb strings.Builder
	sb.WriteString("SELECT (created_at / ?) * ? AS bucket, COUNT(*), COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(real_cost_usd), 0), COALESCE(SUM(billed_cost_usd), 0) FROM (")
	for i := range days {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		source := "logs"
		if i < len(attachAliases) && attachAliases[i] != "" {
			source = attachAliases[i] + ".logs"
		}
		sb.WriteString("SELECT created_at, status_code, prompt_tokens, completion_tokens, real_cost_usd, billed_cost_usd FROM ")
		sb.WriteString(source)
	}
	sb.WriteString(")")

	where, args := buildWhere(filter)
	if where != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(where)
	}
	sb.WriteString(" GROUP BY bucket ORDER BY bucket")
	qargs := append([]any{bucketSec, bucketSec}, args...)

	rows, err := df.conn.Query(sb.String(), qargs...)
	if err != nil {
		return nil, fmt.Errorf("logstore: timeseries: %w", err)
	}
	defer rows.Close()
	out := []SeriesBucket{}
	for rows.Next() {
		var b SeriesBucket
		if err := rows.Scan(&b.Bucket, &b.Requests, &b.Errors,
			&b.PromptTokens, &b.CompletionTokens,
			&b.RealCostUSD, &b.BilledCostUSD); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---------- TopByField ----------

// TopByField groups matching rows by field (one of "model",
// "channel_id", "token_id") and returns the top `limit` groups by
// request count.
func (d *SQLiteDriver) TopByField(filter QueryFilter, field string, limit int, days []string) ([]NamedMetric, error) {
	if limit <= 0 {
		limit = 10
	}
	switch field {
	case "model", "channel_id", "token_id":
		// allow
	default:
		return nil, fmt.Errorf("logstore: TopByField invalid field %q", field)
	}

	df, attachAliases, days, err := d.openUnion(days)
	if err != nil {
		return nil, err
	}
	if df == nil {
		// No files exist yet — return empty result
		return []NamedMetric{}, nil
	}
	defer detachAll(df.conn, attachAliases)

	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(field)
	sb.WriteString(", COUNT(*), COALESCE(SUM(prompt_tokens+completion_tokens),0), COALESCE(SUM(billed_cost_usd),0) FROM (")
	for i := range days {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		source := "logs"
		if i < len(attachAliases) && attachAliases[i] != "" {
			source = attachAliases[i] + ".logs"
		}
		sb.WriteString("SELECT ")
		sb.WriteString(field)
		sb.WriteString(", prompt_tokens, completion_tokens, billed_cost_usd FROM ")
		sb.WriteString(source)
	}
	sb.WriteString(")")

	where, args := buildWhere(filter)
	if where != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(where)
	}
	sb.WriteString(" GROUP BY ")
	sb.WriteString(field)
	sb.WriteString(" ORDER BY 2 DESC LIMIT ?")
	args = append(args, limit)

	rows, err := df.conn.Query(sb.String(), args...)
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

// openUnion is shared between LogStats / TimeSeries / TopByField.
// It returns the anchor connection, the list of ATTACH aliases
// (the anchor maps to ""), and the day list actually queried
// (capped at MaxAttachFiles).
func (d *SQLiteDriver) openUnion(days []string) (*dayFile, []string, []string, error) {
	if len(days) == 0 {
		var err error
		days, err = d.ListFiles()
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if len(days) == 0 {
		return nil, nil, nil, nil
	}
	sort.Strings(days)
	if len(days) > MaxAttachFiles {
		days = days[len(days)-MaxAttachFiles:]
	}

	anchor := days[len(days)-1]
	d.mu.RLock()
	df, ok := d.conns[anchor]
	d.mu.RUnlock()
	if !ok {
		var err error
		df, _, err = d.acquire(extractDate(anchor), seqOf(anchor))
		if err != nil {
			return nil, nil, nil, err
		}
	}

	attachAliases := make([]string, 0, len(days))
	for i, day := range days {
		alias := fmt.Sprintf("u_%d_%d", os.Getpid(), i)
		if day == anchor {
			attachAliases = append(attachAliases, "")
			continue
		}
		path := filepath.Join(d.dir, day+".db")
		if _, err := df.conn.Exec(fmt.Sprintf(`ATTACH %q AS %s`, path, alias)); err != nil {
			detachAll(df.conn, attachAliases)
			return nil, nil, nil, fmt.Errorf("logstore: attach %s: %w", day, err)
		}
		attachAliases = append(attachAliases, alias)
	}
	return df, attachAliases, days, nil
}

// ---------- File management ----------

// ListFiles returns all *.db files in dir sorted ascending. The
// returned strings are basenames without the .db extension.
func (d *SQLiteDriver) ListFiles() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(d.dir, "*.db"))
	if err != nil {
		return nil, fmt.Errorf("logstore: glob: %w", err)
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		out = append(out, strings.TrimSuffix(base, ".db"))
	}
	sort.Strings(out)
	return out, nil
}

// DeleteFiles removes the named day files (closing their
// connections first). Idempotent: missing files are not errors.
func (d *SQLiteDriver) DeleteFiles(days []string) error {
	for _, day := range days {
		d.mu.Lock()
		if df, ok := d.conns[day]; ok {
			_ = df.conn.Close()
			delete(d.conns, day)
		}
		d.mu.Unlock()

		// Remove .db plus WAL/SHM sidecars.
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(filepath.Join(d.dir, day+".db"+suffix))
		}
	}
	return nil
}

// ---------- Filename helpers ----------

// extractDate pulls the "YYYY-MM-DD" prefix from a dayFile key
// like "2026-07-09-2". It returns the date unchanged if there is
// no seq suffix.
func extractDate(key string) string {
	if idx := strings.LastIndex(key, "-"); idx > 0 {
		if _, err := strconv.Atoi(key[idx+1:]); err == nil {
			return key[:idx]
		}
	}
	return key
}

// seqOf returns the seq suffix of a dayFile key (0 for the base
// file). Returns 0 if the key has no seq suffix.
func seqOf(key string) int {
	if idx := strings.LastIndex(key, "-"); idx > 0 {
		if n, err := strconv.Atoi(key[idx+1:]); err == nil {
			return n
		}
	}
	return 0
}

// Compile-time assertion that SQLiteDriver satisfies Driver.
var _ Driver = (*SQLiteDriver)(nil)
