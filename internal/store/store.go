package store

import (
	"github.com/sn0wfree/llmRx/internal/model"
)

type Store interface {
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

	// Tokens
	GetToken(key string) (*model.Token, error)
	GetTokens() ([]model.Token, error)
	CreateToken(t *model.Token) error
	DeleteToken(id int64) error

	// Plans
	GetPlans() ([]model.Plan, error)
	GetPlan(id int64) (*model.Plan, error)
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error

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
	LogStats() (LogStats, error)
	QueryLogs(f LogFilter) ([]model.Log, int64, error)

	// Analytics
	TimeSeries(f LogFilter, bucketSec int64) ([]SeriesPoint, error)
	TopByModel(f LogFilter, limit int) ([]NamedMetric, error)
	TopByChannel(f LogFilter, limit int) ([]NamedMetric, error)
	TopByToken(f LogFilter, limit int) ([]NamedMetric, error)
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
	TokenID    int64
	ChannelID  int64
	Model      string
	StatusCode int
	CreatedFrom int64
	CreatedTo   int64
	Limit       int
	Offset      int
}

// SeriesPoint is one bucket of a time-series.
type SeriesPoint struct {
	Bucket   int64   `json:"bucket"`             // unix seconds at bucket start
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
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
