package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	yaml := `
server:
  port: 9000
  rate_limit: 100
  log_level: debug

database:
  driver: sqlite
  dsn: /tmp/test.db

tokens:
  - key: sk-a
    name: a
    models: [foo]

channels:
  - name: deepseek
    provider: deepseek
    base_url: https://api.deepseek.com/v1
    keys: [sk-deepseek]
    models: [deepseek-chat]
    priority: 5
    input_price_per_1m: 0.1
    output_price_per_1m: 0.3
    max_failures: 3
    reset_timeout_ms: 30000
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("Server.Port: got %d", cfg.Server.Port)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("Database.Driver: got %s", cfg.Database.Driver)
	}
	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Key != "sk-a" {
		t.Errorf("Tokens: got %+v", cfg.Tokens)
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0].Name != "deepseek" {
		t.Errorf("Channels: got %+v", cfg.Channels)
	}
	if cfg.Channels[0].MaxFailures != 3 || cfg.Channels[0].ResetTimeoutMs != 30000 {
		t.Errorf("breaker cfg: got %+v", cfg.Channels[0])
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load("/tmp/does-not-exist-llmrx.yml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yml")
	if err := os.WriteFile(path, []byte("server: : :"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected YAML parse error")
	}
}