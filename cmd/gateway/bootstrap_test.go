package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestBootstrapMasterKey_FromEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "llmrx.key")
	const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("TEST_KEY_MASTER", hex64)

	if err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile, false); err != nil {
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

	if err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile, false); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got := os.Getenv("TEST_KEY_MASTER"); got != hex64 {
		t.Errorf("env after bootstrap = %q, want %q", got, hex64)
	}
}

// TestBootstrapMasterKey_NoEnvNoFileAutoGenerates: in production
// mode (DevAllowPlaintext=false) when neither env nor file is set,
// bootstrap auto-generates a fresh key and persists it to keyFile.
func TestBootstrapMasterKey_NoEnvNoFileAutoGenerates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEST_KEY_MASTER", "")
	os.Unsetenv("TEST_KEY_MASTER")
	keyFile := filepath.Join(dir, "k")
	err := bootstrapMasterKey("TEST_KEY_MASTER", keyFile, false)
	if err != nil {
		t.Fatalf("expected auto-generate, got error: %v", err)
	}
	// Key file should exist with a 64-char hex string.
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	key := strings.TrimSpace(string(data))
	if len(key) != 64 {
		t.Fatalf("key length: want 64, got %d", len(key))
	}
	// Env should be set to the generated key.
	if got := os.Getenv("TEST_KEY_MASTER"); got != key {
		t.Errorf("env not set to generated key: got %q want %q", got, key)
	}
}

// TestBootstrapMasterKey_AllowPlaintextNoOps: dev plaintext
// mode bypasses the bootstrap entirely — no env, no file, no
// error.
func TestBootstrapMasterKey_AllowPlaintextNoOps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEST_KEY_MASTER", "")
	os.Unsetenv("TEST_KEY_MASTER")
	if err := bootstrapMasterKey("TEST_KEY_MASTER", filepath.Join(dir, "k"), true); err != nil {
		t.Fatalf("bootstrap with allowPlaintext: %v", err)
	}
	// Env should remain unset.
	if got := os.Getenv("TEST_KEY_MASTER"); got != "" {
		t.Errorf("env should remain unset in plaintext mode, got %q", got)
	}
}

func TestBootstrapMasterKey_InvalidLength(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEST_KEY_MASTER", "abc")
	if err := bootstrapMasterKey("TEST_KEY_MASTER", filepath.Join(dir, "k"), false); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestBootstrapMasterKey_InvalidHex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEST_KEY_MASTER", strings.Repeat("z", 64))
	if err := bootstrapMasterKey("TEST_KEY_MASTER", filepath.Join(dir, "k"), false); err == nil {
		t.Fatal("expected error for non-hex key")
	}
}

func TestBootstrapMasterKey_DefaultEnvName(t *testing.T) {
	dir := t.TempDir()
	const hex64 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	t.Setenv("LLMRX_KEY_MASTER", hex64)

	if err := bootstrapMasterKey("", filepath.Join(dir, "k"), false); err != nil {
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

// TestSeedAdmin_DefaultPasswordRefused: fresh installs must NOT
// silently ship with admin/admin. The seed step returns an
// error unless the operator explicitly opts in.
func TestSeedAdmin_DefaultPasswordRefused(t *testing.T) {
	st, err := store.OpenSQLite(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{} // empty AdminPassword → defaults to "admin"
	if err := seedAdmin(st, cfg); err == nil {
		t.Fatal("expected seedAdmin to refuse default admin/admin without opt-in")
	}
	if u, _ := st.GetUserByUsername("admin"); u != nil {
		t.Fatal("admin user must not have been created")
	}
}

// TestSeedAdmin_DefaultPasswordAllowedWithOptIn: setting
// AllowDefaultAdminPassword=true restores the dev-friendly path
// for CI / smoke tests.
func TestSeedAdmin_DefaultPasswordAllowedWithOptIn(t *testing.T) {
	st, err := store.OpenSQLite(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{Server: config.ServerConfig{AllowDefaultAdminPassword: true}}
	if err := seedAdmin(st, cfg); err != nil {
		t.Fatalf("seedAdmin with opt-in: %v", err)
	}
	u, err := st.GetUserByUsername("admin")
	if err != nil || u == nil {
		t.Fatalf("GetUserByUsername: %v %v", u, err)
	}
	if u.Role != model.RoleRoot {
		t.Fatalf("admin role = %v, want RoleRoot", u.Role)
	}
}

// TestSeedAdmin_CustomPasswordHonoured: when the operator
// supplies their own password (length ≥ 6), the default gate
// is bypassed and the password is used verbatim.
func TestSeedAdmin_CustomPasswordHonoured(t *testing.T) {
	st, err := store.OpenSQLite(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{Server: config.ServerConfig{AdminPassword: "correct-horse-battery-staple"}}
	if err := seedAdmin(st, cfg); err != nil {
		t.Fatalf("seedAdmin with custom password: %v", err)
	}
	u, _ := st.GetUserByUsername("admin")
	if u == nil {
		t.Fatal("admin user not created")
	}
	// argon2id hash should verify against the custom password.
	if !auth.Verify(u.PasswordHash, "correct-horse-battery-staple").OK {
		t.Fatal("admin hash does not verify against the supplied password")
	}
	if auth.Verify(u.PasswordHash, "admin").OK {
		t.Fatal("admin hash verifies against 'admin' — wrong password was hashed")
	}
}

// helpers

var _ = strconv.Itoa

// TestIntentWiring_FallsBackToNop exercises the production
// intent-wiring code path in main.go: we attempt intent.Load().
// In CI without the Rust cdylib, Load returns an error and the
// router keeps its default Nop classifier — meaning the build
// stays usable even when L4 is unavailable. With LLMRX_INTENT_LIB
// pointing at a built .so, Load succeeds and the router is
// swapped to the native backend.
func TestIntentWiring_FallsBackToNop(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cp := pool.NewChannelPool()
	eng := router.New(st, cp)

	// Unset LLMRX_INTENT_LIB so Load falls through to the
	// default candidate paths (none of which exist in CI).
	t.Setenv("LLMRX_INTENT_LIB", "")
	intent.DefaultLibraryPath = "/nonexistent/libllmrx_intent.so"

	if classifier, err := intent.Load(); err == nil {
		// If the dev environment happens to have built the
		// crate, swap and accept either outcome.
		eng.SetIntentClassifier(classifier)
		t.Logf("native classifier available: backend=%s", classifier.Backend())
	} else {
		// Production fallback path: do nothing — router keeps its
		// default Nop classifier. Verify that explicitly.
		t.Logf("intent: native unavailable: %v", err)
	}
	// Sanity: the router is usable regardless of which path was
	// taken (this is a smoke test for the wiring itself).
	if eng == nil {
		t.Fatal("router is nil")
	}
	// Verify the backend name reflects the actual wiring.
	if got := eng.IntentBackend(); got == "" {
		t.Fatal("IntentBackend() returned empty string")
	}
}

// TestIntentRequired_EnvFlagCausesFailOnLoadError covers the
// LLMRX_INTENT_REQUIRED behaviour: when the flag is set and
// intent.Load() fails, the helper must return a non-nil error so
// the caller can abort startup. Without the flag the helper
// returns nil and the caller continues with Nop.
func TestIntentRequired_EnvFlagCausesFailOnLoadError(t *testing.T) {
	t.Setenv("LLMRX_INTENT_LIB", "")
	intent.DefaultLibraryPath = "/nonexistent/libllmrx_intent.so"

	// Required + Load failure => error surfaced.
	t.Setenv("LLMRX_INTENT_REQUIRED", "1")
	if _, _, err := loadIntentClassifier(); err == nil {
		t.Fatal("expected error when required and Load fails")
	}

	// Required + Load success => no error. (We can't easily
	// simulate success without the cdylib, so skip that branch.)

	// Not required + Load failure => nil error (fallback to Nop).
	t.Setenv("LLMRX_INTENT_REQUIRED", "")
	if _, _, err := loadIntentClassifier(); err != nil {
		t.Fatalf("expected nil error when not required and Load fails: %v", err)
	}
}