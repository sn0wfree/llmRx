package middleware

import (
	"net/http"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestWithLimits_ExpiryBeforeEnforcer pins the ordering fixed in
// ca2c6e3: an expired token is rejected even when the enforcer would
// have allowed it, and the enforcer is never consulted (no
// rate-limit slot consumed for a dead credential).
func TestWithLimits_ExpiryBeforeEnforcer(t *testing.T) {
	called := false
	enf := &countingEnforcer{onCall: func() { called = true }}
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{ID: 9, ExpiresAt: time.Now().Add(-time.Minute)}, true
	}
	h := WithLimits(lookup, enf)(passthrough())
	rec := do(t, h, "Bearer sk-x")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("enforcer must not run for an expired token")
	}
}

// TestWithLimits_NonExpiringTokenAllowed: a token without ExpiresAt
// passes through to the enforcer.
func TestWithLimits_NonExpiringTokenAllowed(t *testing.T) {
	called := false
	enf := &countingEnforcer{onCall: func() { called = true }}
	lookup := func(string) (TokenInfo, bool) { return TokenInfo{ID: 9}, true }
	h := WithLimits(lookup, enf)(passthrough())
	rec := do(t, h, "Bearer sk-x")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !called {
		t.Fatal("enforcer must run for a valid non-expiring token")
	}
}

// TestHasModelAccess_ComboEnabledOnly: a model reachable only through
// a DISABLED combo (and not whitelisted) must be denied; enabling the
// combo makes it reachable. Covers the enabled-combo loop branch.
func TestHasModelAccess_ComboEnabledOnly(t *testing.T) {
	disabled := TokenInfo{ComboModels: map[string]model.TokenComboModel{
		"set": {Name: "set", Enabled: false, Models: []string{"gpt-4o"}},
	}}
	if disabled.HasModelAccess("gpt-4o") {
		t.Fatal("model behind a disabled combo must be denied")
	}

	enabled := TokenInfo{ComboModels: map[string]model.TokenComboModel{
		"set": {Name: "set", Enabled: true, Models: []string{"gpt-4o"}},
	}}
	if !enabled.HasModelAccess("gpt-4o") {
		t.Fatal("model inside an enabled combo must be allowed")
	}
	// A whitelist entry still works alongside combos.
	disc := TokenInfo{
		ComboModels:     map[string]model.TokenComboModel{"set": {Enabled: true, Models: []string{"a"}}},
		ModelsWhitelist: []string{"b"},
	}
	if !disc.HasModelAccess("b") {
		t.Fatal("whitelist must supplement combo models")
	}
	if disc.HasModelAccess("c") {
		t.Fatal("unlisted, un-comboed model must be denied")
	}
}

type countingEnforcer struct {
	onCall func()
}

func (c *countingEnforcer) Allow(int64, int, int, int, float64, float64, float64) (bool, string) {
	if c.onCall != nil {
		c.onCall()
	}
	return true, ""
}
