package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func timeHour() time.Duration { return time.Hour }

func TestChannelUpdate_ViaPUT(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	req := httptest.NewRequest(http.MethodPost, "/channels/"+itoa(ch.ID), strings.NewReader("_method=PUT&name=ch2&status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestChannelUpdate_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/channels/9999", strings.NewReader("_method=PUT&name=x&status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestChannelUpdate_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/channels/abc", strings.NewReader("_method=PUT&name=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelAction_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/channels/abc", strings.NewReader("_method=PUT"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelKeysPage_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/channels/abc/keys", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelKeysPage_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/channels/9999/keys", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestChannelKeyCreate_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/channels/abc/keys", strings.NewReader("key=sk-test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelKeyDelete_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodDelete, "/channels/1/keys/abc", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestTokenUpdate_ViaPUT(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader("_method=PUT&name=tok2&status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestTokenUpdate_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/tokens/9999", strings.NewReader("_method=PUT&name=x&status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestTokenAction_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/tokens/abc", strings.NewReader("_method=PUT"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestTokenAction_BadMethod(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader("_method=PATCH"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d want 405", rec.Code)
	}
}

func TestTokenDelete_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodDelete, "/tokens/abc", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestPlanAction_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/plans/abc", strings.NewReader("_method=PUT"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestPlanAction_BadMethod(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	p := &model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1}
	st.CreatePlan(p)

	req := httptest.NewRequest(http.MethodPost, "/plans/"+itoa(p.ID), strings.NewReader("_method=PATCH"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d want 405", rec.Code)
	}
}

func TestAlertEditForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	a := &model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, WindowSec: 300, CooldownSec: 300, Enabled: true}
	st.CreateAlert(a)

	req := httptest.NewRequest(http.MethodGet, "/alerts/"+itoa(a.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAlertAction_Update(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	a := &model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, WindowSec: 300, CooldownSec: 300, Enabled: true}
	st.CreateAlert(a)

	body := "_method=PUT"
	req := httptest.NewRequest(http.MethodPost, "/alerts/"+itoa(a.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code=%d want 501 (stub)", rec.Code)
	}
}

func TestLoadEffectiveYAML_WithFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("server_port: 8787\nmax_connections: 10\nserver.host: localhost\n"), 0644)
	_, st := newTestWebUI(t)
	out, err := loadEffectiveYAML(cfgPath, st)
	if err != nil {
		t.Fatalf("loadEffectiveYAML: %v", err)
	}
	if len(out) == 0 {
		t.Errorf("expected non-empty map")
	}
}

func TestLoadEffectiveYAML_FileNotFound(t *testing.T) {
	_, st := newTestWebUI(t)
	out, err := loadEffectiveYAML("/nonexistent/path.yaml", st)
	if err == nil {
		t.Errorf("expected error for nonexistent file")
	}
	if len(out) != 0 {
		t.Errorf("expected empty map on error")
	}
}

func TestSessionMiddleware_ValidSession(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (valid session)", rec.Code)
	}
}

func TestSessionMiddleware_ExpiredSession(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := newSessionToken()
	admin.SessionToken = tok
	exp := nowAdd(-1 * timeHour())
	admin.SessionExp = &exp
	st.UpdateUser(admin)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303 (expired -> redirect)", rec.Code)
	}
}

func TestConfigSave_Error(t *testing.T) {
	_, st := newTestWebUI(t)
	// Pass an unwritable path to trigger error
	h2, err := New(st, nil, "/nonexistent/dir/config.yaml")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "yaml=test"
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h2.Routes().ServeHTTP(rec, req)
	// Should render error page, not crash
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

var _ store.Store
var _ = os.WriteFile
