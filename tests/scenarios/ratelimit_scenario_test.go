package scenarios

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// Scenario: RPM rate limiting triggers 429 after exceeding limit.
func TestScenario_RPMRateLimit(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1", []string{"gpt-4o"}, "sk-key-1")
	app.AddTokenWithLimits("sk-tok-1", "limited", 3, 0, nil)

	for i := 0; i < 3; i++ {
		code, _ := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
		if code != 200 {
			t.Fatalf("request %d: got %d, want 200", i+1, code)
		}
	}

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
	if code != 429 {
		t.Fatalf("4th request: got %d, want 429 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "rate_limited" {
		t.Fatalf("error code: got %s, want rate_limited", c)
	}
}

// Scenario: Token with model whitelist rejects disallowed model.
func TestScenario_ModelWhitelist(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1", []string{"gpt-4o", "gpt-4o-mini"}, "sk-key-1")
	app.AddTokenWithLimits("sk-tok-1", "restricted", 0, 0, []string{"gpt-4o-mini"})

	code, _ := doChat(t, app, "sk-tok-1", "gpt-4o-mini", userMsg("hi"))
	if code != 200 {
		t.Fatalf("allowed model: got %d, want 200", code)
	}

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
	if code != 403 {
		t.Fatalf("disallowed model: got %d, want 403 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "model_not_allowed" {
		t.Fatalf("error code: got %s, want model_not_allowed", c)
	}
}

// Scenario: Missing auth header returns 401.
func TestScenario_NoAuth(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1", []string{"gpt-4o"}, "sk-key-1")

	code, body := doChatNoAuth(t, app, "gpt-4o")
	if code != 401 {
		t.Fatalf("no auth: got %d, want 401 (body=%v)", code, body)
	}
}

// Scenario: Invalid token returns 403.
func TestScenario_InvalidToken(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1", []string{"gpt-4o"}, "sk-key-1")

	code, body := doChat(t, app, "sk-invalid", "gpt-4o", userMsg("hi"))
	if code != 403 {
		t.Fatalf("invalid token: got %d, want 403 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "invalid_token" {
		t.Fatalf("error code: got %s, want invalid_token", c)
	}
}

// Scenario: Plan budget exhaustion returns 402.
func TestScenario_BudgetExhaustion(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, 10.0, 10.0, "sk-key-1")
	app.AddToken("sk-tok-1", "budgeted")
	plan := app.AddPlan("basic", 0.001)
	app.BindTokenToPlan("sk-tok-1", plan.ID)

	// Simulate pre-existing spend that exceeds budget.
	plan.UsedUSD = 0.002
	if err := app.Store.UpdatePlan(plan); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if err := app.Cache.Reload(); err != nil {
		t.Fatalf("Cache.Reload: %v", err)
	}

	_ = model.Plan{}
	code, body := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
	if code != 402 {
		t.Fatalf("budget exceeded: got %d, want 402 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "budget_exceeded" {
		t.Fatalf("error code: got %s, want budget_exceeded", c)
	}
}
