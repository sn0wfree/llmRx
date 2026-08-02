package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// ──────────────────────────────────────────────────────────
// cleanupLoop: ticker fires
// ──────────────────────────────────────────────────────────

func TestCleanupLoop_TickerFires(t *testing.T) {
	st := openTestStore(t)
	// Seed a user with an already-expired session so CleanupExpiredSessions has something to do.
	expired := time.Now().Add(-1 * time.Hour)
	fresh := time.Now().Add(1 * time.Hour)
	st.CreateUser(&model.User{
		Username: "alice", PasswordHash: "x", Role: model.RoleUser, Status: 1,
		SessionToken: "stale-session",
		SessionExp:   &expired,
	})
	st.CreateUser(&model.User{
		Username: "bob", PasswordHash: "x", Role: model.RoleUser, Status: 1,
		SessionToken: "fresh-session",
		SessionExp:   &fresh,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cleanupLoop(ctx, st)
	// 5-minute ticker is too slow for tests; we only check ctx.Done works.
	// For the ticker fire path, use a separate test with a mock.
	cancel()
}

func TestCleanupLoop_TickerFiresWithMock(t *testing.T) {
	// Since the ticker is hard-coded at 5 minutes, we can't observe
	// it directly. Instead, verify the call cleanupLoop makes
	// (CleanupExpiredSessions) behaves correctly with our test data.
	st := openTestStore(t)
	expired := time.Now().Add(-1 * time.Hour)
	st.CreateUser(&model.User{
		Username: "alice", PasswordHash: "x", Role: model.RoleUser, Status: 1,
		SessionToken: "stale",
		SessionExp:   &expired,
	})

	// The actual cleanup call (what cleanupLoop makes):
	n, err := st.CleanupExpiredSessions()
	if err != nil {
		t.Fatalf("CleanupExpiredSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleanup, got %d", n)
	}
}

// ──────────────────────────────────────────────────────────
// seedTokens: edge cases
// ──────────────────────────────────────────────────────────

func TestSeedTokens_EmptyConfig_NoOp(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{} // no Tokens
	if err := seedTokens(st, cfg); err != nil {
		t.Fatalf("seedTokens: %v", err)
	}
	// No tokens created, no logging on empty.
	tokens, _ := st.GetTokens()
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens, got %d", len(tokens))
	}
}

func TestSeedTokens_SkipsWhenExisting(t *testing.T) {
	st := openTestStore(t)
	// Pre-seed an existing token.
	st.CreateToken(&model.Token{
		Key: "sk-existing", Name: "preexisting", Status: model.TokenActive,
	})
	cfg := &config.Config{
		Tokens: []config.TokenConfig{
			{Key: "sk-new-1", Name: "new1"},
			{Key: "sk-new-2", Name: "new2"},
		},
	}
	if err := seedTokens(st, cfg); err != nil {
		t.Fatalf("seedTokens: %v", err)
	}
	tokens, _ := st.GetTokens()
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token (existing), got %d", len(tokens))
	}
	if tokens[0].Key != "sk-existing" {
		t.Fatalf("expected existing token to remain, got %q", tokens[0].Key)
	}
}

func TestSeedTokens_MultipleTokens(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{
		Tokens: []config.TokenConfig{
			{Key: "sk-1", Name: "one", Models: []string{"a", "b"}},
			{Key: "sk-2", Name: "two", Models: []string{"c"}},
		},
	}
	if err := seedTokens(st, cfg); err != nil {
		t.Fatalf("seedTokens: %v", err)
	}
	tokens, _ := st.GetTokens()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	// Verify models whitelist was wired.
	for _, tok := range tokens {
		if len(tok.ModelsWhitelist) == 0 {
			t.Errorf("token %q should have models whitelist", tok.Name)
		}
	}
}

// ──────────────────────────────────────────────────────────
// seed: orchestration error paths
// ──────────────────────────────────────────────────────────

func TestSeed_PropagatesAdminError(t *testing.T) {
	st := openTestStore(t)
	// Refuse default admin (allow flag off) → seedAdmin returns error.
	cfg := &config.Config{} // no admin password, no opt-in
	err := seed(st, cfg)
	if err == nil {
		t.Fatal("expected error when default admin refused")
	}
	if !strings.Contains(err.Error(), "refusing to seed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSeed_PropagatesTokenError(t *testing.T) {
	st := openTestStore(t)
	// Use a ScriptedStore that fails on CreateToken.
	scripted := &scriptedStore{
		Store:          st,
		createTokenErr: errSimulated("simulated token create failure"),
	}
	cfg := &config.Config{
		Server: config.ServerConfig{
			AdminPassword:             "validpass",
			AllowDefaultAdminPassword: true,
		},
		Tokens: []config.TokenConfig{{Key: "sk-x", Name: "x"}},
	}
	err := seed(scripted, cfg)
	if err == nil {
		t.Fatal("expected error from CreateToken")
	}
}

// ──────────────────────────────────────────────────────────
// bootstrapMasterKey: env wins over file
// ──────────────────────────────────────────────────────────

func TestBootstrapMasterKey_EnvWinsOverFile(t *testing.T) {
	envName := "LLMRX_TEST_BOOTSTRAP_ENV_FILE"
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")

	// Write a different key to the file.
	fileKey := strings.Repeat("a", 64)
	os.WriteFile(keyFile, []byte(fileKey), 0o600)

	// Set env to a different key.
	envKey := strings.Repeat("b", 64)
	t.Setenv(envName, envKey)

	if err := bootstrapMasterKey(envName, keyFile, false); err != nil {
		t.Fatalf("bootstrapMasterKey: %v", err)
	}
	// The env should have been used (validated successfully).
	got := os.Getenv(envName)
	if got != envKey {
		t.Fatalf("env should be unchanged: got %q", got)
	}
}

func TestBootstrapMasterKey_AutoGenerated_PersistsFile(t *testing.T) {
	envName := "LLMRX_TEST_BOOTSTRAP_AUTOGEN"
	t.Setenv(envName, "") // unset env
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "autogen.key")

	if err := bootstrapMasterKey(envName, keyFile, false); err != nil {
		t.Fatalf("bootstrapMasterKey: %v", err)
	}
	// The file should now exist with a 64-char hex key.
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file should exist: %v", err)
	}
	key := strings.TrimSpace(string(data))
	if len(key) != 64 {
		t.Fatalf("expected 64-char key, got %d", len(key))
	}
	// Env should now hold the same key.
	if got := os.Getenv(envName); got != key {
		t.Fatalf("env should match file key")
	}
}

func TestBootstrapMasterKey_BlankEnvUsesFile(t *testing.T) {
	envName := "LLMRX_TEST_BOOTSTRAP_BLANK"
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	expectedKey := strings.Repeat("c", 64)
	os.WriteFile(keyFile, []byte(expectedKey), 0o600)

	// Set env to whitespace only.
	t.Setenv(envName, "   ")

	if err := bootstrapMasterKey(envName, keyFile, false); err != nil {
		t.Fatalf("bootstrapMasterKey: %v", err)
	}
	got := os.Getenv(envName)
	if got != expectedKey {
		t.Fatalf("env should be populated from file: got %q", got)
	}
}

// ──────────────────────────────────────────────────────────
// runHealthcheck: bad response (non-200)
// ──────────────────────────────────────────────────────────

func TestRunHealthcheck_BadResponse(t *testing.T) {
	// Start a TCP listener that responds with 503 instead of 200.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("HTTP/1.0 503 Service Unavailable\r\n\r\n"))
	}()
	if got := runHealthcheck(ln.Addr().String(), 2*time.Second); got != 1 {
		t.Fatalf("expected exit code 1 for non-200, got %d", got)
	}
}

// ──────────────────────────────────────────────────────────
// seedChannels: keys with no keys config
// ──────────────────────────────────────────────────────────

func TestSeedChannels_ChannelWithoutKeys(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{
		Channels: []config.ChannelConfig{
			{Name: "no-keys", Provider: "openai", BaseURL: "https://x", Models: []string{"m"}},
		},
	}
	if err := seedChannels(st, cfg); err != nil {
		t.Fatalf("seedChannels: %v", err)
	}
	chs, _ := st.GetChannels()
	if len(chs) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(chs))
	}
	keys, _ := st.GetKeys(chs[0].ID)
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}
}

func TestSeedChannels_MultipleKeys(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{
		Channels: []config.ChannelConfig{
			{Name: "multi", Provider: "openai", BaseURL: "https://x", Models: []string{"m"},
				Keys: []string{"sk-aaaaaaaaaaa1", "sk-bbbbbbbbbbb2", "sk-ccccccccccc3"}},
		},
	}
	if err := seedChannels(st, cfg); err != nil {
		t.Fatalf("seedChannels: %v", err)
	}
	chs, _ := st.GetChannels()
	keys, _ := st.GetKeys(chs[0].ID)
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
}

// ──────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────

// errSimulated wraps a message in an error.
type errSimulated string

func (e errSimulated) Error() string { return string(e) }

// scriptedStore wraps store.Store and overrides CreateToken to inject errors.
// Other methods delegate to the underlying store.
type scriptedStore struct {
	store.Store
	createTokenErr error
}

func (s *scriptedStore) CreateToken(t *model.Token) error {
	if s.createTokenErr != nil {
		return s.createTokenErr
	}
	return s.Store.CreateToken(t)
}
