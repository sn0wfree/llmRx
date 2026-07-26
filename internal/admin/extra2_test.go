package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAdmin_ReloadAll(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/reload", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_RotateSecrets_NoManager(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"new_master_key":"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500 (no secrets manager)", rec.Code)
	}
}

func TestAdmin_RotateSecrets_BadKey(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"new_master_key":"short"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_RotateSecrets_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, `{bad`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ListChannels(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var resp struct{ Data []model.Channel `json:"data"` }
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Data) != 1 {
		t.Errorf("expected 1 channel, got %d", len(resp.Data))
	}
}

func TestAdmin_ListKeys(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"}, "sk-test-key-123")
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/keys", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_DeleteKey_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"}, "sk-test-key-123")
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_DeleteKey_BadKeyID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ListAlerts(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_StreamLogs(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil).WithContext(ctx)
	req.Header.Set("X-Session-Token", sess)
	rec := httptest.NewRecorder()
	app.Admin.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsTimeSeries_WithParams(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/timeseries?bucket=3600&limit=50&model=gpt-4&from=1000&to=2000&status_code=200&token_id=1&channel_id=1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_ListPlans(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.Store.CreatePlan(&model.Plan{Name: "pro", MarkupRatio: 1.5, Status: 1})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/plans", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_DeletePlan(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	p := &model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1}
	app.Store.CreatePlan(p)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/plans/"+itoa(p.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_DeletePlan_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/plans/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeletePlan_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/plans/9999", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (idempotent)", rec.Code)
	}
}

func TestAdmin_ChangePassword_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"old_password":"admin","new_password":"newpassword123"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ChangePassword_RootChangesOther(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	hash, _ := authHash("alicepw123")
	u := &model.User{Username: "alice", PasswordHash: hash, Role: model.RoleUser, Status: 1}
	app.Store.CreateUser(u)

	body := `{"new_password":"alicepassword123"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/"+itoa(u.ID)+"/password", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_DeleteUser_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	hash, _ := authHash("alicepw123")
	u := &model.User{Username: "alice", PasswordHash: hash, Role: model.RoleUser, Status: 1}
	app.Store.CreateUser(u)

	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/"+itoa(u.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func authHash(pw string) (string, error) {
	return authHashForTest(pw)
}
