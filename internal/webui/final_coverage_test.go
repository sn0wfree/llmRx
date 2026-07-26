package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestEscapeHTML_AllChars(t *testing.T) {
	got := escapeHTML(`<script>"a" & 'b'</script>`)
	if got == "" {
		t.Errorf("escapeHTML should return non-empty")
	}
}

func TestEscapeHTML_Empty(t *testing.T) {
	if escapeHTML("") != "" {
		t.Errorf("empty should return empty")
	}
}

func TestEscapeHTML_NoSpecial(t *testing.T) {
	if escapeHTML("hello world") != "hello world" {
		t.Errorf("no special chars should be unchanged")
	}
}

func TestTriggerReload_WithBridge(t *testing.T) {
	_, st := newTestWebUI(t)
	bridge := NewWebAPIBridge(st)
	bridge.SetReloader(func() error {
		return nil
	})
	h, _ := newTestWebUI(t)
	h.adminH = bridge
	h.triggerReload()
}

func TestTokenCreate_DuplicateKey(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=tok1&status=1"
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	toks, _ := st.GetTokens()
	if len(toks) != 1 {
		t.Fatalf("expected 1 token, got %d", len(toks))
	}
}

func TestTokenCreate_WithExpiry(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=tok1&status=1&expires_in_days=30"
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	toks, _ := st.GetTokens()
	if len(toks) != 1 || toks[0].ExpiresAt.IsZero() {
		t.Errorf("token should have expiry set")
	}
}

func TestTokenCreate_Disabled(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=tok1&status=0"
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	toks, _ := st.GetTokens()
	if len(toks) != 1 || toks[0].Status != model.TokenDisabled {
		t.Errorf("token should be disabled")
	}
}

func TestTokenUpdate_ClearExpiry(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	body := "_method=PUT&name=tok2&status=1&expires_in_days=0"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestLogsPage_WithData(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	st.CreateLog(&model.Log{Model: "gpt-4", PromptTokens: 10, StatusCode: 200})

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAlertsPage_WithData(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	st.CreateAlert(&model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, Enabled: true})

	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAnalyticsPage_WithData(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	st.CreateLog(&model.Log{Model: "gpt-4", ChannelID: 1, PromptTokens: 10, StatusCode: 200, RealCostUSD: 1.0})

	req := httptest.NewRequest(http.MethodGet, "/analytics", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestPlansPage_WithData(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	st.CreatePlan(&model.Plan{Name: "pro", MarkupRatio: 1.5, Status: 1})

	req := httptest.NewRequest(http.MethodGet, "/plans", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pro") {
		t.Errorf("body should contain plan name")
	}
}

func TestUsersPage_WithData(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin") {
		t.Errorf("body should contain admin user")
	}
}

func TestConfigSave_SuccessWithFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	_, st := newTestWebUI(t)
	h2, _ := New(st, nil, cfgPath)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "yaml=server:\n  port: 9090"
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h2.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestEffectivePage_WithConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	osWriteFile(cfgPath, []byte("server_port: 8787\nmax_connections: 10\nserver.host: localhost\n"))

	_, st := newTestWebUI(t)
	h2, _ := New(st, nil, cfgPath)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/effective", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h2.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestChannelCreate_WithStatus(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=ch1&provider=openai&base_url=https://x&models=gpt-4&priority=5&input_price=1&output_price=2&status=0"
	req := httptest.NewRequest(http.MethodPost, "/channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	chs, _ := st.GetChannels()
	if len(chs) != 1 || chs[0].Status != model.ChannelDisabled {
		t.Errorf("channel should be disabled")
	}
}

func TestChannelEditForm_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/channels/9999/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestPlanCreate_WithZeroMarkup(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=pro&markup_ratio=0&status=1"
	req := httptest.NewRequest(http.MethodPost, "/plans", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	plans, _ := st.GetPlans()
	if len(plans) != 1 || plans[0].MarkupRatio != 1.0 {
		t.Errorf("markup should default to 1.0, got %v", plans[0].MarkupRatio)
	}
}

func TestUserPasswordForm_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/users/9999/password", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}
