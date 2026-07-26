package admin_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAdmin_UpdateToken(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddToken("sk-upd", "upd-test")

	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/1", sess, `{"rpm":120,"tpm":50000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}

	toks, _ := app.Store.GetTokens()
	if len(toks) != 1 || toks[0].RPM != 120 || toks[0].TPM != 50000 {
		t.Fatalf("token not updated: %+v", toks)
	}
}

func TestAdmin_UpdateToken_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/999", sess, `{"rpm":60}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAdmin_UpdateToken_BadBody(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddToken("sk-bad", "bad")
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/1", sess, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdmin_UpdateToken_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/abc", sess, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdmin_AnalyticsByToken(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-token?limit=5", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data") {
		t.Fatalf("response should contain 'data': %s", rec.Body.String())
	}
}

func TestAdmin_DeleteKey(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key-to-delete")

	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete key: %d %s", rec.Code, rec.Body.String())
	}

	keys, _ := app.Store.GetKeys(1)
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestAdmin_DeleteKey_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdmin_DeleteUser_DefaultAdminProtected(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/1", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for default admin, got %d", rec.Code)
	}
}

func TestAdmin_DeleteUser_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/999", sess, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAdmin_GetConfig(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/config", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cost_strategy") {
		t.Fatalf("response should contain cost_strategy: %s", rec.Body.String())
	}
}

func TestAdmin_EffectiveConfig(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/effective", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "runtime") {
		t.Fatalf("response should contain runtime: %s", rec.Body.String())
	}
}
