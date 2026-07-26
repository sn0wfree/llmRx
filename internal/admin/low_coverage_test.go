package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAdmin_DeleteAlert_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/alerts/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_AckAlertEvent_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts/events/abc/ack", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_AckAlertEvent_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts/events/9999/ack", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (ack is idempotent)", rec.Code)
	}
}

func TestAdmin_CreateKey_BadChannelID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/abc/keys", sess, `{"key":"sk-test"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateKey_EmptyKey(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/keys", sess, `{"key":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateKey_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/keys", sess, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateUser_EmptyFields(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", sess, `{"username":"","password":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateUser_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", sess, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteChannel_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteToken_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/tokens/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateAlert_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateAlert_EmptyName(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, `{"name":"","type":"cost"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateAlert_EmptyType(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, `{"name":"a","type":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateAlert_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/alerts/abc", sess, `{"name":"a"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateAlert_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/alerts/9999", sess, `{"name":"a"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_UpdateAlert_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, `{"name":"a","type":"error_rate","threshold":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create alert: code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/alerts/1", sess, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateChannel_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/abc", sess, `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateChannel_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/9999", sess, `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_UpdateChannel_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteUser_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ChangePassword_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/abc/password", sess, `{"old_password":"admin","new_password":"newpass123"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ChangePassword_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ChangePassword_ShortPassword(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess, `{"old_password":"admin","new_password":"123"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ChangePassword_TargetNotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/9999/password", sess, `{"old_password":"admin","new_password":"newpass123"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_ListAlertEvents(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts/events?limit=10", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_AnalyticsTopByToken(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-token?limit=5", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
}
