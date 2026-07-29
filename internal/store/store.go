package store

import (
	"context"
	"database/sql"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

type Store interface {
	Ping(ctx context.Context) error
	Close() error

	// Channels
	GetChannels() ([]model.Channel, error)
	GetChannel(id int64) (*model.Channel, error)
	CreateChannel(ch *model.Channel) error
	UpdateChannel(ch *model.Channel) error
	DeleteChannel(id int64) error
	// GetDrainedChannels returns enabled channels that have zero
	// active keys. Used by the key_exhausted alert rule.
	GetDrainedChannels() ([]DrainedChannel, error)

	// Keys
	GetKeys(channelID int64) ([]model.Key, error)
	CreateKey(k *model.Key) error
	DeleteKey(id int64) error
	// WipeKeys clears all key material (plaintext + ciphertext) in
	// the keys table, leaving the row shells intact so channel IDs
	// and masked hints survive. Used by the `-wipe-keys` recovery
	// command when the master key has rotated and the old ciphertext
	// can no longer be decrypted. Returns rows affected.
	WipeKeys() (int64, error)

	// Tokens
	GetToken(key string) (*model.Token, error)
	GetTokenByID(id int64) (*model.Token, error)
	GetTokens() ([]model.Token, error)
	CreateToken(t *model.Token) error
	UpdateToken(t *model.Token) error
	DeleteToken(id int64) error
	IncrementTokenSpend(tokenID int64, amount float64) error
	IncrementPlanSpend(planID int64, amount float64) error
	// MarkTokenExpired flips a token's status to TokenExpired so
	// the cache skips it on next reload. Used by the expiry hook
	// wired into tokencache.SetExpirer.
	MarkTokenExpired(tokenID int64) error
	// RecordRequestSpend atomically credits both the per-token
	// and per-plan spend ledgers in a single SQL transaction.
	// On ErrBudgetExceeded the transaction is rolled back so
	// the two ledgers cannot drift. planID==0 skips the plan
	// leg. This is the canonical entry point for the chat path.
	RecordRequestSpend(tokenID, planID int64, amount float64) error

	// Plans
	GetPlans() ([]model.Plan, error)
	GetPlan(id int64) (*model.Plan, error)
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error
	DeletePlan(id int64) error

	// Users
	GetUsers() ([]model.User, error)
	GetUser(id int64) (*model.User, error)
	GetUserByUsername(username string) (*model.User, error)
	GetUserBySession(token string) (*model.User, error)
	CreateUser(u *model.User) error
	UpdateUser(u *model.User) error
	CleanupExpiredSessions() (int64, error)

	// Alerts
	GetAlerts() ([]model.Alert, error)
	GetAlert(id int64) (*model.Alert, error)
	CreateAlert(a *model.Alert) error
	UpdateAlert(a *model.Alert) error
	DeleteAlert(id int64) error
	RecordAlertFired(id int64, atUnix int64) error
	// DisableAlert flips the rule's enabled flag to 0 and records
	// the human-readable reason (e.g. "cost_spike window > 4 days").
	// Auto-disable lets the alert loop stop re-reporting a permanent
	// misconfiguration; the operator can re-enable via the admin UI.
	DisableAlert(id int64, reason string) error
	GetAlertEvents(limit int) ([]model.AlertEvent, error)
	CreateAlertEvent(e *model.AlertEvent) error
	AckAlertEvent(id int64) error

	// Raw access for subsystems that need bespoke SQL (alerts,
	// retention jobs). The caller is responsible for the query.
	RawQueryRow(query string, args ...any) *sql.Row
	RawQuery(query string, args ...any) (*sql.Rows, error)

	// RuntimeSettings persists the runtime.Defaults snapshot as a
	// single JSON row so admin changes survive restarts. Get
	// returns (nil, nil) when the table is empty (no overrides
	// recorded yet — caller should fall back to YAML seeds).
	GetRuntimeSettings() ([]byte, error)
	SetRuntimeSettings(payload []byte) error

	// ReencryptAllKeys re-encrypts every key_ciphertext row from
	// oldMgr to newMgr. Returns the count of keys rotated.
	ReencryptAllKeys(oldMgr, newMgr *secrets.Manager) (int, error)
	SetSecrets(m *secrets.Manager)
	RotateMasterKey(newKeyHex string) (int, error)

// BYOK (Bring Your Own Key) — consumer-supplied upstream keys.
	CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error)
	ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error)
	GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error)
	// GetBYOKChannelByIP finds the most recently created active row
	// for a given client IP — used by the UnknownTokenHook path.
	GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error)
	// TouchBYOKChannel records a successful use of a BYOK channel.
	TouchBYOKChannel(ctx context.Context, id int64) error
	DeleteBYOKChannel(ctx context.Context, id int64) error

	// ProviderDefs - user-defined provider descriptors created via
	// the Admin UI. Built-in providers come from providers.yaml and
	// are not stored in the DB; only operator-created ones live here.
	GetProviderDefs() ([]model.ProviderDef, error)
	CreateProviderDef(p *model.ProviderDef) error
	DeleteProviderDef(id int64) error

	// ComboModels - per-token virtual model mappings. Each combo
	// maps a virtual model name to a pool of underlying real model
	// names; the combo name is what clients send in the model field.
	GetComboModels(tokenID int64) ([]model.TokenComboModel, error)
	GetComboModel(id int64) (*model.TokenComboModel, error)
	// GetAllComboModels returns every enabled combo across all tokens
	// in one query — used by tokencache.Reload() to avoid N+1.
	GetAllComboModels() ([]model.TokenComboModel, error)
	ListAllComboModels() ([]model.TokenComboModel, error)
	CreateComboModel(c *model.TokenComboModel) error
	UpdateComboModel(c *model.TokenComboModel) error
	DeleteComboModel(id int64) error
	// SetDefaultModelSet promotes comboID to the token's default
	// set (the alias "auto" resolves to it) and demotes any other.
	// comboID == 0 clears the default.
	SetDefaultModelSet(tokenID, comboID int64) error

	// Guardrails
	GetEnabledGuardrailRules() ([]model.GuardrailRule, error)
	GetGuardrailRules() ([]model.GuardrailRule, error)
	GetGuardrailRule(id int64) (*model.GuardrailRule, error)
	CreateGuardrailRule(r *model.GuardrailRule) error
	UpdateGuardrailRule(r *model.GuardrailRule) error
	DeleteGuardrailRule(id int64) error
	CreateGuardrailEvent(e *model.GuardrailEvent) error
	GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error)
}

type DrainedChannel struct {
	ID   int64
	Name string
}
