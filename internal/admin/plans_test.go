package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAdmin_PlansCRUD(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Initially empty.
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/plans", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list empty: %d %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Data []model.Plan `json:"data"`
	}
	decodeJSON(t, rec, &listed)
	if len(listed.Data) != 0 {
		t.Fatalf("expected 0 plans, got %d", len(listed.Data))
	}

	// Create.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess,
		`{"name":"free-tier","budget_usd":10,"markup_ratio":1.5,"status":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created model.Plan
	decodeJSON(t, rec, &created)
	if created.ID == 0 || created.Name != "free-tier" {
		t.Fatalf("create result: %+v", created)
	}
	if created.UsedUSD != 0 {
		t.Fatalf("used_usd should default to 0, got %v", created.UsedUSD)
	}

	// Get by id.
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/plans/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var got model.Plan
	decodeJSON(t, rec, &got)
	if got.Name != "free-tier" {
		t.Fatalf("get: got name %q", got.Name)
	}

	// Update.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/plans/1", sess,
		`{"name":"free-tier-v2","budget_usd":20,"markup_ratio":1.2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var updated model.Plan
	decodeJSON(t, rec, &updated)
	if updated.Name != "free-tier-v2" || updated.BudgetUSD != 20 || updated.MarkupRatio != 1.2 {
		t.Fatalf("update: %+v", updated)
	}

	// List now has 1.
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/plans", sess, "")
	decodeJSON(t, rec, &listed)
	if len(listed.Data) != 1 {
		t.Fatalf("after create: got %d plans", len(listed.Data))
	}

	// Delete.
	rec = do(t, app.Admin.Routes(), http.MethodDelete, "/plans/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/plans", sess, "")
	decodeJSON(t, rec, &listed)
	if len(listed.Data) != 0 {
		t.Fatalf("after delete: got %d plans", len(listed.Data))
	}
}

func TestAdmin_PlansValidation(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Missing name.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess, `{"budget_usd":10}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name: expected 400, got %d", rec.Code)
	}

	// Invalid body.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess, `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: expected 400, got %d", rec.Code)
	}

	// Update nonexistent.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/plans/999", sess, `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update missing: expected 404, got %d", rec.Code)
	}

	// Get nonexistent.
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/plans/999", sess, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing: expected 404, got %d", rec.Code)
	}
}

func TestAdmin_PlansDeleteUnlinksTokens(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Create a plan.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess,
		`{"name":"p1","budget_usd":10,"markup_ratio":1.0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create plan: %d %s", rec.Code, rec.Body.String())
	}
	var p model.Plan
	decodeJSON(t, rec, &p)

	// Create a token bound to it.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/tokens", sess,
		`{"name":"t1","plan_id":1,"rpm":10,"tpm":1000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}

	// Delete the plan.
	rec = do(t, app.Admin.Routes(), http.MethodDelete, "/plans/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete plan: %d %s", rec.Code, rec.Body.String())
	}

	// Token should now have plan_id=0.
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/tokens", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list tokens: %d", rec.Code)
	}
	var tdata struct {
		Data []model.Token `json:"data"`
	}
	decodeJSON(t, rec, &tdata)
	if len(tdata.Data) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tdata.Data))
	}
	if tdata.Data[0].PlanID != 0 {
		t.Fatalf("token.plan_id should be 0 after plan delete, got %d", tdata.Data[0].PlanID)
	}
}

func TestAdmin_ChannelsProtocolPassthrough(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Create with explicit protocol=anthropic.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"ant","provider":"anthropic","protocol":"anthropic","base_url":"https://api.anthropic.com/v1","models":["claude-3-opus"],"status":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create anthropic: %d %s", rec.Code, rec.Body.String())
	}
	var ch model.Channel
	decodeJSON(t, rec, &ch)
	if ch.Protocol != "anthropic" {
		t.Fatalf("create protocol: got %q", ch.Protocol)
	}

	// Default protocol on create is openai.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"def","provider":"openai","base_url":"https://api.openai.com/v1","models":["gpt-4"],"status":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create default: %d %s", rec.Code, rec.Body.String())
	}
	decodeJSON(t, rec, &ch)
	if ch.Protocol != "openai" {
		t.Fatalf("default protocol: got %q", ch.Protocol)
	}

	// Invalid protocol rejected.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"bad","provider":"x","protocol":"bogus","base_url":"https://x","models":["m"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid protocol: expected 400, got %d", rec.Code)
	}

	// Update existing channel's protocol.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess,
		`{"protocol":"gemini"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update protocol: %d %s", rec.Code, rec.Body.String())
	}
	decodeJSON(t, rec, &ch)
	if ch.Protocol != "gemini" {
		t.Fatalf("after update: got %q", ch.Protocol)
	}

	// Invalid update rejected.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess,
		`{"protocol":"made-up"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid update: expected 400, got %d", rec.Code)
	}
}
