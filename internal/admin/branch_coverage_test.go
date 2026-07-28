package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAdmin_CreateChannel_MissingFields(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, `{"name":"ch1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateChannel_BadProtocol(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"ch1","provider":"x","base_url":"https://x","protocol":"badproto"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateChannel_WithCustomProtocol(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"ch1","provider":"anthropic","base_url":"https://x","protocol":"anthropic"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_CreateChannel_DefaultStatus(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"ch1","provider":"openai","base_url":"https://x","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_CreateToken_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens", sess, `{bad`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateToken_WithExpiry(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"t1","expires_in_days":30,"rpm":100,"tpm":200,"models_whitelist":["gpt-4"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_CreateUser_DuplicateUsername(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"username":"admin","password":"newpass123","role":100}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400 (duplicate)", rec.Code)
	}
}

func TestAdmin_CreateUser_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"username":"alice","password":"alicepw123","role":0}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_UpdateToken_NotFound2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"x"}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/9999", sess, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_UpdateToken_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddToken("sk-t", "t1")
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/1", sess, `{bad`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateChannel_ProtocolValidation(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	body := `{"protocol":"badproto"}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateKey_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/keys", sess, `{"key":"sk-test-key-12345"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_DeleteKey_Success2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"}, "sk-test-key-12345")
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_ReloadAll_WithAlerts(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/reload", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ListChannels_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"}, "sk-test-key")
	app.AddChannel("ch2", "anthropic", "https://y", []string{"claude-3"})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_ListKeys_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"}, "sk-test-key-123", "sk-test-key-456")
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/keys", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ListKeys_BadChannelID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/abc/keys", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ListAlerts_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.Store.CreateAlert(&model.Alert{Name: "a", Type: "error_rate", Threshold: 0.1, Enabled: true})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_ListPlans_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.Store.CreatePlan(&model.Plan{Name: "pro", MarkupRatio: 1.5, Status: 1})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/plans", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsTimeSeries_WithParams2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/timeseries?bucket=3600&limit=50&model=gpt-4&from=1&to=9999999999&status_code=200&token_id=1&channel_id=1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByModel_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.LogStore.Insert(&model.Log{Model: "gpt-4", PromptTokens: 10, StatusCode: 200, RealCostUSD: 1.0})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-model?limit=10", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByChannel_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.LogStore.Insert(&model.Log{ChannelID: 1, Model: "m", PromptTokens: 10, StatusCode: 200})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-channel?limit=10", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_UpdateConfig_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"server":{"port":9090}}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_UpdateConfig_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, `{bad`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteAlert_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	a := &model.Alert{Name: "a", Type: "error_rate", Threshold: 0.1, Enabled: true}
	app.Store.CreateAlert(a)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/alerts/"+itoa(a.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_DeleteKey_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/9999", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (idempotent)", rec.Code)
	}
}

func TestAdmin_ListUsers_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	hash, _ := authHashForTest("alicepw123")
	app.Store.CreateUser(&model.User{Username: "alice", PasswordHash: hash, Role: model.RoleUser, Status: 1})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/users", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_DeleteDefaultAdminProtected2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/1", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400 (protected)", rec.Code)
	}
}

func TestAdmin_UpdatePlan_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	p := &model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1}
	app.Store.CreatePlan(p)
	body := `{"name":"pro-v2","markup_ratio":2.0,"budget_usd":100,"status":1}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/plans/"+itoa(p.ID), sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_UpdatePlan_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/plans/9999", sess, `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_CreatePlan_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"pro","markup_ratio":1.5,"budget_usd":100,"status":1}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_CreatePlan_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess, `{bad`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ListAlertEvents_WithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	a := &model.Alert{Name: "a", Type: "error_rate", Threshold: 0.1, Enabled: true}
	app.Store.CreateAlert(a)
	app.Store.CreateAlertEvent(&model.AlertEvent{AlertID: a.ID, AlertName: "a", FiredAt: timeNow()})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts/events?limit=10", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AckAlertEvent_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	a := &model.Alert{Name: "a", Type: "error_rate", Threshold: 0.1, Enabled: true}
	app.Store.CreateAlert(a)
	e := &model.AlertEvent{AlertID: a.ID, AlertName: "a", FiredAt: timeNow()}
	app.Store.CreateAlertEvent(e)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts/events/"+itoa(e.ID)+"/ack", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_RotateSecrets_InvalidJSON2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, `{bad`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_RotateSecrets_BadHex(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"new_master_key":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}
