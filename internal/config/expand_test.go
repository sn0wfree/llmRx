package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpand_SubstitutesSimple(t *testing.T) {
	t.Setenv("LLMRX_TEST_FOO", "bar")
	got, err := Expand("hello ${LLMRX_TEST_FOO}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "hello bar" {
		t.Fatalf("got %q, want %q", got, "hello bar")
	}
}

func TestExpand_UnsetIsEmpty(t *testing.T) {
	os.Unsetenv("LLMRX_TEST_UNSET")
	got, err := Expand("x=${LLMRX_TEST_UNSET}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "x=" {
		t.Fatalf("got %q, want %q", got, "x=")
	}
}

func TestExpand_DefaultWithColonDash(t *testing.T) {
	os.Unsetenv("LLMRX_TEST_NOT_SET")
	got, err := Expand("${LLMRX_TEST_NOT_SET:-fallback}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "fallback" {
		t.Fatalf("got %q, want %q", got, "fallback")
	}
	t.Setenv("LLMRX_TEST_NOT_SET", "actual")
	got, err = Expand("${LLMRX_TEST_NOT_SET:-fallback}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "actual" {
		t.Fatalf("got %q, want %q", got, "actual")
	}
}

func TestExpand_EmptyUsesDefault(t *testing.T) {
	t.Setenv("LLMRX_TEST_EMPTY", "")
	got, err := Expand("${LLMRX_TEST_EMPTY:-fallback}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "fallback" {
		t.Fatalf("got %q, want %q", got, "fallback")
	}
}

func TestExpand_RequiredMissingErrors(t *testing.T) {
	os.Unsetenv("LLMRX_TEST_REQUIRED")
	_, err := Expand("${LLMRX_TEST_REQUIRED:?this var is required}")
	if err == nil {
		t.Fatal("expected error for missing required var")
	}
}

func TestExpand_RequiredPresentOK(t *testing.T) {
	t.Setenv("LLMRX_TEST_REQUIRED", "value")
	got, err := Expand("${LLMRX_TEST_REQUIRED:?should not fire}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "value" {
		t.Fatalf("got %q, want %q", got, "value")
	}
}

func TestExpand_AlternateForm(t *testing.T) {
	// ${VAR:+alt} → alt when VAR is set, empty otherwise.
	t.Setenv("LLMRX_TEST_ALT", "1")
	got, _ := Expand("[${LLMRX_TEST_ALT:+yes}]")
	if got != "[yes]" {
		t.Fatalf("set: got %q, want %q", got, "[yes]")
	}
	os.Unsetenv("LLMRX_TEST_ALT")
	got, _ = Expand("[${LLMRX_TEST_ALT:+yes}]")
	if got != "[]" {
		t.Fatalf("unset: got %q, want %q", got, "[]")
	}
}

func TestExpand_DollarDollar(t *testing.T) {
	got, err := Expand("price: $$5")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "price: $5" {
		t.Fatalf("got %q, want %q", got, "price: $5")
	}
}

func TestExpand_BackslashEscape(t *testing.T) {
	got, err := Expand(`literal \${LLMRX_TEST_FOO}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "literal ${LLMRX_TEST_FOO}" {
		t.Fatalf("got %q, want %q", got, "literal ${LLMRX_TEST_FOO}")
	}
}

func TestExpand_UnterminatedDollarBrace(t *testing.T) {
	// Missing closing brace — emit verbatim so the operator sees
	// the bug in their config rather than getting a confusing
	// empty substitution.
	got, err := Expand("hello ${UNCLOSED")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "hello ${UNCLOSED" {
		t.Fatalf("got %q, want %q", got, "hello ${UNCLOSED")
	}
}

func TestExpand_NoDollarIsIdentity(t *testing.T) {
	got, err := Expand("plain text, no vars here")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "plain text, no vars here" {
		t.Fatalf("got %q", got)
	}
}

func TestExpand_MultipleSubstitutions(t *testing.T) {
	t.Setenv("LLMRX_TEST_A", "alpha")
	t.Setenv("LLMRX_TEST_B", "beta")
	got, err := Expand("${LLMRX_TEST_A}/${LLMRX_TEST_B}.log")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "alpha/beta.log" {
		t.Fatalf("got %q", got)
	}
}

// ---------- Load integration ----------

func TestLoad_InterpolatesEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	t.Setenv("LLMRX_TEST_DSN", "sqlite:///test.db")
	t.Setenv("LLMRX_TEST_PWD", "s3cr3t-pwd")

	yaml := `
server:
  port: 8787
  admin_password: ${LLMRX_TEST_PWD}
database:
  driver: sqlite
  dsn: ${LLMRX_TEST_DSN}
secrets:
  dev_allow_plaintext_keys: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.AdminPassword != "s3cr3t-pwd" {
		t.Errorf("AdminPassword = %q, want %q", cfg.Server.AdminPassword, "s3cr3t-pwd")
	}
	if cfg.Database.DSN != "sqlite:///test.db" {
		t.Errorf("DSN = %q", cfg.Database.DSN)
	}
}

func TestLoad_RequiredMissingFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.Unsetenv("LLMRX_TEST_NEEDED")
	yaml := `
server:
  admin_password: ${LLMRX_TEST_NEEDED:?must set the admin password env}
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected Load to fail on required-missing env")
	}
}
