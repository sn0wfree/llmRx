package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Database DatabaseConfig  `yaml:"database"`
	Strategy StrategyConfig  `yaml:"strategy"`
	Tokens   []TokenConfig   `yaml:"tokens"`
	Channels []ChannelConfig `yaml:"channels"`
	Secrets  SecretsConfig   `yaml:"secrets"`
	BYOK     BYOKConfig      `yaml:"byok"`
	Providers []ProviderConfig `yaml:"providers"`
}

type ServerConfig struct {
	Host               string  `yaml:"host"`
	Port               int     `yaml:"port"`
	LogLevel           string  `yaml:"log_level"`
	AdminPassword      string  `yaml:"admin_password"`
	LogRetentionDays   int     `yaml:"log_retention_days"`
	LogDir             string  `yaml:"log_dir"` // default "data/logs"
	MarkupRatio        float64 `yaml:"markup_ratio"`
	BreakerMax         int     `yaml:"breaker_max_failures"`
	BreakerResetMs     int     `yaml:"breaker_reset_timeout_ms"`
	AlertCooldownSec   int     `yaml:"alert_cooldown_sec"`
	MaxLogSubscribers  int     `yaml:"max_log_subscribers"`   // 0 = unlimited
	StreamTimeoutSec   int     `yaml:"stream_timeout_sec"`    // 0 = disable streaming timeout
	StreamMaxBodyBytes int     `yaml:"stream_max_body_bytes"` // soft cap on bytes sent to client

	// RequestTimeoutSec is the per-upstream-call timeout for
	// non-streaming requests. Default 60. Set to 0 to disable
	// (uses chi global 120s timeout).
	RequestTimeoutSec int `yaml:"request_timeout_sec"`

	// MaxRetries is the maximum number of retry attempts for
	// failed upstream calls (5xx, timeout, 429). Default 0
	// (retries disabled). LiteLLM/Portkey default to 5.
	MaxRetries int `yaml:"max_retries"`

	// RetryBaseDelayMs is the base delay for exponential backoff.
	// Actual delay = base * 2^attempt, capped at 30s. Default 500.
	RetryBaseDelayMs int `yaml:"retry_base_delay_ms"`

	// AllowDefaultAdminPassword, when true, lets the gateway
	// start with the well-known admin/admin credential. Off by
	// default so production deployments refuse to bootstrap
	// with a publicly known root password. The flag exists for
	// fresh-install smoke tests and CI.
	AllowDefaultAdminPassword bool `yaml:"allow_default_admin_password"`

	// TrustProxyHeaders makes clientIP() honour X-Forwarded-For
	// and X-Real-IP. Off by default so a direct-internet
	// deployment can't be spoofed into bypassing the per-token
	// IP whitelist. Set this when fronting llmRx with a known
	// reverse proxy (nginx, traefik, ELB, ...).
	TrustProxyHeaders bool `yaml:"trust_proxy_headers"`

	// TrustedProxyCIDRs, when TrustProxyHeaders is true, narrows
	// the set of source IPs allowed to set proxy headers. An
	// empty list with TrustProxyHeaders=true means "trust
	// every source" — keep that opt-in so misconfiguration
	// can't silently widen the trust boundary.
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`

	// CORSAllowedOrigins is the list of origins the gateway
	// will echo back in Access-Control-Allow-Origin. An empty
	// list disables CORS entirely (no Access-Control-Allow-Origin
	// header is sent at all). The legacy "*" wildcard is
	// supported for dev workflows; production deployments
	// should pin specific origins.
	CORSAllowedOrigins []string `yaml:"cors_allowed_origins"`
}

type StrategyConfig struct {
	CostStrategy string `yaml:"cost_strategy"` // cheapest | fastest | balanced
}

type ChannelConfig struct {
	Name           string   `yaml:"name"`
	Provider       string   `yaml:"provider"`
	Protocol       string   `yaml:"protocol"` // openai | anthropic | gemini; default openai
	BaseURL        string   `yaml:"base_url"`
	Keys           []string `yaml:"keys"`
	Models         []string `yaml:"models"`
	Priority       int      `yaml:"priority"`
	InputPrice     float64  `yaml:"input_price_per_1m"`
	OutputPrice    float64  `yaml:"output_price_per_1m"`
	MaxFailures    int      `yaml:"max_failures"`
	ResetTimeoutMs int      `yaml:"reset_timeout_ms"`
}

// ProviderConfig lets operators override built-in providers or add
// new ones via config.yml. Same shape as the built-in providers.yaml.
type ProviderConfig struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Protocol    string `yaml:"protocol"`
	BaseURL     string `yaml:"base_url"`
}

type TokenConfig struct {
	Key    string   `yaml:"key"`
	Name   string   `yaml:"name"`
	Models []string `yaml:"models"`
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

// SecretsConfig controls the at-rest encryption of channel API keys
// (added in P0). KeyMasterEnv names the env var holding the 32-byte
// hex master key. Empty value falls back to LLMRX_KEY_MASTER. If
// the env var is missing, the gateway refuses to start — there is
// no plaintext fallback in production. For local dev only, set
// DEV_ALLOW_PLAINTEXT_KEYS=true to skip the requirement (not
// recommended for any non-localhost deployment).
type SecretsConfig struct {
	KeyMasterEnv      string `yaml:"key_master_env"`
	DevAllowPlaintext bool   `yaml:"dev_allow_plaintext_keys"`
}

// BYOKConfig is the (Phase 1.5 reserved) BYOK configuration. The
// feature is not yet implemented; keep Enabled=false. When the
// feature ships, WhitelistIPs and WhitelistEmails will gate which
// callers may present their own upstream key.
type BYOKConfig struct {
	Enabled         bool     `yaml:"enabled"`
	WhitelistIPs    []string `yaml:"whitelist_ips"`
	WhitelistEmails []string `yaml:"whitelist_emails"`
	MaxKeysPerIP    int      `yaml:"max_keys_per_ip"`
	TTLDays         int      `yaml:"ttl_days"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	expanded, err := Expand(string(data))
	if err != nil {
		return nil, fmt.Errorf("config: env interpolation: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
