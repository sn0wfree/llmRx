package config

import (
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
}

type ServerConfig struct {
	Host                string  `yaml:"host"`
	Port                int     `yaml:"port"`
	LogLevel            string  `yaml:"log_level"`
	AdminPassword       string  `yaml:"admin_password"`
	LogRetentionDays    int     `yaml:"log_retention_days"`
	MarkupRatio         float64 `yaml:"markup_ratio"`
	BreakerMax          int     `yaml:"breaker_max_failures"`
	BreakerResetMs      int     `yaml:"breaker_reset_timeout_ms"`
	AlertCooldownSec    int     `yaml:"alert_cooldown_sec"`
	MaxLogSubscribers   int     `yaml:"max_log_subscribers"`     // 0 = unlimited
	StreamTimeoutSec    int     `yaml:"stream_timeout_sec"`      // 0 = disable streaming timeout
	StreamMaxBodyBytes  int     `yaml:"stream_max_body_bytes"`   // soft cap on bytes sent to client
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
	KeyMasterEnv string `yaml:"key_master_env"`
	DevAllowPlaintext bool `yaml:"dev_allow_plaintext_keys"`
}

// BYOKConfig is the (Phase 1.5 reserved) BYOK configuration. The
// feature is not yet implemented; keep Enabled=false. When the
// feature ships, WhitelistIPs and WhitelistEmails will gate which
// callers may present their own upstream key.
type BYOKConfig struct {
	Enabled          bool     `yaml:"enabled"`
	WhitelistIPs     []string `yaml:"whitelist_ips"`
	WhitelistEmails  []string `yaml:"whitelist_emails"`
	MaxKeysPerIP     int      `yaml:"max_keys_per_ip"`
	TTLDays           int      `yaml:"ttl_days"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
