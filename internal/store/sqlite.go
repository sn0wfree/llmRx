package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"database/sql"
	"encoding/json"
	_ "github.com/mattn/go-sqlite3"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"path/filepath"
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

// dbStore implements every store.Store method against *sql.DB using
// a Dialect. SQLite and Postgres embed it; backend-specific code is
// limited to Open*/applyPragmas/migrate hooks.
type dbStore struct {
	db      *sql.DB
	d       dialect.Dialect
	secrets *secrets.Manager // nil ⇒ plaintext only (legacy mode); set by SetSecrets
}

type SQLite struct {
	dbStore
}

// SetSecrets attaches a secrets manager used to encrypt new key rows
// and decrypt existing ones. When set, the store will:
//   - encrypt any plaintext Key field on Create
//   - decrypt KeyCiphertext on every read, falling back to the
//     legacy plaintext Key column for rows written before the
//     migration landed.
func (s *dbStore) SetSecrets(m *secrets.Manager) { s.secrets = m }

// SecretsManager returns the attached secrets manager (nil when
// unset). Satisfies store.SecretsProvider.
func (s *dbStore) SecretsManager() *secrets.Manager { return s.secrets }

// Ping verifies the underlying database connection is responsive.
// Returns nil when the connection is healthy.
func (s *dbStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

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
		"&_busy_timeout=10000" +
		"&_foreign_keys=on" +
		"&_synchronous=NORMAL"
	db, err := sql.Open("sqlite3", fullDSN)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// Allow concurrent reads + 1 writer. SQLite serialises writers
	// internally; 16 connections gives headroom for admin reads,
	// concurrent API requests, and the LLM hot path.
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(time.Hour) // Reuse connections for 1 hour
	s := &SQLite{dbStore: dbStore{db: db, d: dialect.SQLite{}}}
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
func (s *dbStore) applyPragmas() error {
	pragmas := []string{
		"PRAGMA cache_size=-40000",            // 40MB page cache (increased from 20MB)
		"PRAGMA temp_store=MEMORY",            // temp tables in RAM
		"PRAGMA mmap_size=536870912",          // 512MB mmap for large reads (increased from 256MB)
		"PRAGMA wal_autocheckpoint=4000",      // 4000-page WAL threshold (increased from 2000)
		"PRAGMA journal_size_limit=134217728", // 128MB WAL size limit
		"PRAGMA busy_timeout=10000",           // 10 second busy timeout (increased from 5s via DSN)
	}
	for _, p := range pragmas {
		if _, err := s.exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func (s *dbStore) Close() error { return s.db.Close() }

// defaultTimeout bounds every exec call that has no explicit
// context. Query/queryRow are NOT wrapped here: sql.Rows/Rows.Scan
// are lazy and would run after a deferred cancel, silently turning
// every read into "context canceled". Long-running statement
// protection for reads comes from connection-level timeouts:
// Postgres sets statement_timeout at open (see postgres.go) and
// SQLite is process-local with busy_timeout.
const defaultTimeout = 30 * time.Second

func (s *dbStore) exec(q string, args ...any) (sql.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return s.db.ExecContext(ctx, s.d.RewriteQuery(q), args...)
}

func (s *dbStore) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.d.RewriteQuery(q), args...)
}

func (s *dbStore) queryRow(q string, args ...any) *sql.Row {
	return s.db.QueryRow(s.d.RewriteQuery(q), args...)
}

func (s *dbStore) execContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.d.RewriteQuery(q), args...)
}

func (s *dbStore) queryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.d.RewriteQuery(q), args...)
}

func (s *dbStore) queryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.d.RewriteQuery(q), args...)
}

func (s *dbStore) migrate() error {
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
		// in per-date files under logs/YYYY-MM-DD.db (defaulting to
		// the SQLite DB's directory) via the logstore package.
		// CreateLog/QueryLogs/etc. on this Store delegate there (see
		// SetLogStore).
		`CREATE INDEX IF NOT EXISTS idx_keys_channel ON keys(channel_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tokens_plan ON tokens(plan_id)`,
		`CREATE INDEX IF NOT EXISTS idx_users_session_exp ON users(session_exp) WHERE session_exp > 0`,
		`CREATE TABLE IF NOT EXISTS redeem_codes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT UNIQUE NOT NULL,
			kind TEXT NOT NULL DEFAULT 'invite',
			amount_usd REAL NOT NULL DEFAULT 0,
			initial_quota REAL NOT NULL DEFAULT 5,
			role INTEGER NOT NULL DEFAULT 0,
			created_by INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL DEFAULT 0,
			used_by INTEGER NOT NULL DEFAULT 0,
			used_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_redeem_codes_kind ON redeem_codes(kind)`,
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
			tiers TEXT NOT NULL DEFAULT '',
			fallback TEXT NOT NULL DEFAULT '[]',
			context_fallback TEXT NOT NULL DEFAULT '[]',
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
		// P15 phase 3: edge client enrollment.
		//
		// enroll tokens are single-use and short-lived; only the hash is
		// stored so a database dump cannot be replayed at /enroll.
		`CREATE TABLE IF NOT EXISTS edge_enroll_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_hash TEXT NOT NULL UNIQUE,
			client_name TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			used_at INTEGER NOT NULL DEFAULT 0,
			used_client TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_edge_enroll_expires ON edge_enroll_tokens(expires_at)`,
		// P14 phase 3: balance history. A separate table rather than extra
		// channels columns: one channel accumulates hundreds of points,
		// and channels sits on the routing hot path.
		`CREATE TABLE IF NOT EXISTS channel_balances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER NOT NULL,
			ts INTEGER NOT NULL,
			balance TEXT NOT NULL,
			currency TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_balances_lookup ON channel_balances(channel_id, ts)`,
		// P22: vendor-reported subscription usage history. One row per
		// (channel, window) per sweep; used by the trend chart and by the
		// "how fast am I burning through the plan" view. Window is a
		// reserved word in Postgres, hence quoting via the dialect-safe
		// name "win" (kept short for index size).
		`CREATE TABLE IF NOT EXISTS channel_usage_points (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER NOT NULL,
			ts INTEGER NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			win TEXT NOT NULL DEFAULT '',
			used REAL NOT NULL DEFAULT 0,
			limit_val REAL NOT NULL DEFAULT 0,
			remaining REAL NOT NULL DEFAULT -1,
			unit TEXT NOT NULL DEFAULT '',
			resets_at INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_usage_points_lookup ON channel_usage_points(channel_id, ts)`,
		// Registered edge clients. secret_hash is sha256, not Argon2id:
		// the secret is 192 bits of randomness (not a user password), and
		// it is verified on every 30s heartbeat.
		`CREATE TABLE IF NOT EXISTS edge_clients (
			client_id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			secret_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL DEFAULT 0,
			revoked_at INTEGER NOT NULL DEFAULT 0
		)`,
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
		`CREATE TABLE IF NOT EXISTS model_costs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			model TEXT UNIQUE NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			input_price_per_1m REAL NOT NULL DEFAULT 0,
			output_price_per_1m REAL NOT NULL DEFAULT 0,
			cache_read_price_per_1m REAL NOT NULL DEFAULT 0,
			cache_write_price_per_1m REAL NOT NULL DEFAULT 0,
			reasoning_price_per_1m REAL NOT NULL DEFAULT 0,
			image_price REAL NOT NULL DEFAULT 0,
			input_price_per_1m_above_128k REAL NOT NULL DEFAULT 0,
			output_price_per_1m_above_128k REAL NOT NULL DEFAULT 0,
			input_price_per_1m_above_200k REAL NOT NULL DEFAULT 0,
			output_price_per_1m_above_200k REAL NOT NULL DEFAULT 0,
			context_window INTEGER NOT NULL DEFAULT 0,
			max_output INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.exec(s.d.RewriteDDL(q)); err != nil {
			return fmt.Errorf("exec %q: %w", q, err)
		}
	}
	if err := s.addColumn("users", "session_exp", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumn("users", "permissions", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "protocol", "TEXT NOT NULL DEFAULT 'openai'"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "intents", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "cached_input_discount", "REAL NOT NULL DEFAULT 0.1"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "total_cost_usd", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// P14: upstream balance snapshot. Empty strings / NULL mean "never
	// queried"; balance_updated_at distinguishes that from "unsupported".
	if err := s.addColumn("channels", "balance", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "balance_currency", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "balance_updated_at", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "balance_error", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "balance_url", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "balance_threshold", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// P21: upstream subscription plan quota (JSON-encoded model.PlanQuota,
	// {} = untracked). Unlike the balance snapshot this feeds routing.
	if err := s.addColumn("channels", "plan_quota", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	// P22: vendor-side subscription usage polling. usage_probe is the
	// configuration (JSON model.UsageProbe, {} = disabled);
	// usage_state caches the latest vendor report (JSON subusage.Report);
	// usage_checked_at / usage_error mirror channels.balance_* semantics.
	if err := s.addColumn("channels", "usage_probe", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "usage_state", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "usage_checked_at", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumn("channels", "usage_error", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// P14 phase 4: per-key balance. Account-level data belongs on the
	// key row, since one channel can hold several accounts.
	if err := s.addColumn("keys", "balance", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("keys", "balance_currency", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("keys", "balance_updated_at", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumn("keys", "balance_error", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("tokens", "used_usd", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumn("keys", "key_ciphertext", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("tokens", "key_ciphertext", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("tokens", "user_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumn("alerts", "disabled_reason", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("token_combo_models", "is_default", s.d.BoolColumn("INTEGER NOT NULL DEFAULT 0")); err != nil {
		return err
	}
	if err := s.addColumn("token_combo_models", "tiers", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("token_combo_models", "fallback", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumn("token_combo_models", "context_fallback", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumn("mcp_servers", "transport", "TEXT NOT NULL DEFAULT 'http'"); err != nil {
		return err
	}
	if err := s.addColumn("mcp_servers", "command", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("mcp_servers", "oauth_config_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("mcp_servers", "token_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
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
func (s *dbStore) migrateDefaultFlag() {
	res, err := s.exec(`UPDATE token_combo_models SET is_default = ? WHERE name = 'auto' AND is_default = ?`, s.d.Bool(true), s.d.Bool(false))
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
func (s *dbStore) migrateAutoCombos() {
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

// addColumn ensures the column exists. Dialects with a native
// idempotent form (Postgres: ADD COLUMN IF NOT EXISTS) run it
// directly; SQLite falls back to a PRAGMA-guarded ALTER.
func (s *dbStore) addColumn(table, column, decl string) error {
	if stmt := s.d.AddColumnIfMissing(table, column, decl); stmt != "" {
		_, err := s.exec(stmt)
		return err
	}
	return s.addColumnIfMissing(table, column, decl)
}

func (s *dbStore) addColumnIfMissing(table, column, decl string) error {
	rows, err := s.query(`PRAGMA table_info(` + table + `)`)
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
	_, err = s.exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	return err
}

// toUnix maps a time to epoch seconds; zero values (never set)
// become 0 so "0 means unset/never" semantics hold in SQL.
func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

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

// encodePQ serialises the plan quota as JSON; the zero value collapses
// to "{}" so untracked channels keep the column default.
func encodePQ(pq model.PlanQuota) string {
	if pq == (model.PlanQuota{}) {
		return "{}"
	}
	b, _ := json.Marshal(pq)
	return string(b)
}

func decodePQ(s string) model.PlanQuota {
	if s == "" {
		return model.PlanQuota{}
	}
	var pq model.PlanQuota
	_ = json.Unmarshal([]byte(s), &pq)
	return pq
}

// encodeUP serialises the usage probe; the zero value collapses to "{}"
// so untracked channels keep the column default. CookieCipher is tagged
// json:"-" so an accidental JSON write cannot leak the cookie.
func encodeUP(up model.UsageProbe) string {
	if up == (model.UsageProbe{}) {
		return "{}"
	}
	b, _ := json.Marshal(up)
	return string(b)
}

func decodeUP(s string) model.UsageProbe {
	if s == "" {
		return model.UsageProbe{}
	}
	var up model.UsageProbe
	_ = json.Unmarshal([]byte(s), &up)
	return up
}

func encodeTiers(t map[string]model.TierConfig) string {
	if t == nil {
		return ""
	}
	b, _ := json.Marshal(t)
	return string(b)
}

func decodeTiers(s string) map[string]model.TierConfig {
	if s == "" {
		return nil
	}
	var t map[string]model.TierConfig
	_ = json.Unmarshal([]byte(s), &t)
	return t
}

// ---------------- Channels ----------------
