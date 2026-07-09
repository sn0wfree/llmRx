package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// errNotImplemented is returned by Phase 1.5 reserved BYOK
	// store methods until the feature ships. Keeping it unexported
	// so callers don't depend on the wire format.
	errNotImplemented = errors.New("not implemented (Phase 1.5 reserved)")
)

type SQLite struct {
	db       *sql.DB
	logStore *logstore.Manager // nil until SetLogStore is called
	Secrets  *secrets.Manager  // nil ⇒ plaintext only (legacy mode); set by SetSecrets
}

// SetSecrets attaches a secrets manager used to encrypt new key rows
// and decrypt existing ones. When set, the store will:
//   - encrypt any plaintext Key field on Create
//   - decrypt KeyCiphertext on every read, falling back to the
//     legacy plaintext Key column for rows written before the
//     migration landed.
func (s *SQLite) SetSecrets(m *secrets.Manager) { s.Secrets = m }

// SetLogStore wires the per-date file log store. After this call
// the legacy CreateLog/QueryLogs/LogStats/DeleteLogsBefore methods
// delegate into the logstore package; the logs table is no longer
// present in the main DB. Required at startup; the call is
// idempotent (a second call replaces the manager).
func (s *SQLite) SetLogStore(m *logstore.Manager) { s.logStore = m }

// Ping verifies the underlying database connection is responsive.
// Returns nil when the connection is healthy.
func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func OpenSQLite(dsn string) (*SQLite, error) {
	if dsn == "" {
		return nil, errors.New("empty dsn")
	}
	if dir := filepath.Dir(dsn); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	// DSN-level pragmas: WAL + busy_timeout + foreign keys + sync mode.
	// Per-connection pragmas (cache, mmap, wal_autocheckpoint) are
	// applied in applyPragmas() after Open.
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
	// Allow concurrent reads + 1 writer. SQLite serialises writers
	// internally; 8 connections is enough headroom for admin reads
	// while the LLM hot path is a single goroutine.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	s := &SQLite{db: db}
	if err := s.applyPragmas(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// applyPragmas sets SQLite pragmas that can't be passed via DSN.
// Note: pragmas are per-connection; Go's database/sql reuses
// connections so subsequent queries inherit these settings.
func (s *SQLite) applyPragmas() error {
	pragmas := []string{
		"PRAGMA cache_size=-20000",       // 20MB page cache
		"PRAGMA temp_store=MEMORY",       // temp tables in RAM
		"PRAGMA mmap_size=268435456",     // 256MB mmap for large reads
		"PRAGMA wal_autocheckpoint=2000", // 2000-page WAL threshold
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role INTEGER NOT NULL DEFAULT 0,
			status INTEGER NOT NULL DEFAULT 1,
			session_token TEXT NOT NULL DEFAULT '',
			session_exp INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			provider TEXT NOT NULL,
			protocol TEXT NOT NULL DEFAULT 'openai',
			base_url TEXT NOT NULL,
			models TEXT NOT NULL,
			intents TEXT NOT NULL DEFAULT '[]',
			priority INTEGER NOT NULL DEFAULT 0,
			input_price REAL NOT NULL DEFAULT 0,
			output_price REAL NOT NULL DEFAULT 0,
			cached_input_discount REAL NOT NULL DEFAULT 0.1,
			circuit_breaker TEXT NOT NULL DEFAULT '{}',
			status INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER NOT NULL,
			key TEXT NOT NULL,
			key_masked TEXT NOT NULL,
			status INTEGER NOT NULL DEFAULT 0,
			last_used_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS plans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			budget_usd REAL NOT NULL DEFAULT 0,
			used_usd REAL NOT NULL DEFAULT 0,
			markup_ratio REAL NOT NULL DEFAULT 1.0,
			status INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id INTEGER NOT NULL DEFAULT 0,
			key TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL DEFAULT 0,
			rpm INTEGER NOT NULL DEFAULT 0,
			tpm INTEGER NOT NULL DEFAULT 0,
			used_usd REAL NOT NULL DEFAULT 0,
			models_whitelist TEXT NOT NULL DEFAULT '[]',
			ip_whitelist TEXT NOT NULL DEFAULT '[]',
			expires_at INTEGER NOT NULL DEFAULT 0,
			last_used_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		// Note: the logs table is intentionally absent. Logs now live
		// in per-date files under data/logs/YYYY-MM-DD.db via the
		// logstore package. CreateLog/QueryLogs/etc. on this Store
		// delegate there (see SetLogStore).
		`CREATE INDEX IF NOT EXISTS idx_keys_channel ON keys(channel_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tokens_plan ON tokens(plan_id)`,
		`CREATE INDEX IF NOT EXISTS idx_users_session_exp ON users(session_exp) WHERE session_exp > 0`,
		`CREATE TABLE IF NOT EXISTS alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			threshold REAL NOT NULL,
			window_sec INTEGER NOT NULL,
			cooldown_sec INTEGER NOT NULL,
			webhook_url TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			last_fired_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS alert_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alert_id INTEGER NOT NULL,
			alert_name TEXT NOT NULL DEFAULT '',
			alert_type TEXT NOT NULL DEFAULT '',
			fired_at INTEGER NOT NULL,
			payload TEXT NOT NULL DEFAULT '',
			delivered_webhook INTEGER NOT NULL DEFAULT 0,
			acknowledged INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (alert_id) REFERENCES alerts(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_alert_events_fired ON alert_events(fired_at DESC)`,
		`CREATE TABLE IF NOT EXISTS runtime_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			settings_json TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec %q: %w", q, err)
		}
	}
	if err := s.addColumnIfMissing("users", "session_exp", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("channels", "protocol", "TEXT NOT NULL DEFAULT 'openai'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("channels", "intents", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("channels", "cached_input_discount", "REAL NOT NULL DEFAULT 0.1"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("tokens", "used_usd", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return s.addColumnIfMissing("keys", "key_ciphertext", "TEXT NOT NULL DEFAULT ''")
}

func (s *SQLite) addColumnIfMissing(table, column, decl string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	return err
}

func toUnix(t time.Time) int64   { return t.Unix() }
func fromUnix(s int64) time.Time { return time.Unix(s, 0).UTC() }

func encodeStrings(xs []string) string {
	if xs == nil {
		xs = []string{}
	}
	b, _ := json.Marshal(xs)
	return string(b)
}

func decodeStrings(s string) []string {
	if s == "" {
		return nil
	}
	var xs []string
	_ = json.Unmarshal([]byte(s), &xs)
	return xs
}

func encodeCB(cb model.CircuitBreakerConfig) string {
	b, _ := json.Marshal(cb)
	return string(b)
}

func decodeCB(s string) model.CircuitBreakerConfig {
	if s == "" {
		return model.CircuitBreakerConfig{}
	}
	var cb model.CircuitBreakerConfig
	_ = json.Unmarshal([]byte(s), &cb)
	return cb
}

// ---------------- Channels ----------------

func (s *SQLite) GetChannels() ([]model.Channel, error) {
	rows, err := s.db.Query(`SELECT id, name, provider, protocol, base_url, models, intents, priority, input_price, output_price, cached_input_discount, circuit_breaker, status, created_at, updated_at FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, rows.Err()
}

func (s *SQLite) GetChannel(id int64) (*model.Channel, error) {
	row := s.db.QueryRow(`SELECT id, name, provider, protocol, base_url, models, intents, priority, input_price, output_price, cached_input_discount, circuit_breaker, status, created_at, updated_at FROM channels WHERE id = ?`, id)
	return scanChannel(row)
}

func (s *SQLite) CreateChannel(ch *model.Channel) error {
	now := time.Now().UTC()
	ch.CreatedAt = now
	ch.UpdatedAt = now
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	}
	if ch.CachedInputDiscount == 0 {
		ch.CachedInputDiscount = 0.1
	}
	res, err := s.db.Exec(
		`INSERT INTO channels(name, provider, protocol, base_url, models, intents, priority, input_price, output_price, cached_input_discount, circuit_breaker, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.Name, ch.Provider, ch.Protocol, ch.BaseURL,
		encodeStrings(ch.Models), encodeStrings(ch.Intents),
		ch.Priority, ch.InputPrice, ch.OutputPrice, ch.CachedInputDiscount, encodeCB(ch.CircuitBreaker),
		int(ch.Status), toUnix(ch.CreatedAt), toUnix(ch.UpdatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	ch.ID = id
	return nil
}

func (s *SQLite) UpdateChannel(ch *model.Channel) error {
	ch.UpdatedAt = time.Now().UTC()
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	}
	_, err := s.db.Exec(
		`UPDATE channels SET name=?, provider=?, protocol=?, base_url=?, models=?, intents=?, priority=?, input_price=?, output_price=?, cached_input_discount=?, circuit_breaker=?, status=?, updated_at=? WHERE id=?`,
		ch.Name, ch.Provider, ch.Protocol, ch.BaseURL,
		encodeStrings(ch.Models), encodeStrings(ch.Intents),
		ch.Priority, ch.InputPrice, ch.OutputPrice, ch.CachedInputDiscount, encodeCB(ch.CircuitBreaker),
		int(ch.Status), toUnix(ch.UpdatedAt), ch.ID,
	)
	return err
}

func (s *SQLite) DeleteChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

func scanChannel(r interface {
	Scan(dest ...any) error
}) (*model.Channel, error) {
	var ch model.Channel
	var modelsJSON, intentsJSON, cbJSON string
	var status int
	var created, updated int64
	if err := r.Scan(&ch.ID, &ch.Name, &ch.Provider, &ch.Protocol, &ch.BaseURL,
		&modelsJSON, &intentsJSON, &ch.Priority,
		&ch.InputPrice, &ch.OutputPrice, &ch.CachedInputDiscount, &cbJSON, &status, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ch.Models = decodeStrings(modelsJSON)
	ch.Intents = decodeStrings(intentsJSON)
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	}
	ch.CircuitBreaker = decodeCB(cbJSON)
	ch.Status = model.ChannelStatus(status)
	ch.CreatedAt = fromUnix(created)
	ch.UpdatedAt = fromUnix(updated)
	return &ch, nil
}

// ---------------- Keys ----------------

func (s *SQLite) GetKeys(channelID int64) ([]model.Key, error) {
	rows, err := s.db.Query(`SELECT id, channel_id, key, key_ciphertext, key_masked, status, last_used_at, created_at FROM keys WHERE channel_id = ? ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	type rawRow struct {
		k      model.Key
		plain  string
		cipher string
	}
	var raws []rawRow
	for rows.Next() {
		var rr rawRow
		var status, lastUsed, created int64
		if err := rows.Scan(&rr.k.ID, &rr.k.ChannelID, &rr.plain, &rr.cipher, &rr.k.KeyMasked, &status, &lastUsed, &created); err != nil {
			rows.Close()
			return nil, err
		}
		rr.k.Status = model.KeyStatus(status)
		rr.k.LastUsedAt = fromUnix(lastUsed)
		rr.k.CreatedAt = fromUnix(created)
		raws = append(raws, rr)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Decrypt / migrate outside the row cursor: SetMaxOpenConns(1)
	// means a nested Exec would deadlock while the read connection
	// is still pinned. We do all writes in a second pass.
	out := make([]model.Key, 0, len(raws))
	for _, rr := range raws {
		if s.Secrets != nil {
			if rr.cipher != "" {
				pt, derr := s.Secrets.Decrypt(rr.cipher)
				if derr != nil {
					// Corrupt ciphertext: refuse rather than return
					// empty plaintext — sending "Authorization:
					// Bearer " upstream is worse than failing.
					return nil, fmt.Errorf("decrypt key id=%d: %w", rr.k.ID, derr)
				}
				rr.k.Key = string(pt)
			} else if rr.plain != "" {
				// Legacy row: plaintext column has the value.
				rr.k.Key = rr.plain
				// Best-effort background migration to ciphertext.
				// Failures are retried on the next read.
				if ct, eerr := s.Secrets.Encrypt([]byte(rr.plain)); eerr == nil {
					_, _ = s.db.Exec(`UPDATE keys SET key='', key_ciphertext=? WHERE id=?`, ct, rr.k.ID)
				}
			}
		} else {
			// No secrets manager configured (legacy mode): serve
			// plaintext. If a row carries ciphertext we cannot
			// decode it, so refuse rather than return "".
			if rr.cipher != "" {
				return nil, fmt.Errorf("decrypt key id=%d (no manager): ciphertext present but no secrets manager configured", rr.k.ID)
			}
			rr.k.Key = rr.plain
		}
		out = append(out, rr.k)
	}
	return out, nil
}

func (s *SQLite) CreateKey(k *model.Key) error {
	k.CreatedAt = time.Now().UTC()
	plain := k.Key
	if plain == "" {
		return errors.New("key is empty")
	}
	cipher := ""
	storedPlain := plain // default for legacy mode
	if s.Secrets != nil {
		ct, err := s.Secrets.Encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("encrypt key: %w", err)
		}
		cipher = ct
		storedPlain = "" // never store plaintext when a manager is attached
	}
	res, err := s.db.Exec(
		`INSERT INTO keys(channel_id, key, key_ciphertext, key_masked, status, last_used_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		k.ChannelID, storedPlain, cipher, k.KeyMasked, int(k.Status), toUnix(k.LastUsedAt), toUnix(k.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	k.ID = id
	// Leave k.Key populated in-memory so callers (e.g. admin
	// response, pool seeding) get the plaintext immediately.
	return nil
}

func (s *SQLite) DeleteKey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM keys WHERE id = ?`, id)
	return err
}

func (s *SQLite) ReencryptAllKeys(oldMgr, newMgr *secrets.Manager) (int, error) {
	rows, err := s.db.Query(`SELECT id, key_ciphertext FROM keys WHERE key_ciphertext != ''`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type kr struct {
		id     int64
		cipher string
	}
	var keys []kr
	for rows.Next() {
		var r kr
		if err := rows.Scan(&r.id, &r.cipher); err != nil {
			return 0, err
		}
		keys = append(keys, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	for _, r := range keys {
		pt, err := oldMgr.Decrypt(r.cipher)
		if err != nil {
			return 0, fmt.Errorf("decrypt key %d: %w", r.id, err)
		}
		newCT, err := newMgr.Encrypt(pt)
		if err != nil {
			return 0, fmt.Errorf("encrypt key %d: %w", r.id, err)
		}
		if _, err := s.db.Exec(`UPDATE keys SET key_ciphertext = ? WHERE id = ?`, newCT, r.id); err != nil {
			return 0, fmt.Errorf("update key %d: %w", r.id, err)
		}
	}
	return len(keys), nil
}

func (s *SQLite) RotateMasterKey(newKeyHex string) (int, error) {
	m, err := secrets.FromHexKey(newKeyHex)
	if err != nil {
		return 0, err
	}
	if s.Secrets == nil {
		return 0, errors.New("no secrets manager configured; cannot rotate")
	}
	n, err := s.ReencryptAllKeys(s.Secrets, m)
	if err != nil {
		return 0, err
	}
	s.Secrets = m
	return n, nil
}

// ---------------- Tokens ----------------

func (s *SQLite) GetToken(key string) (*model.Token, error) {
	row := s.db.QueryRow(`SELECT id, plan_id, key, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at FROM tokens WHERE key = ?`, key)
	return scanToken(row)
}

func (s *SQLite) GetTokenByID(id int64) (*model.Token, error) {
	row := s.db.QueryRow(`SELECT id, plan_id, key, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at FROM tokens WHERE id = ?`, id)
	return scanToken(row)
}

func (s *SQLite) GetTokens() ([]model.Token, error) {
	rows, err := s.db.Query(`SELECT id, plan_id, key, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *SQLite) CreateToken(t *model.Token) error {
	t.CreatedAt = time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO tokens(plan_id, key, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.PlanID, t.Key, t.Name, int(t.Status), t.RPM, t.TPM, t.UsedUSD,
		encodeStrings(t.ModelsWhitelist), encodeStrings(t.IPWhitelist),
		toUnix(t.ExpiresAt), toUnix(t.LastUsedAt), toUnix(t.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	t.ID = id
	return nil
}

func (s *SQLite) UpdateToken(t *model.Token) error {
	t.LastUsedAt = time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE tokens SET plan_id=?, name=?, status=?, rpm=?, tpm=?, used_usd=?, models_whitelist=?, ip_whitelist=?, expires_at=? WHERE id=?`,
		t.PlanID, t.Name, int(t.Status), t.RPM, t.TPM, t.UsedUSD,
		encodeStrings(t.ModelsWhitelist), encodeStrings(t.IPWhitelist),
		toUnix(t.ExpiresAt), t.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) IncrementTokenSpend(tokenID int64, amount float64) error {
	if amount == 0 {
		return nil
	}
	res, err := s.db.Exec(
		`UPDATE tokens SET used_usd = used_usd + ? WHERE id = ?`, amount, tokenID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) IncrementPlanSpend(planID int64, amount float64) error {
	if amount == 0 || planID == 0 {
		return nil
	}
	res, err := s.db.Exec(
		`UPDATE plans SET used_usd = used_usd + ? WHERE id = ?`, amount, planID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) DeleteToken(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	return err
}

func scanToken(r interface {
	Scan(dest ...any) error
}) (*model.Token, error) {
	var t model.Token
	var status int
	var mwJSON, ipwJSON string
	var expires, lastUsed, created int64
	if err := r.Scan(&t.ID, &t.PlanID, &t.Key, &t.Name, &status, &t.RPM, &t.TPM,
		&t.UsedUSD, &mwJSON, &ipwJSON, &expires, &lastUsed, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Status = model.TokenStatus(status)
	t.ModelsWhitelist = decodeStrings(mwJSON)
	t.IPWhitelist = decodeStrings(ipwJSON)
	t.ExpiresAt = fromUnix(expires)
	t.LastUsedAt = fromUnix(lastUsed)
	t.CreatedAt = fromUnix(created)
	return &t, nil
}

// ---------------- Plans ----------------

func (s *SQLite) GetPlans() ([]model.Plan, error) {
	rows, err := s.db.Query(`SELECT id, name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at FROM plans ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Plan
	for rows.Next() {
		var p model.Plan
		var status, created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &p.BudgetUSD, &p.UsedUSD, &p.MarkupRatio, &status, &created, &updated); err != nil {
			return nil, err
		}
		p.Status = int(status)
		p.CreatedAt = fromUnix(created)
		p.UpdatedAt = fromUnix(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLite) GetPlan(id int64) (*model.Plan, error) {
	row := s.db.QueryRow(`SELECT id, name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at FROM plans WHERE id = ?`, id)
	var p model.Plan
	var status, created, updated int64
	if err := row.Scan(&p.ID, &p.Name, &p.BudgetUSD, &p.UsedUSD, &p.MarkupRatio, &status, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Status = int(status)
	p.CreatedAt = fromUnix(created)
	p.UpdatedAt = fromUnix(updated)
	return &p, nil
}

func (s *SQLite) CreatePlan(p *model.Plan) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	res, err := s.db.Exec(
		`INSERT INTO plans(name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.BudgetUSD, p.UsedUSD, p.MarkupRatio, p.Status, toUnix(p.CreatedAt), toUnix(p.UpdatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	p.ID = id
	return nil
}

func (s *SQLite) UpdatePlan(p *model.Plan) error {
	p.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE plans SET name=?, budget_usd=?, used_usd=?, markup_ratio=?, status=?, updated_at=? WHERE id=?`,
		p.Name, p.BudgetUSD, p.UsedUSD, p.MarkupRatio, p.Status, toUnix(p.UpdatedAt), p.ID,
	)
	return err
}

// DeletePlan removes a plan row. Tokens that referenced it keep
// plan_id pointing at the now-missing row; the chat pipeline
// treats plan_id=0 (or unknown) as "no plan limit". Callers
// (admin handler) are expected to null out tokens.plan_id FIRST
// when an explicit unlink is desired.
func (s *SQLite) DeletePlan(id int64) error {
	_, err := s.db.Exec(`DELETE FROM plans WHERE id=?`, id)
	return err
}

// ---------------- Users ----------------

func (s *SQLite) GetUsers() ([]model.User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *SQLite) GetUser(id int64) (*model.User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *SQLite) GetUserByUsername(username string) (*model.User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *SQLite) GetUserBySession(token string) (*model.User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	now := time.Now().UTC().UnixMilli()
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users
		 WHERE session_token = ? AND status = 1 AND (session_exp = 0 OR session_exp > ?)`,
		token, now,
	)
	return scanUser(row)
}

func (s *SQLite) CreateUser(u *model.User) error {
	u.CreatedAt = time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO users(username, password_hash, role, status, session_token, session_exp, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, int(u.Role), u.Status, u.SessionToken, sessionExpUnix(u.SessionExp), toUnix(u.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	u.ID = id
	return nil
}

func (s *SQLite) UpdateUser(u *model.User) error {
	_, err := s.db.Exec(
		`UPDATE users SET password_hash=?, role=?, status=?, session_token=?, session_exp=? WHERE id=?`,
		u.PasswordHash, int(u.Role), u.Status, u.SessionToken, sessionExpUnix(u.SessionExp), u.ID,
	)
	return err
}

// CleanupExpiredSessions clears session_token for users whose
// session_exp is set and in the past. Returns rows affected.
func (s *SQLite) CleanupExpiredSessions() (int64, error) {
	now := time.Now().UTC().UnixMilli()
	res, err := s.db.Exec(
		`UPDATE users SET session_token = '' WHERE session_exp > 0 AND session_exp <= ?`,
		now,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func sessionExpUnix(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

func scanUser(r interface {
	Scan(dest ...any) error
}) (*model.User, error) {
	var u model.User
	var role int
	var sessionExp, created int64
	if err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &u.Status, &u.SessionToken, &sessionExp, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Role = model.UserRole(role)
	u.CreatedAt = fromUnix(created)
	if sessionExp > 0 {
		exp := time.UnixMilli(sessionExp).UTC()
		u.SessionExp = &exp
	}
	return &u, nil
}

// ---------------- Logs ----------------
// Logs now live in per-date SQLite files under data/logs/. Every
// method below delegates to the logstore Manager wired in via
// SetLogStore. The methods on this Store still exist so admin and
// api call sites don't change.

func (s *SQLite) CreateLog(l *model.Log) error {
	if s.logStore == nil {
		return errors.New("logstore not initialized")
	}
	return s.logStore.Insert(l)
}

func (s *SQLite) GetLogs(limit, offset int) ([]model.Log, error) {
	if s.logStore == nil {
		return []model.Log{}, nil
	}
	out, _, err := s.logStore.Query(logstore.QueryFilter{Limit: limit, Offset: offset}, nil)
	return out, err
}

func (s *SQLite) CountLogs() (int64, error) {
	if s.logStore == nil {
		return 0, nil
	}
	_, total, err := s.logStore.Query(logstore.QueryFilter{Limit: 1}, nil)
	return total, err
}

// DeleteLogsBefore deletes every day file whose date is strictly
// before the cutoff. Returns the number of files removed. The
// unixSec argument is interpreted as a UTC timestamp; the file
// date is derived from it.
func (s *SQLite) DeleteLogsBefore(unixSec int64) (int64, error) {
	if s.logStore == nil {
		return 0, nil
	}
	cutoff := time.Unix(unixSec, 0).UTC().Format("2006-01-02")
	files, err := s.logStore.ListFiles()
	if err != nil {
		return 0, err
	}
	var toDelete []string
	for _, f := range files {
		if extractDateForStore(f) < cutoff {
			toDelete = append(toDelete, f)
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	if err := s.logStore.DeleteFiles(toDelete); err != nil {
		return 0, err
	}
	return int64(len(toDelete)), nil
}

func (s *SQLite) LogStats() (LogStats, error) {
	if s.logStore == nil {
		return LogStats{}, nil
	}
	r, err := s.logStore.Stats(nil)
	if err != nil {
		return LogStats{}, err
	}
	return LogStats{
		PromptTokens:     r.PromptTokens,
		CompletionTokens: r.CompletionTokens,
		RealCostUSD:      r.RealCostUSD,
		BilledCostUSD:    r.BilledCostUSD,
		Total:            r.Total,
		Errors:           r.Errors,
	}, nil
}

func (s *SQLite) QueryLogs(f LogFilter) ([]model.Log, int64, error) {
	if s.logStore == nil {
		return []model.Log{}, 0, nil
	}
	return s.logStore.Query(toLogstoreFilter(f), nil)
}

// ---------------- Analytics ----------------

func (s *SQLite) TimeSeries(f LogFilter, bucketSec int64) ([]SeriesPoint, error) {
	if s.logStore == nil {
		return []SeriesPoint{}, nil
	}
	buckets, err := s.logStore.TimeSeries(toLogstoreFilter(f), bucketSec, nil)
	if err != nil {
		return nil, err
	}
	out := make([]SeriesPoint, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, SeriesPoint{
			Bucket:           b.Bucket,
			Requests:         b.Requests,
			Errors:           b.Errors,
			PromptTokens:     b.PromptTokens,
			CompletionTokens: b.CompletionTokens,
			RealCostUSD:      b.RealCostUSD,
			BilledCostUSD:    b.BilledCostUSD,
		})
	}
	return out, nil
}

func (s *SQLite) TopByModel(f LogFilter, limit int) ([]NamedMetric, error) {
	return s.topByFieldViaStore("model", f, limit)
}

func (s *SQLite) TopByChannel(f LogFilter, limit int) ([]NamedMetric, error) {
	return s.topByFieldViaStore("channel_id", f, limit)
}

func (s *SQLite) TopByToken(f LogFilter, limit int) ([]NamedMetric, error) {
	return s.topByFieldViaStore("token_id", f, limit)
}

func (s *SQLite) topByFieldViaStore(field string, f LogFilter, limit int) ([]NamedMetric, error) {
	if s.logStore == nil {
		return []NamedMetric{}, nil
	}
	out, err := s.logStore.TopByField(toLogstoreFilter(f), field, limit, nil)
	if err != nil {
		return nil, err
	}
	res := make([]NamedMetric, 0, len(out))
	for _, m := range out {
		res = append(res, NamedMetric{
			Label:  m.Label,
			Count:  m.Count,
			Tokens: m.Tokens,
			Cost:   m.Cost,
		})
	}
	return res, nil
}

// toLogstoreFilter converts the public LogFilter (which admin
// handlers use) into the logstore-internal QueryFilter.
func toLogstoreFilter(f LogFilter) logstore.QueryFilter {
	return logstore.QueryFilter{
		TokenID:     f.TokenID,
		ChannelID:   f.ChannelID,
		Model:       f.Model,
		StatusCode:  f.StatusCode,
		CreatedFrom: f.CreatedFrom,
		CreatedTo:   f.CreatedTo,
		Limit:       f.Limit,
		Offset:      f.Offset,
	}
}

// extractDateForStore pulls the YYYY-MM-DD prefix from a day file
// basename. For "2026-07-09" (base file) it returns "2026-07-09".
// For "2026-07-09-2" (rollover file) it returns "2026-07-09".
// It uses a date-format check rather than a numeric suffix check
// to avoid confusing the day number with a seq suffix.
func extractDateForStore(key string) string {
	// Try the longest known date prefix (YYYY-MM-DD-N).
	if len(key) >= len("2006-01-02-1") {
		candidate := key[:len("2006-01-02-1")-1]
		if _, err := time.Parse("2006-01-02", candidate); err == nil {
			return candidate
		}
	}
	// Try base YYYY-MM-DD.
	if len(key) >= len("2006-01-02") {
		candidate := key[:len("2006-01-02")]
		if _, err := time.Parse("2006-01-02", candidate); err == nil {
			return candidate
		}
	}
	return key
}

// ---------------- alerts ----------------

func (s *SQLite) GetAlerts() ([]model.Alert, error) {
	rows, err := s.db.Query(`SELECT id, name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, created_at FROM alerts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Alert
	for rows.Next() {
		var a model.Alert
		var enabled int
		var created int64
		if err := rows.Scan(&a.ID, &a.Name, &a.Type, &a.Threshold, &a.WindowSec, &a.CooldownSec, &a.WebhookURL, &enabled, &a.LastFiredAt, &created); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		a.CreatedAt = fromUnix(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLite) GetAlert(id int64) (*model.Alert, error) {
	row := s.db.QueryRow(`SELECT id, name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, created_at FROM alerts WHERE id=?`, id)
	var a model.Alert
	var enabled int
	var created int64
	if err := row.Scan(&a.ID, &a.Name, &a.Type, &a.Threshold, &a.WindowSec, &a.CooldownSec, &a.WebhookURL, &enabled, &a.LastFiredAt, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.Enabled = enabled != 0
	a.CreatedAt = fromUnix(created)
	return &a, nil
}

func (s *SQLite) CreateAlert(a *model.Alert) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO alerts(name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		a.Name, string(a.Type), a.Threshold, a.WindowSec, a.CooldownSec, a.WebhookURL, enabled, a.LastFiredAt, a.CreatedAt.Unix())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	a.ID = id
	return nil
}

func (s *SQLite) UpdateAlert(a *model.Alert) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`UPDATE alerts SET name=?, type=?, threshold=?, window_sec=?, cooldown_sec=?, webhook_url=?, enabled=?, last_fired_at=? WHERE id=?`,
		a.Name, string(a.Type), a.Threshold, a.WindowSec, a.CooldownSec, a.WebhookURL, enabled, a.LastFiredAt, a.ID)
	return err
}

func (s *SQLite) DeleteAlert(id int64) error {
	_, err := s.db.Exec(`DELETE FROM alerts WHERE id=?`, id)
	return err
}

func (s *SQLite) RecordAlertFired(id int64, atUnix int64) error {
	_, err := s.db.Exec(`UPDATE alerts SET last_fired_at=? WHERE id=?`, atUnix, id)
	return err
}

func (s *SQLite) CreateAlertEvent(e *model.AlertEvent) error {
	if e.FiredAt.IsZero() {
		e.FiredAt = time.Now()
	}
	delivered := 0
	if e.DeliveredWebhook {
		delivered = 1
	}
	ack := 0
	if e.Acknowledged {
		ack = 1
	}
	res, err := s.db.Exec(`INSERT INTO alert_events(alert_id, alert_name, alert_type, fired_at, payload, delivered_webhook, acknowledged) VALUES(?,?,?,?,?,?,?)`,
		e.AlertID, e.AlertName, string(e.AlertType), e.FiredAt.Unix(), e.Payload, delivered, ack)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	e.ID = id
	return nil
}

func (s *SQLite) GetAlertEvents(limit int) ([]model.AlertEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, alert_id, alert_name, alert_type, fired_at, payload, delivered_webhook, acknowledged FROM alert_events ORDER BY fired_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AlertEvent
	for rows.Next() {
		var e model.AlertEvent
		var del, ack int
		var fired int64
		if err := rows.Scan(&e.ID, &e.AlertID, &e.AlertName, &e.AlertType, &fired, &e.Payload, &del, &ack); err != nil {
			return nil, err
		}
		e.FiredAt = time.Unix(fired, 0)
		e.DeliveredWebhook = del != 0
		e.Acknowledged = ack != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLite) AckAlertEvent(id int64) error {
	_, err := s.db.Exec(`UPDATE alert_events SET acknowledged=1 WHERE id=?`, id)
	return err
}

// ---------------- raw access ----------------

func (s *SQLite) RawQueryRow(query string, args ...any) *sql.Row {
	return s.db.QueryRow(query, args...)
}

func (s *SQLite) RawQuery(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(query, args...)
}

// ---------------- runtime settings ----------------

// GetRuntimeSettings returns the persisted JSON snapshot written by
// SetRuntimeSettings, or (nil, nil) when no row exists yet.
func (s *SQLite) GetRuntimeSettings() ([]byte, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT settings_json FROM runtime_settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// SetRuntimeSettings upserts the single row. payload must be valid
// JSON; callers should validate before persisting.
func (s *SQLite) SetRuntimeSettings(payload []byte) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT INTO runtime_settings(id, settings_json, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET settings_json = excluded.settings_json, updated_at = excluded.updated_at
	`, string(payload), now)
	return err
}

// ---------- BYOK (Phase 1.5 reserved) ----------
//
// The four methods below are interface stubs. They return
// ErrNotImplemented until the BYOK feature ships, keeping the
// Store contract stable so router and admin code can reference
// the BYOK path without future refactoring. See docs/BYOK.md.

func (s *SQLite) CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error) {
	return 0, errNotImplemented
}

func (s *SQLite) ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error) {
	return nil, errNotImplemented
}

func (s *SQLite) GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error) {
	return nil, errNotImplemented
}

func (s *SQLite) DeleteBYOKChannel(ctx context.Context, id int64) error {
	return errNotImplemented
}
