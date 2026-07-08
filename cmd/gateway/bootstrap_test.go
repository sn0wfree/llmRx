package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestBootstrapMasterKey_FromEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "llmrx.key")
	const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("TEST_KEY_MASTER", hex64)

	if err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got := os.Getenv("TEST_KEY_MASTER"); got != hex64 {
		t.Errorf("env after bootstrap = %q, want %q", got, hex64)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Errorf("key file should not exist when env is set, stat err = %v", err)
	}
}

func TestBootstrapMasterKey_FromFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "llmrx.key")
	const hex64 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := os.WriteFile(keyFile, []byte(hex64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make sure the env var isn't set for this test.
	t.Setenv("TEST_KEY_MASTER", "")
	os.Unsetenv("TEST_KEY_MASTER")

	if err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got := os.Getenv("TEST_KEY_MASTER"); got != hex64 {
		t.Errorf("env after bootstrap = %q, want %q", got, hex64)
	}
}

func TestBootstrapMasterKey_Generated(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "llmrx.key")
	t.Setenv("TEST_KEY_MASTER", "")
	os.Unsetenv("TEST_KEY_MASTER")

	if err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	got := os.Getenv("TEST_KEY_MASTER")
	if len(got) != 64 {
		t.Errorf("generated key length = %d, want 64", len(got))
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file perm = %o, want 0600", perm)
	}
	// Second call should reuse the persisted file, not regenerate.
	first := got
	if err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile); err != nil {
		t.Fatalf("bootstrap #2: %v", err)
	}
	if os.Getenv("TEST_KEY_MASTER") != first {
		t.Errorf("key changed between calls: %q != %q", os.Getenv("TEST_KEY_MASTER"), first)
	}
}

func TestBootstrapMasterKey_InvalidLength(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEST_KEY_MASTER", "abc")
	if err := bootstrapMasterKey("TEST_KEY_MASTER", filepath.Join(dir, "k")); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestBootstrapMasterKey_InvalidHex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEST_KEY_MASTER", strings.Repeat("z", 64))
	if err := bootstrapMasterKey("TEST_KEY_MASTER", filepath.Join(dir, "k")); err == nil {
		t.Fatal("expected error for non-hex key")
	}
}

func TestBootstrapMasterKey_DefaultEnvName(t *testing.T) {
	dir := t.TempDir()
	const hex64 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	t.Setenv("LLMRX_KEY_MASTER", hex64)

	if err := bootstrapMasterKey("", filepath.Join(dir, "k")); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got := os.Getenv("LLMRX_KEY_MASTER"); got != hex64 {
		t.Errorf("env = %q, want %q", got, hex64)
	}
}

func TestMaybeChownDataDir_NonRootNoop(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test must run as non-root")
	}
	dir := t.TempDir()
	if err := maybeChownDataDir(dir, "root"); err != nil {
		t.Errorf("maybeChownDataDir as non-root should be no-op, got %v", err)
	}
}

func TestRunHealthcheck_Healthy(t *testing.T) {
	// Spin up a tiny HTTP server that returns 200 on /health.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok"))
			c.Close()
		}
	}()
	if rc := runHealthcheck(addr, time.Second); rc != 0 {
		t.Errorf("runHealthcheck on 200 = %d, want 0", rc)
	}
}

func TestRunHealthcheck_Unhealthy(t *testing.T) {
	// Listener that returns 500.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.0 500 Server Error\r\n\r\n"))
			c.Close()
		}
	}()
	if rc := runHealthcheck(addr, time.Second); rc != 1 {
		t.Errorf("runHealthcheck on 500 = %d, want 1", rc)
	}
}

func TestRunHealthcheck_NoListener(t *testing.T) {
	// Pick an address with no listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // free the port — nothing listening now
	if rc := runHealthcheck(addr, 200*time.Millisecond); rc != 1 {
		t.Errorf("runHealthcheck on dead addr = %d, want 1", rc)
	}
}

func TestSeedTokens_WiresModelsWhitelist(t *testing.T) {
	cfg := &config.Config{
		Tokens: []config.TokenConfig{
			{Key: "sk-test-foo", Name: "foo", Models: []string{"deepseek-chat", "deepseek-reasoner"}},
			{Key: "sk-test-bar", Name: "bar"},
		},
	}
	st, err := store.OpenSQLite(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if err := seedTokens(st, cfg); err != nil {
		t.Fatalf("seedTokens: %v", err)
	}
	toks, err := st.GetTokens()
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	var foo, bar *model.Token
	for i := range toks {
		switch toks[i].Key {
		case "sk-test-foo":
			foo = &toks[i]
		case "sk-test-bar":
			bar = &toks[i]
		}
	}
	if foo == nil {
		t.Fatal("token sk-test-foo not found")
	}
	if len(foo.ModelsWhitelist) != 2 || foo.ModelsWhitelist[0] != "deepseek-chat" {
		t.Errorf("foo.ModelsWhitelist = %v, want [deepseek-chat deepseek-reasoner]", foo.ModelsWhitelist)
	}
	if bar == nil {
		t.Fatal("token sk-test-bar not found")
	}
	if len(bar.ModelsWhitelist) != 0 {
		t.Errorf("bar.ModelsWhitelist = %v, want []", bar.ModelsWhitelist)
	}
}

// helpers

var _ = strconv.Itoa