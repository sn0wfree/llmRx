package model

import "time"

// Status enum note: the legacy "active = 0" / "disabled = 1"
// convention is preserved for Token and Key (matches the values
// shipped in the v1 schema). Channel flipped the convention in
// v2 — "enabled = 1, disabled = 2" — to match what most operators
// expect when reading the admin UI. Don't normalise these without
// a database migration; call sites encode the chosen convention
// (e.g. the webui token form sends status="0" for active and
// status="1" for disabled, while the channel form sends "1" for
// enabled).

type ChannelStatus int

const (
	ChannelUnknown   ChannelStatus = 0
	ChannelEnabled   ChannelStatus = 1
	ChannelDisabled  ChannelStatus = 2
	ChannelAutoBreak ChannelStatus = 3
)

type KeyStatus int

const (
	KeyActive      KeyStatus = 0
	KeyRateLimited KeyStatus = 1
	KeyDisabled    KeyStatus = 2
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
	StrategyCheapest       CostStrategy = "cheapest"
	StrategyFastest        CostStrategy = "fastest"
	StrategyBalanced       CostStrategy = "balanced"
	StrategyWeightedRandom CostStrategy = "weighted_random"
	StrategyLatency        CostStrategy = "latency"
	StrategyCostLatency    CostStrategy = "cost_latency_blend"
)

// ComboMode selects the routing strategy inside a token combo model.
type ComboMode string

const (
	// ComboModeLoadBalance: expand the combo's model list into a
	// candidate set and run L1-L5 to pick a single channel. Default.
	ComboModeLoadBalance ComboMode = "load_balance"
	// ComboModeSerial: try underlying models in order; first 2xx wins,
	// others serve as fallback. Non-2xx responses trigger L2 breaker.
	ComboModeSerial ComboMode = "serial"
	// ComboModeAuto: classify the prompt into a complexity tier, pick
	// a model for that tier via Thompson sampling over (tier, model)
	// quality arms, fail over within the tier, then fall back to the
	// Fallback list. Only meaningful for combos with a Tiers map.
	ComboModeAuto ComboMode = "auto"
	// (reserved) ComboModeParallel  ComboMode = "parallel"
	// (reserved) ComboModeIntent    ComboMode = "intent"
)

// TierConfig declares the candidate models for one complexity
// tier of a mode:auto combo. The Models order is the cost order:
// the auto router tries cheaper models first and only escalates
// when Thompson sampling or failover requires it.
type TierConfig struct {
	Models []string `json:"models"`
}

// AutoTiers are the canonical complexity tier names for
// mode:auto combos, ordered from cheapest to most capable.
var AutoTiers = []string{"simple", "standard", "complex", "agentic"}

// ValidAutoTier reports whether t is one of the canonical tiers.
func ValidAutoTier(t string) bool {
	for _, k := range AutoTiers {
		if k == t {
			return true
		}
	}
	return false
}

type CircuitBreakerConfig struct {
	MaxFailures  int           `yaml:"max_failures" json:"max_failures"`
	ResetTimeout time.Duration `yaml:"reset_timeout" json:"reset_timeout"`
}

type Channel struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Protocol    string   `json:"protocol"`
	BaseURL     string   `json:"base_url"`
	Models      []string `json:"models"`
	Intents     []string `json:"intents"`
	Priority    int      `json:"priority"`
	InputPrice  float64  `json:"input_price_per_1m"`
	OutputPrice float64  `json:"output_price_per_1m"`
	// CachedInputDiscount is the rate applied to prompt tokens that
	// the upstream identifies as cache hits. The actual charge for
	// cached tokens is CachedInputDiscount * InputPrice per 1M tokens.
	//
	//   0.1 = pay 10% of normal (Anthropic default = 90% off)
	//   0.0 = cached tokens free
	//   1.0 = no discount (cached = normal rate)
	//
	// Default if unset: 0.1.
	CachedInputDiscount float64              `json:"cached_input_discount"`
	CircuitBreaker      CircuitBreakerConfig `json:"circuit_breaker"`
	Status              ChannelStatus        `json:"status"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

type Key struct {
	ID        int64 `json:"id"`
	ChannelID int64 `json:"channel_id"`
	// Key is the plaintext form. After P0 the store treats this
	// field as **transient** — write paths accept plaintext, store
	// it encrypted in KeyCiphertext, and zero out Key. Read paths
	// decrypt KeyCiphertext back into Key. Legacy rows created
	// before P0 still have plaintext in Key and an empty
	// KeyCiphertext; the store migrates them lazily on first read.
	Key           string    `json:"key,omitempty"`
	KeyCiphertext string    `json:"-"`
	KeyMasked     string    `json:"key_masked"`
	Status        KeyStatus `json:"status"`
	LastUsedAt    time.Time `json:"last_used_at"`
	CreatedAt     time.Time `json:"created_at"`
}

type Plan struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	BudgetUSD   float64   `json:"budget_usd"`
	UsedUSD     float64   `json:"used_usd"`
	MarkupRatio float64   `json:"markup_ratio"`
	Status      int       `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Token struct {
	ID              int64       `json:"id"`
	PlanID          int64       `json:"plan_id"`
	Key             string      `json:"key,omitempty"`
	Name            string      `json:"name"`
	Status          TokenStatus `json:"status"`
	RPM             int         `json:"rpm"`
	TPM             int         `json:"tpm"`
	UsedUSD         float64     `json:"used_usd"`
	ModelsWhitelist []string    `json:"models_whitelist"`
	IPWhitelist     []string    `json:"ip_whitelist"`
	ExpiresAt       time.Time   `json:"expires_at"`
	LastUsedAt      time.Time   `json:"last_used_at"`
	CreatedAt       time.Time   `json:"created_at"`
	// UserID links the token to its owning user (0 = not linked,
	// e.g. legacy admin-created tokens). Backfilled from
	// token.name == users.username on startup for registration-created
	// tokens, so usage stats can attribute requests to a user.
	UserID int64 `json:"user_id"`
}

// ModelCost is the billing rate card for a model. It is the SOLE
// source of truth for per-model pricing (channel-level prices no
// longer participate in cost calculation). Prices are per 1M tokens
// (image_price is per image). A zero price field means "free" where
// a dimension exists, or "fall back to the base tier" for the
// optional dimensions below:
//
//	cache_write = 0 → non-cached prompt billed at input_price
//	reasoning   = 0 → reasoning tokens billed at output_price
//	above_128k/above_200k = 0 → fall back to the lower tier price
type ModelCost struct {
	ID       int64  `json:"id"`
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`

	InputPricePer1M  float64 `json:"input_price_per_1m"`
	OutputPricePer1M float64 `json:"output_price_per_1m"`
	// CacheReadPricePer1M is charged for prompt tokens served from a
	// cache: the upstream prompt cache AND llmRx's own response cache
	// (cache hits). 0 = cached tokens free.
	CacheReadPricePer1M float64 `json:"cache_read_price_per_1m"`
	// CacheWritePricePer1M, when > 0, is charged for the non-cached
	// prompt tokens that (re)populate the upstream cache. 0 = those
	// tokens are billed at input_price_per_1m.
	CacheWritePricePer1M float64 `json:"cache_write_price_per_1m"`
	// ReasoningPricePer1M, when > 0, replaces output_price_per_1m for
	// the reasoning/thinking portion of the completion tokens.
	ReasoningPricePer1M float64 `json:"reasoning_price_per_1m"`
	// ImagePrice is charged per input image (0 = free / unsupported).
	ImagePrice float64 `json:"image_price"`

	// Long-context tier pricing. When prompt/completion tokens exceed
	// the threshold, the excess portion is billed at the tier rate.
	// 0 = fall back to the lower tier.
	InputPricePer1MAbove128K  float64 `json:"input_price_per_1m_above_128k"`
	OutputPricePer1MAbove128K float64 `json:"output_price_per_1m_above_128k"`
	InputPricePer1MAbove200K  float64 `json:"input_price_per_1m_above_200k"`
	OutputPricePer1MAbove200K float64 `json:"output_price_per_1m_above_200k"`

	ContextWindow int `json:"context_window"`
	MaxOutput     int `json:"max_output"`

	UpdatedAt time.Time `json:"updated_at"`
}

// ModelCost thresholds (tokens) for long-context tier pricing.
const (
	ModelCostTier128K = 128000
	ModelCostTier200K = 200000
)

type User struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	PasswordHash string   `json:"-"`
	Role         UserRole `json:"role"`
	// Permissions overrides the role's default permission set. JSON
	// array of strings: "+perm" to grant, "-perm" to revoke. Empty
	// means "use role defaults only".
	Permissions  string     `json:"permissions,omitempty"`
	Status       int        `json:"status"`
	SessionToken string     `json:"-"`
	SessionExp   *time.Time `json:"session_expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// RedeemCodeKind distinguishes invite codes (registration gate) from
// top-up codes (add budget to a plan).
type RedeemCodeKind string

const (
	RedeemKindInvite RedeemCodeKind = "invite"
	RedeemKindTopup  RedeemCodeKind = "topup"
)

// RedeemCode is a single-use code: kind=invite gates registration
// (and grants initial_quota), kind=topup adds amount_usd to a
// plan's budget. Codes are created by an admin and consumed once.
type RedeemCode struct {
	ID int64 `json:"id"`
	// Code is the redeemable string, e.g. "RXM-ABCD1234".
	Code string `json:"code"`
	// Kind: "invite" (registration gate) or "topup" (add budget).
	Kind RedeemCodeKind `json:"kind"`
	// AmountUSD is the budget added on redemption (topup codes).
	AmountUSD float64 `json:"amount_usd"`
	// InitialQuota is the plan budget granted at registration
	// (invite codes, configurable, default 5).
	InitialQuota float64 `json:"initial_quota"`
	// Role granted to the registering user (invite codes).
	Role UserRole `json:"role"`
	// CreatedBy is the admin user id that generated the code.
	CreatedBy int64 `json:"created_by"`
	// ExpiresAt zero = never expires.
	ExpiresAt time.Time `json:"expires_at"`
	// UsedBy / UsedAt zero = unused.
	UsedBy int64     `json:"used_by"`
	UsedAt time.Time `json:"used_at"`
	// CreatedAt records generation time.
	CreatedAt time.Time `json:"created_at"`
}

type Log struct {
	ID               int64  `json:"id"`
	TokenID          int64  `json:"token_id"`
	ChannelID        int64  `json:"channel_id"`
	KeyID            int64  `json:"key_id"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	// CachedTokens is the number of prompt tokens served from the
	// upstream's prompt cache (Anthropic, OpenAI GPT-5+, etc.). The
	// real cost calculation subtracts (CachedTokens * InputPrice *
	// (1 - CachedInputDiscount)).
	CachedTokens int `json:"cached_tokens"`
	// ReasoningTokens is the portion of CompletionTokens used for
	// chain-of-thought / thinking, when the upstream reports it
	// (DeepSeek, GLM, etc.). Billed at the model's reasoning rate.
	ReasoningTokens int `json:"reasoning_tokens"`
	// CacheHit marks a row produced by llmRx's own response cache
	// (no upstream call). Billed at the model's cache-read rate.
	CacheHit      bool    `json:"cache_hit"`
	RealCostUSD   float64 `json:"real_cost_usd"`
	BilledCostUSD float64 `json:"billed_cost_usd"`
	DurationMs    int64   `json:"duration_ms"`
	StatusCode    int     `json:"status_code"`
	RouterPath    string  `json:"router_path"`
	RequestIP     string  `json:"request_ip"`
	// Endpoint records the log source. Empty means a normal LLM
	// request; "mcp" marks a row produced by an MCP tool call.
	Endpoint string `json:"endpoint"`
	// Units is the number of billed units this row represents
	// (1 per MCP tool call).
	Units     int       `json:"units"`
	CreatedAt time.Time `json:"created_at"`
}

// ProviderDef is a user-defined provider descriptor stored in the DB.
// It maps a provider name to a protocol adapter and a default base URL.
// Built-in providers come from providers.yaml; user-defined ones are
// created via the Admin UI and stored in the providers table.
type ProviderDef struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Protocol    string    `json:"protocol"`
	BaseURL     string    `json:"base_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TokenComboModel maps a virtual model name to a pool of underlying
// real model names. Each token can define its own combos; the combo
// name is the "entry point" the client sends in the model field.
//
// load_balance: expand Models into the L1 candidate set, run L1-L5.
// serial:       try Models in order; first 2xx wins.
// auto:         classify the prompt into a complexity tier (Tiers
//               map), pick a model per tier via Thompson sampling,
//               fail over within the tier, then Fallback.
//
// combo names are token-scoped (two tokens may both have "smart-1")
// and must not collide with any channel.Models real model name.
type TokenComboModel struct {
	ID       int64        `json:"id"`
	TokenID  int64        `json:"token_id"`
	Name     string       `json:"name"`
	Models   []string     `json:"models"`
	Mode     ComboMode    `json:"mode"`
	Strategy CostStrategy `json:"strategy"` // "" = inherit global
	// Tiers is the complexity-tier candidate table for mode:auto
	// combos. Empty for load_balance/serial combos.
	Tiers    map[string]TierConfig `json:"tiers,omitempty"`
	Fallback []string              `json:"fallback,omitempty"`
	// ContextFallback lists models tried when the classified tier's
	// candidates all have context windows too small for the prompt.
	// Escalates to a larger-context model instead of failing the
	// request. Sorted by context_window ascending when selected
	// (smallest that fits wins; cost is usually lower too).
	ContextFallback []string  `json:"context_fallback,omitempty"`
	Enabled         bool      `json:"enabled"`
	IsDefault       bool      `json:"is_default"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// LearnedRouterConfig holds configuration for the learned router
// (trained ML model that predicts best model for a prompt).
type LearnedRouterConfig struct {
	Enabled          bool    `yaml:"enabled" json:"enabled"`
	ModelPath        string  `yaml:"model_path" json:"model_path"`
	VocabPath        string  `yaml:"vocab_path" json:"vocab_path"`
	FeatureStatsPath string  `yaml:"feature_stats_path" json:"feature_stats_path"`
	WeightTs         float64 `yaml:"weight_ts" json:"weight_ts"`
	WeightLearned    float64 `yaml:"weight_learned" json:"weight_learned"`
	WeightCost       float64 `yaml:"weight_cost" json:"weight_cost"`
	Fallback         bool    `yaml:"fallback" json:"fallback"` // fallback to TS when model unavailable
}
