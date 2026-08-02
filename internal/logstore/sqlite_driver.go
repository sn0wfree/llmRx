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
// rollover into a new seq file (YYYY-MM-DD-N.db). A variable (not
// const) so tests can lower it and exercise rollover cheaply.
var MaxFileBytes = int64(100 * 1024 * 1024)

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
	// syncMode is the SQLite synchronous level for log files:
	// "normal" (default, fsync on checkpoint only) or "off"
	// (no fsync at all — ~2-5x write throughput, but a process or
	// OS crash can lose the most recent ~1s of committed rows,
	// equivalent to Redis AOF "everysec" semantics).
	syncMode string

	mu    sync.RWMutex
	conns map[string]*dayFile // dayFile (basename without .db) → state
	// current tracks, per UTC date, the active dayFile key (base
	// date or date-N after rollover). acquire consults it first so
	// steady-state writes reuse the open pool instead of re-opening
	// the same file on every batch (which leaked one pool per call
	// when the active file lived under a -N key).
	current map[string]string
}

// dayFile tracks a single on-disk log file and its open connection.
type dayFile struct {
	conn         *sql.DB
	bytesWritten int64 // atomic; updated by Insert, calibrated at Open
}

// NewSQLiteDriver returns an unopened driver. Call Open before use.
func NewSQLiteDriver() *SQLiteDriver {
	return &SQLiteDriver{
		conns:    make(map[string]*dayFile),
		current:  make(map[string]string),
		maxOpen:  4, // today + 3 historical
		syncMode: "normal",
	}
}

// SetSynchronous overrides the per-file synchronous level. Accepts
// "normal" (default) or "off". Must be called before Open/acquire
// so every connection inherits it via the DSN.
func (d *SQLiteDriver) SetSynchronous(mode string) error {
	switch mode {
	case "", "normal":
		d.syncMode = "normal"
	case "off":
		d.syncMode = "off"
	default:
		return fmt.Errorf("logstore: invalid synchronous mode %q (want normal|off)", mode)
	}
	return nil
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
		`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.TokenID, entry.ChannelID, entry.KeyID, entry.Model,
		entry.PromptTokens, entry.CompletionTokens, entry.CachedTokens,
		entry.RealCostUSD, entry.BilledCostUSD,
		entry.DurationMs, entry.StatusCode, entry.RouterPath, entry.RequestIP,
		entry.Endpoint, entry.Units,
		entry.CreatedAt.UTC().Unix(),
	); err != nil {
		return fmt.Errorf("logstore: insert %s: %w", dayFileKey, err)
	}

	atomic.AddInt64(&df.bytesWritten, estimatedRowBytes)
	return nil
}

// BatchInsert inserts multiple entries in a single SQL transaction.
// All entries must share the same UTC date (the first entry's date is
// used). Returns the number of rows inserted.
func (d *SQLiteDriver) BatchInsert(entries []*model.Log) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}

	for _, e := range entries {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = Now()
		}
	}

	date := entries[0].CreatedAt.UTC().Format("2006-01-02")
	df, dayFileKey, err := d.acquire(date, 0)
	if err != nil {
		return 0, err
	}

	tx, err := df.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("logstore: begin tx %s: %w", dayFileKey, err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("logstore: prepare %s: %w", dayFileKey, err)
	}
	defer stmt.Close()

	for _, entry := range entries {
		if _, err := stmt.Exec(
			entry.TokenID, entry.ChannelID, entry.KeyID, entry.Model,
			entry.PromptTokens, entry.CompletionTokens, entry.CachedTokens,
			entry.RealCostUSD, entry.BilledCostUSD,
			entry.DurationMs, entry.StatusCode, entry.RouterPath, entry.RequestIP,
			entry.Endpoint, entry.Units,
			entry.CreatedAt.UTC().Unix(),
		); err != nil {
			return 0, fmt.Errorf("logstore: batch insert %s: %w", dayFileKey, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("logstore: commit %s: %w", dayFileKey, err)
	}

	atomic.AddInt64(&df.bytesWritten, int64(len(entries))*estimatedRowBytes)
	return len(entries), nil
}

// acquire returns the dayFile to write into for the given date. It
// honours the MaxFileBytes rollover: if the current seq file is
// full, the call promotes to the next seq. seqHint is reserved for
// future use (currently unused; rollover is derived from the
// counter).
func (d *SQLiteDriver) acquire(date string, seqHint int) (*dayFile, string, error) {
	// seqHint > 0 means the caller is asking for a specific seq
	// file (typically the anchor in QueryAcross). The fast path
	// looks it up by the exact dayFileKey, not just the date, so
	// per-seq entries are reachable without falling through to
	// the slow path.
	key := date
	if seqHint > 0 {
		key = dayFileKey(date, seqHint)
	}

	// Fast path: file is in cache and below threshold.
	d.mu.RLock()
	if seqHint == 0 {
		// Steady state: reuse the active file's pool. Without this
		// the slow path re-opened the same -N file on every call,
		// leaking a pool (and its fds) per batch.
		if cur, ok := d.current[date]; ok {
			key = cur
		}
	}
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
		// Full: checkpoint the WAL into the file (so the sealed
		// file is complete and its WAL doesn't linger at 100MB+),
		// then close and evict; fall through to next seq. The
		// file has no writers now, so TRUNCATE reliably completes.
		checkpointWithRetry(df.conn, "TRUNCATE", 20)
		_ = df.conn.Close()
		delete(d.conns, key)
		if d.current[date] == key {
			delete(d.current, date)
		}
	}

	// LRU eviction if cache is over budget: close one entry.
	if len(d.conns) >= d.maxOpen {
		for k, df := range d.conns {
			// Compact the WAL before closing so a sealed file
			// doesn't keep a 4MB+ WAL on disk.
			checkpointWithRetry(df.conn, "TRUNCATE", 20)
			_ = df.conn.Close()
			delete(d.conns, k)
			if d.current[date] == k {
				delete(d.current, date)
			}
			break
		}
	}

	// Find next free seq for this date (base, then -1, -2, ...).
	// When seqHint > 0, prefer that slot first (used by the
	// anchor lookup in QueryAcross so a non-zero seq file can be
	// opened even when its seq=0 sibling exists on disk).
	seq := seqHint
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
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous="+d.syncMode+"&_busy_timeout=5000")
	if err != nil {
		return nil, "", fmt.Errorf("logstore: open %s: %w", path, err)
	}
	// Hard cap on the WAL file size: even when checkpoint is
	// starved by a long-running reader, the WAL stays bounded
	// (sqlite truncates it back after the next checkpoint).
	// 32MB is the configured ceiling; the periodic maintenance
	// checkpoint keeps it far below in practice.
	if _, err := conn.Exec("PRAGMA journal_size_limit=33554432"); err != nil {
		_ = conn.Close()
		return nil, "", fmt.Errorf("logstore: journal_size_limit %s: %w", key, err)
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
	d.current[date] = key
	return df, key, nil
}

// checkpointConn runs a checkpoint on a connection and reports
// whether it completed. SQLite returns a busy count in the pragma's
// RESULT ROW (not as an error), so Exec would silently swallow it —
// a concurrent writer would leave the WAL un-compacted. QueryRow
// reads the (busy, log, checkpointed) triple; busy==0 means done.
func checkpointConn(conn *sql.DB, mode string) (busy int) {
	_ = conn.QueryRow("PRAGMA wal_checkpoint("+mode+")").Scan(&busy, new(int), new(int))
	return busy
}

// checkpointWithRetry compacts a file's WAL, retrying while the
// checkpoint reports busy (a writer or another checkpointer got the
// lock first). Sealed files have no writers, so a handful of retries
// reliably completes; the busy_timeout on the DSN bounds each try.
func checkpointWithRetry(conn *sql.DB, mode string, retries int) {
	for i := 0; i <= retries; i++ {
		if checkpointConn(conn, mode) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CheckpointActive compacts the WAL of every cached day file
// (TRUNCATE resets each WAL file to zero bytes, so a 240MB WAL
// returns to ~0). Called periodically by the Manager; safe to run
// concurrently with inserts (SQLite serialises). Checking every
// cached file (≤ maxOpen) keeps sealed-but-cached files compact
// too, not just the active writer. The active file may report busy
// under a write burst — the next tick retries.
func (d *SQLiteDriver) CheckpointActive() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, df := range d.conns {
		checkpointWithRetry(df.conn, "TRUNCATE", 3)
	}
	return nil
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
			endpoint TEXT NOT NULL DEFAULT '',
			units INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_created ON logs(created_at)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	// Best-effort column migration for files created before the
	// endpoint/units columns landed. SQLite ALTER TABLE ADD COLUMN
	// fails if the column already exists, so probe first.
	if err := addLogColumn(db, "endpoint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addLogColumn(db, "units", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

// addLogColumn adds a column to the logs table if it does not
// already exist. Used to migrate existing day files forward.
func addLogColumn(db *sql.DB, column, decl string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('logs')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE logs ADD COLUMN ` + column + ` ` + decl)
	return err
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
	// Sort by (date, seq) numerically so same-day seq=0 (base)
	// lands adjacent to seq=1, seq=2 etc. Then truncate by date:
	// when we exceed MaxAttachFiles, drop the **oldest dates
	// entirely** rather than slicing mid-day and losing the
	// first 100 MiB of a retained date.
	days = sortByDateSeq(days)
	days = truncateByDate(days, MaxAttachFiles)

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
	sb.WriteString("SELECT id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at FROM (")
	for i := range days {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		source := "logs"
		if i < len(attachAliases) && attachAliases[i] != "" {
			source = attachAliases[i] + ".logs"
		}
		sb.WriteString("SELECT id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, endpoint, units, created_at FROM ")
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

	// Size the result slice from the actual remaining rows when
	// possible — a sparse page (e.g. limit=500 on a query that
	// returns 1 row) used to over-allocate ~70 KiB of unused
	// model.Log backing storage. total >= offset is guaranteed
	// by the caller; remaining can be 0 (offset beyond end).
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
	if f.Endpoint != "" {
		conds = append(conds, "endpoint = ?")
		args = append(args, f.Endpoint)
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
	// Same sort + truncate strategy as QueryAcross: keep every
	// seq of the most recent dates instead of slicing mid-day.
	days = sortByDateSeq(days)
	days = truncateByDate(days, MaxAttachFiles)

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
	// Same sort + truncate strategy as QueryAcross: keep every
	// seq of the most recent dates instead of slicing mid-day.
	days = sortByDateSeq(days)
	days = truncateByDate(days, MaxAttachFiles)

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
// like "2026-07-09-2". The base file "2026-07-09" (seq=0) has no
// suffix and is returned unchanged.
//
// The key has the format "YYYY-MM-DD[-N]" where N is the optional
// seq suffix. We count hyphens to distinguish "2026-07-09" (date
// only, 2 hyphens) from "2026-07-09-2" (date + seq, 3 hyphens).
// Treating the "09" of "2026-07-09" as a seq suffix (because it
// parses as a number) collapses every month's files into a single
// group, which is exactly the bug we are fixing.
func extractDate(key string) string {
	if strings.Count(key, "-") >= 3 {
		if idx := strings.LastIndex(key, "-"); idx > 0 {
			return key[:idx]
		}
	}
	return key
}

// seqOf returns the seq suffix of a dayFile key (0 for the base
// file). Returns 0 if the key has no seq suffix.
func seqOf(key string) int {
	if strings.Count(key, "-") >= 3 {
		if idx := strings.LastIndex(key, "-"); idx > 0 {
			if n, err := strconv.Atoi(key[idx+1:]); err == nil {
				return n
			}
		}
	}
	return 0
}

// Compile-time assertion that SQLiteDriver satisfies Driver.
var _ Driver = (*SQLiteDriver)(nil)

// sortByDateSeq orders day-file basenames by (date, seq) using
// numeric comparison for the seq suffix. The pure string sort in
// ListFiles / QueryAcross interleaves "2026-07-22" between
// "2026-07-22-1" and "2026-07-22-2" because '-' (0x2D) < '0'
// (0x30), which makes the seq=0 base file land *first* in the
// list. With a max-files truncation, the base file then falls off
// the head of the slice and the day's first 100 MiB of logs
// silently disappear from every aggregate. This comparator fixes
// that by ordering same-day seqs numerically.
func sortByDateSeq(names []string) []string {
	out := make([]string, len(names))
	copy(out, names)
	sort.SliceStable(out, func(i, j int) bool {
		di, si := extractDate(out[i]), seqOf(out[i])
		dj, sj := extractDate(out[j]), seqOf(out[j])
		if di != dj {
			return di < dj
		}
		return si < sj
	})
	return out
}

// truncateByDate groups day files by date and, when the total
// exceeds the budget, drops the **oldest dates entirely** so every
// seq within a retained date survives. This is the correct
// behaviour for SQLite ATTACH with a hard file cap: rather than
// trim a slice of files (which may slice through a day's seq
// range), we sacrifice the oldest day(s).
func truncateByDate(names []string, budget int) []string {
	if len(names) <= budget {
		return names
	}
	// Group preserving input order (caller already sorted).
	type dayGroup struct {
		date  string
		files []string
	}
	var groups []dayGroup
	idx := map[string]int{}
	for _, n := range names {
		d := extractDate(n)
		if i, ok := idx[d]; ok {
			groups[i].files = append(groups[i].files, n)
			continue
		}
		idx[d] = len(groups)
		groups = append(groups, dayGroup{date: d, files: []string{n}})
	}
	// Drop leading groups until total fits.
	total := len(names)
	drop := 0
	for total > budget && drop < len(groups)-1 {
		total -= len(groups[drop].files)
		drop++
	}
	if drop == 0 {
		return names
	}
	out := make([]string, 0, total)
	for _, g := range groups[drop:] {
		out = append(out, g.files...)
	}
	return out
}
