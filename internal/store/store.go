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

	// Logs
	CreateLog(l *model.Log) error
	GetLogs(limit, offset int) ([]model.Log, error)
	CountLogs() (int64, error)
	DeleteLogsBefore(unixSec int64) (int64, error)
	LogStats() (LogStats, error)
	QueryLogs(f LogFilter) ([]model.Log, int64, error)

	// Analytics
	TimeSeries(f LogFilter, bucketSec int64) ([]SeriesPoint, error)
	TopByModel(f LogFilter, limit int) ([]NamedMetric, error)
	TopByChannel(f LogFilter, limit int) ([]NamedMetric, error)
	TopByToken(f LogFilter, limit int) ([]NamedMetric, error)

	// Alerts
	GetAlerts() ([]model.Alert, error)
	GetAlert(id int64) (*model.Alert, error)
	CreateAlert(a *model.Alert) error
	UpdateAlert(a *model.Alert) error
	DeleteAlert(id int64) error
	RecordAlertFired(id int64, atUnix int64) error
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

	// BYOK (Phase 1.5 reserved — see docs/BYOK.md). Implementations
	// should return ErrNotImplemented until the BYOK feature ships.
	// The interface is in place so the router and admin pages can
	// reference BYOK paths without future refactoring.
	CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error)
	ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error)
	GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error)
	DeleteBYOKChannel(ctx context.Context, id int64) error
}

type LogStats struct {
	PromptTokens     int64
	CompletionTokens int64
	RealCostUSD      float64
	BilledCostUSD    float64
	Total            int64
	Errors           int64
}

// LogFilter narrows a logs query. Zero values mean "no filter".
// CreatedFrom/To are unix seconds; Limit/Offset paginate.
type LogFilter struct {
	TokenID     int64
	ChannelID   int64
	Model       string
	StatusCode  int
	CreatedFrom int64
	CreatedTo   int64
	Limit       int
	Offset      int
}

// SeriesPoint is one bucket of a time-series.
type SeriesPoint struct {
	Bucket           int64   `json:"bucket"` // unix seconds at bucket start
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	RealCostUSD      float64 `json:"real_cost_usd"`
	BilledCostUSD    float64 `json:"billed_cost_usd"`
}

// NamedMetric is a (label, value) pair for top-N queries.
type NamedMetric struct {
	Label  string  `json:"label"`
	Count  int64   `json:"count"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}
