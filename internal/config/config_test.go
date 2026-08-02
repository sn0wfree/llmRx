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

func TestLoad_AutoRouterConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	yaml := `
server:
  port: 9000

auto_router:
  tier_thresholds: [0.2, 0.5, 0.75]
  llm_classifier:
    enabled: true
    base_url: http://127.0.0.1:9999/v1
    api_key: sk-classifier
    model: classifier-1b
    timeout_sec: 2.5
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ar := cfg.AutoRouter
	if len(ar.TierThresholds) != 3 || ar.TierThresholds[0] != 0.2 || ar.TierThresholds[2] != 0.75 {
		t.Errorf("tier thresholds: %v", ar.TierThresholds)
	}
	lc := ar.LLMClassifier
	if !lc.Enabled || lc.BaseURL != "http://127.0.0.1:9999/v1" || lc.APIKey != "sk-classifier" ||
		lc.Model != "classifier-1b" || lc.TimeoutSec != 2.5 {
		t.Errorf("llm classifier: %+v", lc)
	}
}

func TestLoad_AutoRouterDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte("server:\n  port: 9000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AutoRouter.TierThresholds) != 0 {
		t.Errorf("default thresholds should be empty: %v", cfg.AutoRouter.TierThresholds)
	}
	if cfg.AutoRouter.LLMClassifier.Enabled {
		t.Error("llm classifier should default to disabled")
	}
}

func TestLoad_LogstoreSynchronous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	yaml := `
server:
  port: 9000
  logstore_backend: sqlite
  logstore_synchronous: off
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.LogstoreBackend != "sqlite" || cfg.Server.LogstoreSynchronous != "off" {
		t.Errorf("got backend=%q synchronous=%q", cfg.Server.LogstoreBackend, cfg.Server.LogstoreSynchronous)
	}
}
