package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Channels []ChannelConfig `yaml:"channels"`
	Tokens   []TokenConfig   `yaml:"tokens"`
	Database DatabaseConfig  `yaml:"database"`
}

type ServerConfig struct {
	Port          int    `yaml:"port"`
	RateLimit     int    `yaml:"rate_limit"`
	LogLevel      string `yaml:"log_level"`
	AdminPassword string `yaml:"admin_password"`
}

type ChannelConfig struct {
	Name           string   `yaml:"name"`
	Provider       string   `yaml:"provider"`
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
