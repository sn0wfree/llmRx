package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrBudgetExceeded is returned by IncrementPlanSpend when the
	// requested addition would push the plan's used_usd past its
	// configured budget_usd. Callers should treat this as a billing
	// stop: roll back any paired IncrementTokenSpend and reject the
	// request as 402 Payment Required at the HTTP layer.
	ErrBudgetExceeded = errors.New("plan budget exceeded")
	// errNotImplemented is returned by Phase 1.5 reserved BYOK
	// store methods until the feature ships. Keeping it unexported
	// so callers don't depend on the wire format.
	errNotImplemented = errors.New("not implemented (Phase 1.5 reserved)")
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

// Secrets returns the attached secrets manager (nil when unset).
// Satisfies store.SecretsProvider.
func (s *SQLite) SecretsManager() *secrets.Manager { return s.Secrets }

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
		`CREATE TABLE IF NOT EXISTS providers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			display_name TEXT NOT NULL,
			protocol TEXT NOT NULL,
			base_url TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS token_combo_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			models TEXT NOT NULL DEFAULT '[]',
			mode TEXT NOT NULL DEFAULT 'load_balance',
			strategy TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			is_default INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(token_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_combo_token ON token_combo_models(token_id)`,
		`CREATE TABLE IF NOT EXISTS guardrails (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			hook TEXT NOT NULL DEFAULT 'input',
			on_failure TEXT NOT NULL DEFAULT 'deny',
			config TEXT NOT NULL DEFAULT '{}',
			priority INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_guardrails_enabled ON guardrails(enabled, priority)`,
		`CREATE TABLE IF NOT EXISTS guardrail_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_id INTEGER NOT NULL,
			rule_id INTEGER NOT NULL,
			rule_name TEXT NOT NULL DEFAULT '',
			rule_type TEXT NOT NULL DEFAULT '',
			hook TEXT NOT NULL DEFAULT '',
			verdict INTEGER NOT NULL DEFAULT 1,
			action TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			request_ip TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_gr_events_token ON guardrail_events(token_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS byok_channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'openai',
			key_ciphertext TEXT NOT NULL DEFAULT '',
			key_masked TEXT NOT NULL DEFAULT '',
			owner_ip TEXT NOT NULL DEFAULT '',
			owner_email TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL DEFAULT 1,
			last_used_at INTEGER NOT NULL DEFAULT 0,
			use_count INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_byok_owner_ip ON byok_channels(owner_ip, status)`,
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			url TEXT NOT NULL,
			auth_header TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_tools (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			server_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			input_schema_json TEXT NOT NULL,
			FOREIGN KEY (server_id) REFERENCES mcp_servers(id) ON DELETE CASCADE,
			UNIQUE(server_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_tools_server ON mcp_tools(server_id)`,
		`CREATE TABLE IF NOT EXISTS mcp_tool_pricing (
			mcp_tool_id INTEGER PRIMARY KEY,
			price_per_call_usd REAL NOT NULL DEFAULT 0,
			FOREIGN KEY (mcp_tool_id) REFERENCES mcp_tools(id) ON DELETE CASCADE
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
	if err := s.addColumnIfMissing("users", "permissions", "TEXT NOT NULL DEFAULT ''"); err != nil {
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
	if err := s.addColumnIfMissing("keys", "key_ciphertext", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("tokens", "key_ciphertext", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("alerts", "disabled_reason", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("token_combo_models", "is_default", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("mcp_servers", "transport", "TEXT NOT NULL DEFAULT 'http'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("mcp_servers", "command", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("mcp_servers", "oauth_config_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("mcp_servers", "token_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	s.migrateAutoCombos()
	s.migrateDefaultFlag()
	return nil
}

// migrateDefaultFlag sets is_default=1 for any existing combo whose
// Name == "auto", so the data created by Phase 1 (token-form-driven)
// is consistent with the new explicit default-flag semantics. Best
// effort, errors are logged but never block startup.
func (s *SQLite) migrateDefaultFlag() {
	res, err := s.db.Exec(`UPDATE token_combo_models SET is_default = 1 WHERE name = 'auto' AND is_default = 0`)
	if err != nil {
		logging.Debug("migrate default flag: skipped", logging.F("err", err.Error()))
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		logging.Info("migrate default flag", logging.F("promoted", n))
	}
}

// migrateAutoCombos is a best-effort one-time data migration that
// creates a default "auto" combo for any token that has a non-empty
// models whitelist but no combo at all. Tokens with an empty
// whitelist (unrestricted) are skipped — an "auto" combo would have
// no well-defined model pool. Errors are logged but never block
// startup (the table may not exist yet on a fresh DB, or the
// models_whitelist column may be missing on old schemas during
// upgrade tests).
func (s *SQLite) migrateAutoCombos() {
	toks, err := s.GetTokens()
	if err != nil {
		logging.Debug("migrate auto combos: skipped (cannot list tokens)",
			logging.F("err", err.Error()),
		)
		return
	}
	now := time.Now()
	created := 0
	for i := range toks {
		t := &toks[i]
		if len(t.ModelsWhitelist) == 0 {
			continue
		}
		existing, err := s.GetComboModels(t.ID)
		if err != nil {
			logging.Debug("migrate auto combos: skip token",
				logging.F("token_id", t.ID),
				logging.F("err", err.Error()),
			)
			continue
		}
		if len(existing) > 0 {
			continue
		}
		c := &model.TokenComboModel{
			TokenID:   t.ID,
			Name:      "auto",
			Models:    append([]string(nil), t.ModelsWhitelist...),
			Mode:      model.ComboModeLoadBalance,
			Strategy:  model.StrategyBalanced,
			Enabled:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.CreateComboModel(c); err != nil {
			logging.Debug("migrate auto combos: create failed",
				logging.F("token_id", t.ID),
				logging.F("err", err.Error()),
			)
			continue
		}
		created++
	}
	if created > 0 {
		logging.Info("migrate auto combos", logging.F("created", created))
	}
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

func (s *SQLite) GetDrainedChannels() ([]DrainedChannel, error) {
	rows, err := s.db.Query(`SELECT c.id, c.name FROM channels c WHERE c.status = 1 AND NOT EXISTS (SELECT 1 FROM keys k WHERE k.channel_id = c.id AND k.status = 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DrainedChannel
	for rows.Next() {
		var d DrainedChannel
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
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
	badKeyIDs := make([]int64, 0)
	for _, rr := range raws {
		if s.Secrets != nil {
			if rr.cipher != "" {
				pt, derr := s.Secrets.Decrypt(rr.cipher)
				if derr != nil {
					// Master-key mismatch or tampered ciphertext:
					// mark this key as disabled and skip — the row
					// stays so the admin UI can prompt the user
					// to re-enter the API key. Returning an error
					// here would take the whole gateway down and
					// block the recovery path (no admin UI access).
					logging.Warn("key decrypt failed, marking disabled",
					logging.F("key_id", rr.k.ID),
					logging.F("error", derr.Error()),
				)
					rr.k.Status = model.KeyDisabled
					rr.k.Key = ""
					badKeyIDs = append(badKeyIDs, rr.k.ID)
					out = append(out, rr.k)
					continue
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
			// No secrets manager configured (legacy mode). If a row
			// carries ciphertext we cannot decode it, so disable
			// that key in-place rather than failing the whole load.
			if rr.cipher != "" {
				logging.Warn("key has ciphertext but no secrets manager, marking disabled",
					logging.F("key_id", rr.k.ID),
				)
				rr.k.Status = model.KeyDisabled
				rr.k.Key = ""
				badKeyIDs = append(badKeyIDs, rr.k.ID)
				out = append(out, rr.k)
				continue
			}
			rr.k.Key = rr.plain
		}
		out = append(out, rr.k)
	}
	if len(badKeyIDs) > 0 {
		logging.Warn("keys failed decrypt, action required",
					logging.F("bad_count", len(badKeyIDs)),
				)
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

// WipeKeys clears all key material in the keys table. The row
// shells (id, channel_id, key_masked, status, timestamps) are
// preserved so the admin UI can show "0 active keys" instead of
// orphan channels. Status is left untouched — callers that want
// to invalidate should set KeyDisabled explicitly. Used by the
// `-wipe-keys` recovery command after a master-key rotation
// renders existing ciphertext undecryptable.
func (s *SQLite) WipeKeys() (int64, error) {
	res, err := s.db.Exec(`UPDATE keys SET key='', key_ciphertext='' WHERE key != '' OR key_ciphertext != ''`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
	tn, err := s.reencryptAllTokens(s.Secrets, m)
	if err != nil {
		return n, err
	}
	s.Secrets = m
	logging.Info("secrets rotated master key",
					logging.F("channel_keys", n),
					logging.F("tokens", tn),
				)
	return n + tn, nil
}

// reencryptAllTokens re-encrypts every tokens.key_ciphertext row
// from oldMgr to newMgr. Returns the count of tokens rotated.
// Mirror of ReencryptAllKeys but on the tokens table.
func (s *SQLite) reencryptAllTokens(oldMgr, newMgr *secrets.Manager) (int, error) {
	rows, err := s.db.Query(`SELECT id, key_ciphertext FROM tokens WHERE key_ciphertext != ''`)
	if err != nil {
		return 0, err
	}
	type tr struct {
		id     int64
		cipher string
	}
	var toks []tr
	for rows.Next() {
		var r tr
		if err := rows.Scan(&r.id, &r.cipher); err != nil {
			rows.Close()
			return 0, err
		}
		toks = append(toks, r)
	}
	rows.Close()
	for _, r := range toks {
		pt, err := oldMgr.Decrypt(r.cipher)
		if err != nil {
			return 0, fmt.Errorf("decrypt token %d: %w", r.id, err)
		}
		newCT, err := newMgr.Encrypt(pt)
		if err != nil {
			return 0, fmt.Errorf("encrypt token %d: %w", r.id, err)
		}
		if _, err := s.db.Exec(`UPDATE tokens SET key_ciphertext=? WHERE id=?`, newCT, r.id); err != nil {
			return 0, fmt.Errorf("update token %d: %w", r.id, err)
		}
	}
	return len(toks), nil
}

// ---------------- Tokens ----------------

func (s *SQLite) GetToken(key string) (*model.Token, error) {
	// With ciphertext-only mode, the SQL `key = ?` lookup can't
	// match an encrypted bearer directly. Fall back to scanning
	// all rows and comparing in plaintext — production callers
	// go through tokencache (in-memory), so this path is for
	// tests / recovery tools only.
	rows, err := s.db.Query(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at FROM tokens`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTokenRow(s, rows)
		if err != nil {
			return nil, err
		}
		if t.Key == key {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

func (s *SQLite) GetTokenByID(id int64) (*model.Token, error) {
	row := s.db.QueryRow(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at FROM tokens WHERE id = ?`, id)
	return scanTokenRow(s, row)
}

func (s *SQLite) GetTokens() ([]model.Token, error) {
	rows, err := s.db.Query(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Token
	for rows.Next() {
		t, err := scanTokenRow(s, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *SQLite) CreateToken(t *model.Token) error {
	t.CreatedAt = time.Now().UTC()
	plain := t.Key
	if plain == "" {
		return errors.New("token key is empty")
	}
	cipher := ""
	storedPlain := plain
	if s.Secrets != nil {
		ct, err := s.Secrets.Encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("encrypt token: %w", err)
		}
		cipher = ct
		// key column has a UNIQUE constraint but in encrypted mode
		// every row would store the empty string, violating it after
		// the second insert. Insert a sentinel placeholder now and
		// swap it for the row id once SQLite has generated one —
		// the placeholder is unique within the table so the UNIQUE
		// check passes, and the post-insert UPDATE uses the same id
		// to produce a stable, collision-free value (e.g.
		// "__enc_3" for token id 3). Legacy plaintext rows are
		// untouched and continue to dedup on the real key.
		storedPlain = "__enc_pending__"
	}
	res, err := s.db.Exec(
		`INSERT INTO tokens(plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.PlanID, storedPlain, cipher, t.Name, int(t.Status), t.RPM, t.TPM, t.UsedUSD,
		encodeStrings(t.ModelsWhitelist), encodeStrings(t.IPWhitelist),
		toUnix(t.ExpiresAt), toUnix(t.LastUsedAt), toUnix(t.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	t.ID = id
	if s.Secrets != nil {
		// Replace the sentinel with "__enc_<id>" so it stays unique
		// and gives a deterministic lookup if anyone wants to identify
		// the row from its key column. scanTokenRow never reads the
		// key column when ciphertext is present, so this is purely a
		// UNIQUE-constraint workaround.
		_, _ = s.db.Exec(`UPDATE tokens SET key=? WHERE id=?`, fmt.Sprintf("__enc_%d", id), id)
	}
	return nil
}

func (s *SQLite) UpdateToken(t *model.Token) error {
	t.LastUsedAt = time.Now().UTC()
	plain := t.Key
	cipher := ""
	storedPlain := plain
	if plain != "" && s.Secrets != nil {
		ct, err := s.Secrets.Encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("encrypt token: %w", err)
		}
		cipher = ct
		// Same UNIQUE-constraint workaround as CreateToken: store
		// a stable placeholder derived from the row id (which is
		// already known for an UPDATE) rather than the empty
		// string. Plaintext-mode updates leave storedPlain == plain
		// unchanged and continue to dedup on the real key.
		storedPlain = fmt.Sprintf("__enc_%d", t.ID)
	}
	res, err := s.db.Exec(
		`UPDATE tokens SET plan_id=?, key=?, key_ciphertext=?, name=?, status=?, rpm=?, tpm=?, used_usd=?, models_whitelist=?, ip_whitelist=?, expires_at=? WHERE id=?`,
		t.PlanID, storedPlain, cipher, t.Name, int(t.Status), t.RPM, t.TPM, t.UsedUSD,
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

// IncrementPlanSpend atomically adds amount to a plan's used_usd,
// but only if the plan has either no budget (budget_usd = 0,
// meaning unlimited) or enough remaining headroom. The check and
// the update happen in a single SQL statement so concurrent
// requests cannot race past the budget. Returns ErrBudgetExceeded
// when the addition would overshoot the configured limit, and
// ErrNotFound when the plan row does not exist.
//
// Callers that pair this with IncrementTokenSpend must roll the
// token spend back on ErrBudgetExceeded to keep the two ledgers in
// agreement.
func (s *SQLite) IncrementPlanSpend(planID int64, amount float64) error {
	if amount == 0 || planID == 0 {
		return nil
	}
	res, err := s.db.Exec(
		`UPDATE plans
		   SET used_usd = used_usd + ?
		 WHERE id = ?
		   AND (budget_usd = 0 OR used_usd + ? <= budget_usd)`,
		amount, planID, amount,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Distinguish "no such plan" from "budget would overflow".
		var exists int64
		if err := s.db.QueryRow(`SELECT 1 FROM plans WHERE id = ?`, planID).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		return ErrBudgetExceeded
	}
	return nil
}

// RecordRequestSpend credits both the token and plan ledgers
// inside a single SQL transaction. When the plan leg would
// exceed budget_usd the transaction is rolled back so the
// token ledger is automatically reverted — the caller never
// has to compensate manually.
//
// planID == 0 skips the plan leg (no plan bound to the token),
// in which case only the token ledger is touched and no
// ErrBudgetExceeded can be raised.
func (s *SQLite) RecordRequestSpend(tokenID, planID int64, amount float64) error {
	if amount == 0 {
		return nil
	}
	if tokenID == 0 {
		// No token leg to credit; nothing to do.
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// tx.Rollback is safe to call after a successful Commit; the
	// deferred fallback ensures cleanup on any error path.
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`UPDATE tokens SET used_usd = used_usd + ? WHERE id = ?`,
		amount, tokenID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	if planID > 0 {
		res, err = tx.Exec(
			`UPDATE plans
			   SET used_usd = used_usd + ?
			 WHERE id = ?
			   AND (budget_usd = 0 OR used_usd + ? <= budget_usd)`,
			amount, planID, amount,
		)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Distinguish "no such plan" from "budget would overflow".
			var exists int64
			if qerr := tx.QueryRow(`SELECT 1 FROM plans WHERE id = ?`, planID).Scan(&exists); qerr != nil {
				if qerr == sql.ErrNoRows {
					return ErrNotFound
				}
				return qerr
			}
			// Budget would overflow — deferred Rollback will
			// revert the token ledger.
			return ErrBudgetExceeded
		}
	}

	return tx.Commit()
}

// MarkTokenExpired flips a token's status to TokenExpired so the
// in-memory cache will skip it on the next reload and any stale
// request still holding the bearer will be rejected by the
// Expiry check in middleware. Idempotent.
func (s *SQLite) MarkTokenExpired(tokenID int64) error {
	_, err := s.db.Exec(
		`UPDATE tokens SET status = ? WHERE id = ? AND status = ?`,
		int(model.TokenExpired), tokenID, int(model.TokenActive),
	)
	return err
}

func (s *SQLite) DeleteToken(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	return err
}

func scanTokenRow(s *SQLite, r interface {
	Scan(dest ...any) error
}) (*model.Token, error) {
	var t model.Token
	var status int
	var mwJSON, ipwJSON string
	var expires, lastUsed, created int64
	var plain, cipher string
	if err := r.Scan(&t.ID, &t.PlanID, &plain, &cipher, &t.Name, &status, &t.RPM, &t.TPM,
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

	// Resolve plaintext key from ciphertext when a manager is
	// attached. If the row has only legacy plaintext, best-effort
	// upgrade to ciphertext form so subsequent reads go through
	// the manager.
	switch {
	case s.Secrets != nil && cipher != "":
		pt, derr := s.Secrets.Decrypt(cipher)
		if derr != nil {
			// Master-key mismatch or tampered ciphertext: leave
			// the token empty and let cache reload skip it.
			logging.Warn("token decrypt failed",
					logging.F("token_id", t.ID),
					logging.F("error", derr.Error()),
				)
			t.Key = ""
		} else {
			t.Key = string(pt)
		}
	case s.Secrets != nil && plain != "":
		t.Key = plain
		if ct, eerr := s.Secrets.Encrypt([]byte(plain)); eerr == nil {
			_, _ = s.db.Exec(`UPDATE tokens SET key='', key_ciphertext=? WHERE id=?`, ct, t.ID)
		}
	default:
		t.Key = plain
	}
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

// ---------------- alerts ----------------

func (s *SQLite) GetAlerts() ([]model.Alert, error) {
	rows, err := s.db.Query(`SELECT id, name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, disabled_reason, created_at FROM alerts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Alert
	for rows.Next() {
		var a model.Alert
		var enabled int
		var created int64
		if err := rows.Scan(&a.ID, &a.Name, &a.Type, &a.Threshold, &a.WindowSec, &a.CooldownSec, &a.WebhookURL, &enabled, &a.LastFiredAt, &a.DisabledReason, &created); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		a.CreatedAt = fromUnix(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLite) GetAlert(id int64) (*model.Alert, error) {
	row := s.db.QueryRow(`SELECT id, name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, disabled_reason, created_at FROM alerts WHERE id=?`, id)
	var a model.Alert
	var enabled int
	var created int64
	if err := row.Scan(&a.ID, &a.Name, &a.Type, &a.Threshold, &a.WindowSec, &a.CooldownSec, &a.WebhookURL, &enabled, &a.LastFiredAt, &a.DisabledReason, &created); err != nil {
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

// DisableAlert flips the rule's enabled flag to 0 and records
// the reason so the admin UI / /alerts listing can surface why
// the rule was auto-disabled. Idempotent.
func (s *SQLite) DisableAlert(id int64, reason string) error {
	res, err := s.db.Exec(
		`UPDATE alerts SET enabled=0, disabled_reason=? WHERE id=?`,
		reason, id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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

func (s *SQLite) RawDB() *sql.DB { return s.db }

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

// ---------- BYOK (Bring Your Own Key) ----------
//
// Consumer-supplied upstream API keys. When a request arrives with a
// bearer token that isn't in our cache AND the token looks like an
// upstream-provider key (prefix match), the BYOK hook verifies the
// key against the upstream, stores the encrypted ciphertext, and
// proceeds with the request using the consumer's key.

func (s *SQLite) CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error) {
	if ch.Provider == "" {
		return 0, errors.New("provider is required")
	}
	if ch.KeyCiphertext == "" && ch.KeyMasked == "" {
		return 0, errors.New("key is required")
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO byok_channels
		  (provider, key_ciphertext, key_masked, owner_ip, owner_email,
		   status, last_used_at, use_count, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.Provider, ch.KeyCiphertext, ch.KeyMasked, ch.OwnerIP, ch.OwnerEmail,
		ch.Status, ch.LastUsedAt.Unix(), ch.UseCount, ch.ExpiresAt.Unix(), now)
	if err != nil {
		return 0, fmt.Errorf("insert byok channel: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *SQLite) ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, provider, key_ciphertext, key_masked, owner_ip, owner_email,
		       status, last_used_at, use_count, expires_at, created_at
		FROM byok_channels
		ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query byok channels: %w", err)
	}
	defer rows.Close()
	var out []*model.BYOKChannel
	for rows.Next() {
		c, err := scanBYOKRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLite) GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, provider, key_ciphertext, key_masked, owner_ip, owner_email,
		       status, last_used_at, use_count, expires_at, created_at
		FROM byok_channels WHERE id = ?`, id)
	c, err := scanBYOKRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

// GetBYOKChannelByIP looks up an active BYOK row by client IP. Used
// by the UnknownTokenHook to find a previously registered consumer key.
func (s *SQLite) GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, provider, key_ciphertext, key_masked, owner_ip, owner_email,
		       status, last_used_at, use_count, expires_at, created_at
		FROM byok_channels
		WHERE owner_ip = ? AND status = 1
		ORDER BY id DESC LIMIT 1`, ownerIP)
	c, err := scanBYOKRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

// TouchBYOKChannel increments use_count and updates last_used_at.
func (s *SQLite) TouchBYOKChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE byok_channels
		   SET use_count = use_count + 1, last_used_at = ?
		 WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (s *SQLite) DeleteBYOKChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM byok_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete byok channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanBYOKRow scans one row into a BYOKChannel. Accepts any type
// implementing Scan (both *sql.Row and *sql.Rows).
func scanBYOKRow(r interface {
	Scan(dest ...any) error
}) (*model.BYOKChannel, error) {
	var (
		c         model.BYOKChannel
		lastUsed  int64
		expiresAt int64
		createdAt int64
	)
	err := r.Scan(&c.ID, &c.Provider, &c.KeyCiphertext, &c.KeyMasked,
		&c.OwnerIP, &c.OwnerEmail, &c.Status, &lastUsed, &c.UseCount,
		&expiresAt, &createdAt)
	if err != nil {
		return nil, err
	}
	c.LastUsedAt = time.Unix(lastUsed, 0)
	c.ExpiresAt = time.Unix(expiresAt, 0)
	c.CreatedAt = time.Unix(createdAt, 0)
	return &c, nil
}

// ---------- ProviderDefs ----------

func (s *SQLite) GetProviderDefs() ([]model.ProviderDef, error) {
	rows, err := s.db.Query(`SELECT id, name, display_name, protocol, base_url, created_at, updated_at FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProviderDef
	for rows.Next() {
		var p model.ProviderDef
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &p.DisplayName, &p.Protocol, &p.BaseURL, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(created, 0)
		p.UpdatedAt = time.Unix(updated, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLite) CreateProviderDef(p *model.ProviderDef) error {
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO providers (name, display_name, protocol, base_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.DisplayName, p.Protocol, p.BaseURL, now, now)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = time.Unix(now, 0)
	p.UpdatedAt = time.Unix(now, 0)
	return nil
}

func (s *SQLite) DeleteProviderDef(id int64) error {
	_, err := s.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	return err
}

// ---------- ComboModels ----------

func (s *SQLite) scanComboRow(r interface{ Scan(dest ...any) error }) (*model.TokenComboModel, error) {
	var c model.TokenComboModel
	var modelsJSON, mode, strategy string
	var enabled, isDefault int
	var created, updated int64
	if err := r.Scan(&c.ID, &c.TokenID, &c.Name, &modelsJSON, &mode, &strategy, &enabled, &isDefault, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Models = decodeStrings(modelsJSON)
	c.Mode = model.ComboMode(mode)
	c.Strategy = model.CostStrategy(strategy)
	c.Enabled = enabled == 1
	c.IsDefault = isDefault == 1
	c.CreatedAt = fromUnix(created)
	c.UpdatedAt = fromUnix(updated)
	return &c, nil
}

func (s *SQLite) GetComboModels(tokenID int64) ([]model.TokenComboModel, error) {
	rows, err := s.db.Query(`SELECT id, token_id, name, models, mode, strategy, enabled, is_default, created_at, updated_at FROM token_combo_models WHERE token_id = ? ORDER BY id`, tokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TokenComboModel
	for rows.Next() {
		c, err := s.scanComboRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *SQLite) GetComboModel(id int64) (*model.TokenComboModel, error) {
	row := s.db.QueryRow(`SELECT id, token_id, name, models, mode, strategy, enabled, is_default, created_at, updated_at FROM token_combo_models WHERE id = ?`, id)
	return s.scanComboRow(row)
}

func (s *SQLite) GetAllComboModels() ([]model.TokenComboModel, error) {
	rows, err := s.db.Query(`SELECT id, token_id, name, models, mode, strategy, enabled, is_default, created_at, updated_at FROM token_combo_models WHERE enabled = 1 ORDER BY token_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TokenComboModel
	for rows.Next() {
		c, err := s.scanComboRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListAllComboModels returns every combo (enabled or disabled) across
// all tokens. Used by the admin UI's model-sets page where the
// operator wants to see disabled entries too. Routing-time lookup
// uses GetAllComboModels (enabled-only).
func (s *SQLite) ListAllComboModels() ([]model.TokenComboModel, error) {
	rows, err := s.db.Query(`SELECT id, token_id, name, models, mode, strategy, enabled, is_default, created_at, updated_at FROM token_combo_models ORDER BY token_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TokenComboModel
	for rows.Next() {
		c, err := s.scanComboRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *SQLite) CreateComboModel(c *model.TokenComboModel) error {
	if err := s.validateCombo(c); err != nil {
		return err
	}
	if c.IsDefault {
		if err := s.clearDefaultFlag(c.TokenID, 0); err != nil {
			return fmt.Errorf("clear default flag: %w", err)
		}
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`INSERT INTO token_combo_models (token_id, name, models, mode, strategy, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.TokenID, c.Name, encodeStrings(c.Models), string(c.Mode), string(c.Strategy), boolToInt(c.Enabled), boolToInt(c.IsDefault), now, now,
	)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.CreatedAt = fromUnix(now)
	c.UpdatedAt = fromUnix(now)
	return nil
}

func (s *SQLite) validateCombo(c *model.TokenComboModel) error {
	// name format: ^[a-zA-Z0-9_-]{1,64}$
	if !comboNameRe.MatchString(c.Name) {
		return fmt.Errorf("combo name %q must match ^[a-zA-Z0-9_-]{1,64}$", c.Name)
	}
	// models: 1..100 items, each ^[a-zA-Z0-9._-]{1,128}$
	if len(c.Models) == 0 {
		return errors.New("combo models list must not be empty")
	}
	if len(c.Models) > 100 {
		return fmt.Errorf("combo models list has %d items, max is 100", len(c.Models))
	}
	for _, m := range c.Models {
		if !comboModelRe.MatchString(m) {
			return fmt.Errorf("combo model name %q must match ^[a-zA-Z0-9._-]{1,128}$", m)
		}
	}
	// mode must be valid
	switch c.Mode {
	case model.ComboModeLoadBalance, model.ComboModeSerial:
		// ok
	default:
		return fmt.Errorf("combo mode %q must be load_balance or serial", c.Mode)
	}
	// strategy must be valid
	switch c.Strategy {
	case "", model.StrategyCheapest, model.StrategyFastest, model.StrategyBalanced:
		// ok
	default:
		return fmt.Errorf("combo strategy %q must be empty, cheapest, fastest, or balanced", c.Strategy)
	}
	// check name does not collide with any channel.Models real model name
	chs, err := s.GetChannels()
	if err != nil {
		return fmt.Errorf("check combo name conflict: %w", err)
	}
	for i := range chs {
		for _, m := range chs[i].Models {
			if m == c.Name {
				return fmt.Errorf("combo name %q conflicts with real model name in channel %q", c.Name, chs[i].Name)
			}
		}
	}
	return nil
}

var (
	comboNameRe  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	comboModelRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
)

func (s *SQLite) UpdateComboModel(c *model.TokenComboModel) error {
	if c.IsDefault {
		if err := s.clearDefaultFlag(c.TokenID, c.ID); err != nil {
			return fmt.Errorf("clear default flag: %w", err)
		}
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE token_combo_models SET name=?, models=?, mode=?, strategy=?, enabled=?, is_default=?, updated_at=? WHERE id=?`,
		c.Name, encodeStrings(c.Models), string(c.Mode), string(c.Strategy), boolToInt(c.Enabled), boolToInt(c.IsDefault), now, c.ID,
	)
	if err != nil {
		return err
	}
	c.UpdatedAt = fromUnix(now)
	return nil
}

func (s *SQLite) DeleteComboModel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM token_combo_models WHERE id = ?`, id)
	return err
}

// SetDefaultModelSet promotes one combo to the token's "default"
// set and demotes any other. The "auto" routing alias resolves to
// the default set. If comboID is 0 the call is a no-op (used when
// the operator wants to clear the default).
func (s *SQLite) SetDefaultModelSet(tokenID, comboID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE token_combo_models SET is_default = 0, updated_at = ? WHERE token_id = ? AND is_default = 1`,
		time.Now().Unix(), tokenID); err != nil {
		return err
	}
	if comboID != 0 {
		if _, err := tx.Exec(`UPDATE token_combo_models SET is_default = 1, updated_at = ? WHERE id = ? AND token_id = ?`,
			time.Now().Unix(), comboID, tokenID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// clearDefaultFlag unsets is_default for every combo of tokenID
// except excludeID. Used inside Create/Update to keep the per-token
// default-set invariant at most one row has is_default=1. excludeID
// 0 means "no exclusion".
func (s *SQLite) clearDefaultFlag(tokenID, excludeID int64) error {
	if excludeID == 0 {
		_, err := s.db.Exec(`UPDATE token_combo_models SET is_default = 0, updated_at = ? WHERE token_id = ?`,
			time.Now().Unix(), tokenID)
		return err
	}
	_, err := s.db.Exec(`UPDATE token_combo_models SET is_default = 0, updated_at = ? WHERE token_id = ? AND id != ?`,
		time.Now().Unix(), tokenID, excludeID)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- Guardrails ----------

func (s *SQLite) scanGuardrailRow(r interface{ Scan(dest ...any) error }) (*model.GuardrailRule, error) {
	var g model.GuardrailRule
	var hook, onFailure, config string
	var enabled int
	var created, updated int64
	if err := r.Scan(&g.ID, &g.Name, &g.Description, &g.Type, &hook, &onFailure, &config, &g.Priority, &enabled, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.Hook = model.GuardrailHook(hook)
	g.OnFailure = model.GuardrailAction(onFailure)
	g.Config = config
	g.Enabled = enabled == 1
	g.CreatedAt = fromUnix(created)
	g.UpdatedAt = fromUnix(updated)
	return &g, nil
}

func (s *SQLite) GetEnabledGuardrailRules() ([]model.GuardrailRule, error) {
	rows, err := s.db.Query(`SELECT id, name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at FROM guardrails WHERE enabled = 1 ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GuardrailRule
	for rows.Next() {
		g, err := s.scanGuardrailRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *SQLite) GetGuardrailRules() ([]model.GuardrailRule, error) {
	rows, err := s.db.Query(`SELECT id, name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at FROM guardrails ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GuardrailRule
	for rows.Next() {
		g, err := s.scanGuardrailRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *SQLite) GetGuardrailRule(id int64) (*model.GuardrailRule, error) {
	row := s.db.QueryRow(`SELECT id, name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at FROM guardrails WHERE id = ?`, id)
	return s.scanGuardrailRow(row)
}

func (s *SQLite) CreateGuardrailRule(r *model.GuardrailRule) error {
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`INSERT INTO guardrails (name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Description, string(r.Type), string(r.Hook), string(r.OnFailure), r.Config, r.Priority, boolToInt(r.Enabled), now, now,
	)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	r.CreatedAt = fromUnix(now)
	r.UpdatedAt = fromUnix(now)
	return nil
}

func (s *SQLite) UpdateGuardrailRule(r *model.GuardrailRule) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE guardrails SET name=?, description=?, type=?, hook=?, on_failure=?, config=?, priority=?, enabled=?, updated_at=? WHERE id=?`,
		r.Name, r.Description, string(r.Type), string(r.Hook), string(r.OnFailure), r.Config, r.Priority, boolToInt(r.Enabled), now, r.ID,
	)
	if err != nil {
		return err
	}
	r.UpdatedAt = fromUnix(now)
	return nil
}

func (s *SQLite) DeleteGuardrailRule(id int64) error {
	_, err := s.db.Exec(`DELETE FROM guardrails WHERE id = ?`, id)
	return err
}

func (s *SQLite) CreateGuardrailEvent(e *model.GuardrailEvent) error {
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`INSERT INTO guardrail_events (token_id, rule_id, rule_name, rule_type, hook, verdict, action, detail, request_ip, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TokenID, e.RuleID, e.RuleName, e.RuleType, e.Hook, boolToInt(e.Verdict), e.Action, e.Detail, e.RequestIP, now,
	)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	e.CreatedAt = fromUnix(now)
	return nil
}

func (s *SQLite) GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, token_id, rule_id, rule_name, rule_type, hook, verdict, action, detail, request_ip, created_at FROM guardrail_events WHERE token_id = ? ORDER BY created_at DESC LIMIT ?`, tokenID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GuardrailEvent
	for rows.Next() {
		var e model.GuardrailEvent
		var verdict int
		var created int64
		if err := rows.Scan(&e.ID, &e.TokenID, &e.RuleID, &e.RuleName, &e.RuleType, &e.Hook, &verdict, &e.Action, &e.Detail, &e.RequestIP, &created); err != nil {
			return nil, err
		}
		e.Verdict = verdict == 1
		e.CreatedAt = fromUnix(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLite) GetMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at FROM mcp_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServer
	for rows.Next() {
		var srv MCPServer
		var created int64
		var enabled int
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.URL, &srv.AuthHdr, &srv.Transport, &srv.Command, &srv.OAuthConfigJSON, &srv.TokenJSON, &enabled, &created); err != nil {
			return nil, err
		}
		srv.Enabled = enabled == 1
		srv.CreatedAt = fromUnix(created)
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *SQLite) GetMCPServer(ctx context.Context, id int64) (*MCPServer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at FROM mcp_servers WHERE id = ?`, id)
	var srv MCPServer
	var created int64
	var enabled int
	if err := row.Scan(&srv.ID, &srv.Name, &srv.URL, &srv.AuthHdr, &srv.Transport, &srv.Command, &srv.OAuthConfigJSON, &srv.TokenJSON, &enabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	srv.Enabled = enabled == 1
	srv.CreatedAt = fromUnix(created)
	return &srv, nil
}

func (s *SQLite) CreateMCPServer(ctx context.Context, srv *MCPServer) error {
	now := time.Now().Unix()
	enabled := 0
	if srv.Enabled {
		enabled = 1
	}
	if srv.Transport == "" {
		srv.Transport = "http"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.Name, srv.URL, srv.AuthHdr, srv.Transport, srv.Command, srv.OAuthConfigJSON, srv.TokenJSON, enabled, now,
	)
	if err != nil {
		return err
	}
	srv.ID, _ = res.LastInsertId()
	srv.CreatedAt = fromUnix(now)
	return nil
}

func (s *SQLite) UpdateMCPServer(ctx context.Context, srv *MCPServer) error {
	enabled := 0
	if srv.Enabled {
		enabled = 1
	}
	if srv.Transport == "" {
		srv.Transport = "http"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE mcp_servers SET name=?, url=?, auth_header=?, transport=?, command=?, oauth_config_json=?, token_json=?, enabled=? WHERE id=?`,
		srv.Name, srv.URL, srv.AuthHdr, srv.Transport, srv.Command, srv.OAuthConfigJSON, srv.TokenJSON, enabled, srv.ID,
	)
	return err
}

func (s *SQLite) DeleteMCPServer(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	return err
}

func (s *SQLite) GetMCPTools(ctx context.Context, serverID int64) ([]MCPTool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, name, description, input_schema_json FROM mcp_tools WHERE server_id = ? ORDER BY name`,
		serverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPTool
	for rows.Next() {
		var t MCPTool
		if err := rows.Scan(&t.ID, &t.ServerID, &t.Name, &t.Description, &t.InputSchemaJSON); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLite) SetMCPTools(ctx context.Context, serverID int64, tools []MCPTool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mcp_tools WHERE server_id = ?`, serverID); err != nil {
		return err
	}
	for _, t := range tools {
		t.ServerID = serverID
		_, err := tx.Exec(
			`INSERT INTO mcp_tools (server_id, name, description, input_schema_json) VALUES (?, ?, ?, ?)`,
			serverID, t.Name, t.Description, t.InputSchemaJSON,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) GetMCPToolPricing(ctx context.Context, toolID int64) (*MCPToolPricing, error) {
	row := s.db.QueryRowContext(ctx, `SELECT mcp_tool_id, price_per_call_usd FROM mcp_tool_pricing WHERE mcp_tool_id = ?`, toolID)
	var p MCPToolPricing
	if err := row.Scan(&p.MCPToolID, &p.PricePerCallUSD); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &MCPToolPricing{MCPToolID: toolID}, nil
		}
		return nil, err
	}
	return &p, nil
}

func (s *SQLite) SetMCPToolPricing(ctx context.Context, p *MCPToolPricing) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_tool_pricing (mcp_tool_id, price_per_call_usd) VALUES (?, ?)
		 ON CONFLICT(mcp_tool_id) DO UPDATE SET price_per_call_usd = excluded.price_per_call_usd`,
		p.MCPToolID, p.PricePerCallUSD,
	)
	return err
}

func (s *SQLite) GetAllMCPTools(ctx context.Context) ([]MCPTool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.server_id, t.name, t.description, t.input_schema_json
		 FROM mcp_tools t
		 JOIN mcp_servers s ON s.id = t.server_id
		 WHERE s.enabled = 1
		 ORDER BY t.name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPTool
	for rows.Next() {
		var t MCPTool
		if err := rows.Scan(&t.ID, &t.ServerID, &t.Name, &t.Description, &t.InputSchemaJSON); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLite) GetEnabledMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at FROM mcp_servers WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServer
	for rows.Next() {
		var srv MCPServer
		var created int64
		var enabled int
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.URL, &srv.AuthHdr, &srv.Transport, &srv.Command, &srv.OAuthConfigJSON, &srv.TokenJSON, &enabled, &created); err != nil {
			return nil, err
		}
		srv.Enabled = true
		srv.CreatedAt = fromUnix(created)
		out = append(out, srv)
	}
	return out, rows.Err()
}
