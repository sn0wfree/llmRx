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

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

type SQLite struct {
	db      *sql.DB
	Secrets *secrets.Manager // nil ⇒ plaintext only (legacy mode); set by SetSecrets
}

// SetSecrets attaches a secrets manager used to encrypt new key rows
// and decrypt existing ones. When set, the store will:
//   - encrypt any plaintext Key field on Create
//   - decrypt KeyCiphertext on every read, falling back to the
//     legacy plaintext Key column for rows written before the
//     migration landed.
func (s *SQLite) SetSecrets(m *secrets.Manager) { s.Secrets = m }

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
	db, err := sql.Open("sqlite3", dsn+"?_journal=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
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
		`CREATE INDEX IF NOT EXISTS idx_logs_created ON logs(created_at DESC)`,
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
	if err := s.addColumnIfMissing("logs", "cached_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
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

func toUnix(t time.Time) int64  { return t.Unix() }
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
	if ch.CachedInputDiscount == 0 {
		ch.CachedInputDiscount = 0.1
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

func (s *SQLite) CreateLog(l *model.Log) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.Exec(
		`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.TokenID, l.ChannelID, l.KeyID, l.Model, l.PromptTokens, l.CompletionTokens, l.CachedTokens,
		l.RealCostUSD, l.BilledCostUSD, l.DurationMs, l.StatusCode, l.RouterPath, l.RequestIP, toUnix(l.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	l.ID = id
	return nil
}

func (s *SQLite) GetLogs(limit, offset int) ([]model.Log, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(`SELECT id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, created_at FROM logs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Log
	for rows.Next() {
		var l model.Log
		var created int64
		if err := rows.Scan(&l.ID, &l.TokenID, &l.ChannelID, &l.KeyID, &l.Model,
			&l.PromptTokens, &l.CompletionTokens, &l.CachedTokens, &l.RealCostUSD, &l.BilledCostUSD,
			&l.DurationMs, &l.StatusCode, &l.RouterPath, &l.RequestIP, &created); err != nil {
			return nil, err
		}
		l.CreatedAt = fromUnix(created)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *SQLite) CountLogs() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&n)
	return n, err
}

func (s *SQLite) DeleteLogsBefore(unixSec int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM logs WHERE created_at < ?`, unixSec)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLite) LogStats() (LogStats, error) {
	var st LogStats
	row := s.db.QueryRow(`SELECT
		COALESCE(SUM(prompt_tokens),0),
		COALESCE(SUM(completion_tokens),0),
		COALESCE(SUM(real_cost_usd),0),
		COALESCE(SUM(billed_cost_usd),0),
		COUNT(*),
		COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END),0)
	FROM logs`)
	if err := row.Scan(&st.PromptTokens, &st.CompletionTokens, &st.RealCostUSD, &st.BilledCostUSD, &st.Total, &st.Errors); err != nil {
		return st, err
	}
	return st, nil
}

func (s *SQLite) QueryLogs(f LogFilter) ([]model.Log, int64, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where, args := buildLogWhere(f)

	var total int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM logs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := "SELECT id, token_id, channel_id, key_id, model, prompt_tokens, completion_tokens, cached_tokens, real_cost_usd, billed_cost_usd, duration_ms, status_code, router_path, request_ip, created_at FROM logs " +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	qargs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := s.db.Query(q, qargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Log
	for rows.Next() {
		var l model.Log
		var created int64
		if err := rows.Scan(&l.ID, &l.TokenID, &l.ChannelID, &l.KeyID, &l.Model,
			&l.PromptTokens, &l.CompletionTokens, &l.CachedTokens, &l.RealCostUSD, &l.BilledCostUSD,
			&l.DurationMs, &l.StatusCode, &l.RouterPath, &l.RequestIP, &created); err != nil {
			return nil, 0, err
		}
		l.CreatedAt = fromUnix(created)
		out = append(out, l)
	}
	return nonNilLogs(out), total, rows.Err()
}

func nonNilLogs(xs []model.Log) []model.Log {
	if xs == nil {
		return []model.Log{}
	}
	return xs
}

// ---------------- Analytics ----------------

// buildLogWhere returns the WHERE clause + args for log analytics.
// CreatedFrom/To are unix seconds; the resulting filter is applied
// to logs.created_at (also unix seconds). Empty result means no filter.
func buildLogWhere(f LogFilter) (string, []any) {
	var (
		conds []string
		args  []any
	)
	if f.TokenID > 0 {
		conds = append(conds, "token_id = ?")
		args = append(args, f.TokenID)
	}
	if f.ChannelID > 0 {
		conds = append(conds, "channel_id = ?")
		args = append(args, f.ChannelID)
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
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func (s *SQLite) TimeSeries(f LogFilter, bucketSec int64) ([]SeriesPoint, error) {
	if bucketSec <= 0 {
		bucketSec = 3600
	}
	where, args := buildLogWhere(f)
	q := `SELECT
		(created_at / ?) * ? AS bucket,
		COUNT(*),
		COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(completion_tokens), 0),
		COALESCE(SUM(real_cost_usd), 0),
		COALESCE(SUM(billed_cost_usd), 0)
		FROM logs ` + where + ` GROUP BY bucket ORDER BY bucket`
	qargs := append([]any{bucketSec, bucketSec}, args...)
	rows, err := s.db.Query(q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesPoint
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.Bucket, &p.Requests, &p.Errors,
			&p.PromptTokens, &p.CompletionTokens,
			&p.RealCostUSD, &p.BilledCostUSD); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if out == nil {
		out = []SeriesPoint{}
	}
	return out, rows.Err()
}

func (s *SQLite) topByField(field string, f LogFilter, limit int) ([]NamedMetric, error) {
	if limit <= 0 {
		limit = 10
	}
	where, args := buildLogWhere(f)
	q := `SELECT ` + field + `, COUNT(*), COALESCE(SUM(prompt_tokens+completion_tokens),0), COALESCE(SUM(billed_cost_usd),0)
		FROM logs ` + where + ` GROUP BY ` + field + ` ORDER BY 2 DESC LIMIT ?`
	qargs := append(append([]any{}, args...), limit)
	rows, err := s.db.Query(q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NamedMetric
	for rows.Next() {
		var m NamedMetric
		var label sql.NullString
		if err := rows.Scan(&label, &m.Count, &m.Tokens, &m.Cost); err != nil {
			return nil, err
		}
		m.Label = label.String
		if m.Label == "" {
			m.Label = "(none)"
		}
		out = append(out, m)
	}
	if out == nil {
		out = []NamedMetric{}
	}
	return out, rows.Err()
}

func (s *SQLite) TopByModel(f LogFilter, limit int) ([]NamedMetric, error) {
	return s.topByField("model", f, limit)
}

func (s *SQLite) TopByChannel(f LogFilter, limit int) ([]NamedMetric, error) {
	return s.topByField("channel_id", f, limit)
}

func (s *SQLite) TopByToken(f LogFilter, limit int) ([]NamedMetric, error) {
	return s.topByField("token_id", f, limit)
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
