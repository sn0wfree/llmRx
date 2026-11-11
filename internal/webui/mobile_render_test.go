package webui

import (
	"bytes"
	"strings"
	"testing"
)

// TestMobileUserCenterChrome: the user center (role < 10) renders the
// mobile chrome (top bar + bottom dock + gradient balance hero), while
// admin (role >= 10) keeps the desktop-only layout with no dock.
func TestMobileUserCenterChrome(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	render := func(role int, active string) string {
		data := map[string]any{
			"Body":   "me_body",
			"Title":  "首页 · llmRx",
			"User":   &User{ID: 3, Username: "alice", Role: role},
			"Me":     &meView{Username: "alice", APIKey: "sk-rx-demo", BudgetUSD: 50, UsedUSD: 10, RemainingUSD: 40},
			"Active": active,
		}
		var buf bytes.Buffer
		if err := r.Render(&buf, "me_body", data); err != nil {
			t.Fatalf("render role %d: %v", role, err)
		}
		return buf.String()
	}

	user := render(1, "me")
	for _, want := range []string{
		`aria-label="页面导航"`,         // bottom dock
		`<header class="md:hidden`,    // mobile top bar
		"from-blue-600 to-indigo-700", // gradient balance hero
		`href="/account/topup"`,       // dock link
		`href="/account/usage"`,       // dock link
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user center render missing %q", want)
		}
	}

	admin := render(100, "")
	if strings.Contains(admin, `aria-label="页面导航"`) {
		t.Error("admin render should not contain the mobile dock")
	}
	if strings.Contains(admin, `<header class="md:hidden`) {
		t.Error("admin render should not contain the mobile top bar")
	}
}

// TestMobileLowBalanceWarn: the hero warn strip shows when remaining
// budget is under $1 or usage reaches 90%, and stays hidden otherwise.
func TestMobileLowBalanceWarn(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	render := func(budget, used float64) string {
		data := map[string]any{
			"Body":   "me_body",
			"Title":  "首页 · llmRx",
			"User":   &User{ID: 3, Username: "alice", Role: 1},
			"Me":     &meView{Username: "alice", APIKey: "sk-rx-demo", BudgetUSD: budget, UsedUSD: used, RemainingUSD: budget - used},
			"Active": "me",
		}
		var buf bytes.Buffer
		if err := r.Render(&buf, "me_body", data); err != nil {
			t.Fatalf("render: %v", err)
		}
		return buf.String()
	}

	if out := render(50, 46.5); !strings.Contains(out, "余额偏低") {
		t.Error("92.9%% used should trigger the low-balance warn strip")
	}
	if out := render(50, 49.5); !strings.Contains(out, "余额偏低") {
		t.Error("remaining < $1 should trigger the low-balance warn strip")
	}
	if out := render(50, 10); strings.Contains(out, "余额偏低") {
		t.Error("healthy balance should not show the warn strip")
	}
}
