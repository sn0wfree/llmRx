package model

import (
	"fmt"
	"time"
)

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
	StrategyQuotaAware     CostStrategy = "quota_aware"
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

// PlanQuota describes the upstream subscription plan attached to a
// channel (P21): a coding-plan subscription ("code"), a prepaid token
// package ("token"), or plain pay-as-you-go (""). The zero value
// (Limit == 0) means "not tracked" — the channel routes unchanged and
// the monitoring page shows no quota gauge.
//
// Metric selects what counts against Limit:
//
//	tokens        prompt + completion + reasoning tokens
//	prompt_tokens prompt tokens incl. cached reads (coding-plan style)
//	usd           real upstream cost in USD
//
// Window selects the reset cadence:
//
//	monthly calendar month (UTC)
//	daily    UTC day
//	rolling  trailing WindowHours hours (e.g. 5 for GLM/Claude-style
//	         coding plans)
type PlanQuota struct {
	Kind        string  `yaml:"kind" json:"kind,omitempty"`
	Metric      string  `yaml:"metric" json:"metric,omitempty"`
	Limit       float64 `yaml:"limit" json:"limit,omitempty"`
	Window      string  `yaml:"window" json:"window,omitempty"`
	WindowHours int     `yaml:"window_hours" json:"window_hours,omitempty"`
}

// UsageProbe configures how llmRx asks the UPSTREAM VENDOR for this
// channel's real subscription usage (P22). It is the vendor-side
// counterpart of PlanQuota: PlanQuota is what llmRx measured locally,
// UsageProbe is what the provider itself reports.
//
// Kind selects the parser:
//
//	openrouter_key  GET {base}/key with the channel's API key
//	                (limit / usage / limit_remaining / usage_daily…)
//	zhipu_web       EXPERIMENTAL: scrape the GLM Coding Plan dashboard
//	                endpoint with an operator-supplied session cookie
//	custom          generic JSONPath extraction from EndpointURL
//
// The zero value (Kind == "") means "do not probe".
type UsageProbe struct {
	Kind string `yaml:"kind" json:"kind,omitempty"`
	// CookieCipher is the AES-GCM-sealed session cookie for web
	// scrapers (zhipu_web). Never serialized to the API — see
	// CookieMasked for the UI.
	CookieCipher string `json:"-"`
	CookieMasked string `json:"cookie_masked,omitempty"`
	// EndpointURL overrides the parser's default endpoint (required
	// for "custom", optional elsewhere).
	EndpointURL string `json:"endpoint_url,omitempty"`
	// JSONPath is the "a.b.c" path (dot-separated, array indexes as
	// numbers) into the response used by the "custom" parser. The
	// values found are read as {used, limit, remaining, resets_at}.
	JSONPath string `json:"json_path,omitempty"`
	// WindowName labels the custom parser's single window.
	WindowName string `json:"window_name,omitempty"`
	// Unit is "usd" | "tokens" | "credits" | "requests".
	Unit string `json:"unit,omitempty"`
	// IntervalSec overrides the monitor's global interval for this
	// channel (0 = global).
	IntervalSec int `json:"interval_sec,omitempty"`
}

// UsageProbe kinds.
const (
	UsageProbeOpenRouter = "openrouter_key"
	UsageProbeZhipuWeb   = "zhipu_web"
	UsageProbeCustom     = "custom"
)

// Validate checks the probe configuration. The zero value is valid
// ("disabled"); an enabled probe needs a recognised kind, and the
// custom kind needs an endpoint + JSONPath + window name.
func (p UsageProbe) Validate() error {
	if p.Kind == "" {
		return nil
	}
	switch p.Kind {
	case UsageProbeOpenRouter, UsageProbeZhipuWeb, UsageProbeCustom:
	default:
		return fmt.Errorf("usage_probe.kind must be openrouter_key|zhipu_web|custom")
	}
	if p.Kind == UsageProbeCustom {
		if p.EndpointURL == "" {
			return fmt.Errorf("usage_probe.endpoint_url is required for the custom parser")
		}
		if p.JSONPath == "" {
			return fmt.Errorf("usage_probe.json_path is required for the custom parser")
		}
		if p.WindowName == "" {
			return fmt.Errorf("usage_probe.window_name is required for the custom parser")
		}
	}
	if p.Kind == UsageProbeZhipuWeb && p.CookieCipher == "" {
		return fmt.Errorf("usage_probe requires a session cookie for the zhipu_web parser")
	}
	if p.IntervalSec < 0 || p.IntervalSec > 24*60*60 {
		return fmt.Errorf("usage_probe.interval_sec must be 0..86400")
	}
	switch p.Unit {
	case "", "usd", "tokens", "credits", "requests":
	default:
		return fmt.Errorf("usage_probe.unit must be usd|tokens|credits|requests")
	}
	return nil
}

// Enabled reports whether the probe should run for this channel.
func (p UsageProbe) Enabled() bool { return p.Kind != "" }

// Quota metrics accepted in PlanQuota.Metric.
const (
	QuotaMetricTokens       = "tokens"
	QuotaMetricPromptTokens = "prompt_tokens"
	QuotaMetricUSD          = "usd"
)

// Quota windows accepted in PlanQuota.Window.
const (
	QuotaWindowMonthly = "monthly"
	QuotaWindowDaily   = "daily"
	QuotaWindowRolling = "rolling"
)

// Validate checks a PlanQuota against the accepted enums. The zero
// value is valid ("not tracked"); a non-zero quota must set
// Metric + Limit + Window, and rolling windows need WindowHours
// within 1..720 (30 days).
func (q PlanQuota) Validate() error {
	if q == (PlanQuota{}) {
		return nil
	}
	switch q.Metric {
	case QuotaMetricTokens, QuotaMetricPromptTokens, QuotaMetricUSD:
	default:
		return fmt.Errorf("plan_quota.metric must be tokens|prompt_tokens|usd")
	}
	if q.Limit <= 0 {
		return fmt.Errorf("plan_quota.limit must be > 0")
	}
	switch q.Window {
	case QuotaWindowMonthly, QuotaWindowDaily:
	case QuotaWindowRolling:
		if q.WindowHours <= 0 || q.WindowHours > 24*30 {
			return fmt.Errorf("plan_quota.window_hours must be in 1..720 for rolling windows")
		}
	default:
		return fmt.Errorf("plan_quota.window must be monthly|daily|rolling")
	}
	switch q.Kind {
	case "", "code", "token":
	default:
		return fmt.Errorf("plan_quota.kind must be code|token|empty")
	}
	return nil
}

// Tracked reports whether the quota is configured (used by the router
// guard and the monitoring page to skip untracked channels).
func (q PlanQuota) Tracked() bool { return q.Limit > 0 }

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
	TotalCostUSD        float64              `json:"total_cost_usd"`

	// Upstream balance snapshot (P14). Written by internal/balance and
	// never used for routing or billing — it is an operator-facing view
	// of what the upstream account has left.
	//
	// Balance is a string to preserve the upstream's own precision
	// (some report "110.00", others "12590.0042"). Empty means either
	// "never queried" or "provider has no balance endpoint"; the two
	// are distinguished by BalanceUpdatedAt != nil.
	Balance          string     `json:"balance,omitempty"`
	BalanceCurrency  string     `json:"balance_currency,omitempty"`
	BalanceUpdatedAt *time.Time `json:"balance_updated_at,omitempty"`
	// BalanceError carries the last failure (truncated to 512 chars).
	// Cleared on the next successful query.
	BalanceError string `json:"balance_error,omitempty"`
	// BalanceURL overrides the provider descriptor's balance path for
	// this channel (relay/proxy deployments that move the endpoint).
	BalanceURL string `json:"balance_url,omitempty"`
	// BalanceThreshold is the "low balance" alert line, in the
	// channel's own currency (P14 phase 2). 0 = not monitored by the
	// balance_low alert. Per channel rather than per rule because each
	// channel has its own currency and its own definition of low.
	BalanceThreshold float64 `json:"balance_threshold,omitempty"`

	// PlanQuota is the upstream subscription plan attached to this
	// channel (P21). Unlike the P14 balance snapshot it IS used by the
	// router: a channel whose quota window is exhausted is dropped from
	// the candidate set (unless nothing else can serve the model), and
	// the quota_aware cost strategy prefers channels with quota left.
	// Zero value = untracked.
	PlanQuota PlanQuota `json:"plan_quota"`

	// UsageProbe configures vendor-side subscription usage polling
	// (P22). Unlike PlanQuota (llmRx's local count) this asks the
	// provider what it thinks was consumed, so the two can be compared.
	// Zero value = no probe.
	UsageProbe UsageProbe `json:"usage_probe"`

	// UsageState caches the most recent vendor report as JSON
	// (subusage.Report). Empty = never probed successfully. P22 writes
	// this; the monitoring/subscription pages read it.
	UsageState string `json:"-"`
	// UsageCheckedAt is when the last probe ran (success or failure).
	UsageCheckedAt *time.Time `json:"usage_checked_at,omitempty"`
	// UsageError carries the last probe failure (truncated). Cleared on
	// the next success; the last good UsageState is kept.
	UsageError string `json:"usage_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

	// Per-key upstream balance (P14 phase 4). Balance is account-level,
	// and one channel can hold several keys, each a separate account —
	// so the accurate place to record it is here, not only on the
	// channel row.
	//
	// Balance is a string to preserve the upstream's precision, matching
	// channels.balance.
	Balance          string     `json:"balance,omitempty"`
	BalanceCurrency  string     `json:"balance_currency,omitempty"`
	BalanceUpdatedAt *time.Time `json:"balance_updated_at,omitempty"`
	BalanceError     string     `json:"balance_error,omitempty"`
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
