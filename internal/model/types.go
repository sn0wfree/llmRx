package model

import "time"

type ChannelStatus int

const (
	ChannelUnknown   ChannelStatus = 0
	ChannelEnabled   ChannelStatus = 1
	ChannelDisabled  ChannelStatus = 2
	ChannelAutoBreak ChannelStatus = 3
)

type KeyStatus int

const (
	KeyActive       KeyStatus = 0
	KeyRateLimited  KeyStatus = 1
	KeyDisabled     KeyStatus = 2
)

type TokenStatus int

const (
	TokenActive    TokenStatus = 0
	TokenDisabled  TokenStatus = 1
	TokenExhausted TokenStatus = 2
	TokenExpired   TokenStatus = 3
)

type UserRole int

const (
	RoleUser  UserRole = 0
	RoleAdmin UserRole = 10
	RoleRoot  UserRole = 100
)

type CostStrategy string

const (
	StrategyCheapest  CostStrategy = "cheapest"
	StrategyFastest   CostStrategy = "fastest"
	StrategyBalanced  CostStrategy = "balanced"
)

type CircuitBreakerConfig struct {
	MaxFailures  int           `yaml:"max_failures" json:"max_failures"`
	ResetTimeout time.Duration `yaml:"reset_timeout" json:"reset_timeout"`
}

type Channel struct {
	ID             int64              `json:"id" gorm:"primaryKey"`
	Name           string             `json:"name" gorm:"uniqueIndex;size:128"`
	Provider       string             `json:"provider" gorm:"size:64"`
	Protocol       string             `json:"protocol" gorm:"size:32"`
	BaseURL        string             `json:"base_url" gorm:"size:512"`
	Models         []string           `json:"models" gorm:"serializer:json"`
	Intents        []string           `json:"intents" gorm:"serializer:json"`
	Priority       int                `json:"priority"`
	InputPrice     float64            `json:"input_price_per_1m"`
	OutputPrice    float64            `json:"output_price_per_1m"`
	CircuitBreaker CircuitBreakerConfig `json:"circuit_breaker" gorm:"serializer:json"`
	Status         ChannelStatus      `json:"status"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

type Key struct {
	ID          int64     `json:"id" gorm:"primaryKey"`
	ChannelID   int64     `json:"channel_id" gorm:"index"`
	Key         string    `json:"key,omitempty" gorm:"size:512"`
	KeyMasked   string    `json:"key_masked" gorm:"size:64"`
	Status      KeyStatus `json:"status"`
	LastUsedAt  time.Time `json:"last_used_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type Plan struct {
	ID           int64     `json:"id" gorm:"primaryKey"`
	Name         string    `json:"name" gorm:"size:128"`
	BudgetUSD    float64   `json:"budget_usd"`
	UsedUSD      float64   `json:"used_usd"`
	MarkupRatio  float64   `json:"markup_ratio"`
	Status       int       `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Token struct {
	ID              int64       `json:"id" gorm:"primaryKey"`
	PlanID          int64       `json:"plan_id" gorm:"index"`
	Key             string      `json:"key,omitempty" gorm:"uniqueIndex;size:64"`
	Name            string      `json:"name" gorm:"size:128"`
	Status          TokenStatus `json:"status"`
	RPM             int         `json:"rpm"`
	TPM             int         `json:"tpm"`
	ModelsWhitelist []string    `json:"models_whitelist" gorm:"serializer:json"`
	IPWhitelist     []string    `json:"ip_whitelist" gorm:"serializer:json"`
	ExpiresAt       time.Time   `json:"expires_at"`
	LastUsedAt      time.Time   `json:"last_used_at"`
	CreatedAt       time.Time   `json:"created_at"`
}

type User struct {
	ID           int64      `json:"id" gorm:"primaryKey"`
	Username     string     `json:"username" gorm:"uniqueIndex;size:64"`
	PasswordHash string     `json:"-" gorm:"size:256"`
	Role         UserRole   `json:"role"`
	Status       int        `json:"status"`
	SessionToken string     `json:"-" gorm:"size:128"`
	SessionExp   *time.Time `json:"session_expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Log struct {
	ID              int64     `json:"id" gorm:"primaryKey"`
	TokenID         int64     `json:"token_id" gorm:"index"`
	ChannelID       int64     `json:"channel_id"`
	KeyID           int64     `json:"key_id"`
	Model           string    `json:"model" gorm:"size:128"`
	PromptTokens    int       `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	RealCostUSD     float64   `json:"real_cost_usd"`
	BilledCostUSD   float64   `json:"billed_cost_usd"`
	DurationMs      int64     `json:"duration_ms"`
	StatusCode      int       `json:"status_code"`
	RouterPath      string    `json:"router_path" gorm:"size:128"`
	RequestIP       string    `json:"request_ip" gorm:"size:64"`
	CreatedAt       time.Time `json:"created_at"`
}
