package webui

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestTokensPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	st.CreateToken(&model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive})

	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenNewForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestTokenCreate_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=tok1&status=1"
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	toks, _ := st.GetTokens()
	if len(toks) != 1 {
		t.Fatalf("expected 1 token, got %d", len(toks))
	}
}

func TestTokenEditForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenEditForm_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/abc/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestTokenAction_Update(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	body := "_method=PUT&name=tok2&status=1"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := st.GetTokenByID(tk.ID)
	if updated.Name != "tok2" {
		t.Errorf("name=%q want tok2", updated.Name)
	}
}

func TestTokenAction_Delete(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	body := "_method=DELETE"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	toks, _ := st.GetTokens()
	if len(toks) != 0 {
		t.Errorf("token should be deleted")
	}
}

func TestTokenDelete_ViaDELETE(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive}
	st.CreateToken(tk)

	req := httptest.NewRequest(http.MethodDelete, "/tokens/"+itoa(tk.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestTokensListPartial_Search(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	st.CreateToken(&model.Token{Key: "sk-a", Name: "alpha", Status: model.TokenActive})
	st.CreateToken(&model.Token{Key: "sk-b", Name: "beta", Status: model.TokenActive})

	req := httptest.NewRequest(http.MethodGet, "/tokens/partial/list?q=alpha", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alpha") {
		t.Errorf("body should contain alpha")
	}
}

func TestTokensHelpPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/help", nil)
	req.Host = "gateway.example.com:8787"
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := []string{
		"Token 调用帮助",
		"sk-&lt;your-token&gt;",
		"/v1/chat/completions",
		"openai",
		"http://gateway.example.com:8787/v1",
		"模型白名单",
		"rate_limited",
	}
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("help page body missing %q", s)
		}
	}
}

func TestTokensHelpPage_HonorsForwardedProto(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/help", nil)
	req.Host = "gateway.example.com:8787"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "https://gateway.example.com:8787/v1") {
		t.Error("help page should render https base URL when behind reverse proxy")
	}
}

func TestTokensListPage_HasHelpLink(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/admin/tokens/help") {
		t.Error("token list page should link to /admin/tokens/help")
	}
}

func TestRequestBaseURL(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		proto      string
		tls        bool
		wantScheme string
	}{
		{"http default", "h:1", "", false, "http"},
		{"https via TLS", "h:1", "", true, "https"},
		{"https via proxy", "h:1", "https", false, "https"},
		{"http via proxy", "h:1", "http", false, "http"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tc.host
			if tc.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			got := requestBaseURL(req)
			want := tc.wantScheme + "://" + tc.host
			if got != want {
				t.Errorf("requestBaseURL: got %q, want %q", got, want)
			}
		})
	}
}

func TestTokenCreate_AutoComboCreated(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	body := "name=tok1&status=1&models_whitelist=gpt-4%0Aclaude-sonnet"
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	toks, _ := st.GetTokens()
	if len(toks) != 1 {
		t.Fatalf("expected 1 token, got %d", len(toks))
	}
	combos, _ := st.GetComboModels(toks[0].ID)
	if len(combos) != 1 {
		t.Fatalf("expected 1 auto combo, got %d", len(combos))
	}
	c := combos[0]
	if c.Name != "auto" {
		t.Errorf("combo name: got %q, want auto", c.Name)
	}
	if c.Mode != model.ComboModeLoadBalance {
		t.Errorf("combo mode: got %q, want load_balance", c.Mode)
	}
	if c.Strategy != model.StrategyBalanced {
		t.Errorf("combo strategy: got %q, want balanced", c.Strategy)
	}
	if len(c.Models) != 2 || c.Models[0] != "gpt-4" || c.Models[1] != "claude-sonnet" {
		t.Errorf("combo models: got %v", c.Models)
	}
	if !c.Enabled {
		t.Error("combo should be enabled")
	}
}

func TestTokenUpdate_AutoComboSynced(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive,
		ModelsWhitelist: []string{"m1", "m2"}}
	st.CreateToken(tk)

	body := "_method=PUT&name=tok1-updated&status=1&models_whitelist=m1%0Am3%0Am4&combo_name=myset&combo_mode=serial&combo_strategy=cheapest"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo after update, got %d", len(combos))
	}
	c := combos[0]
	if c.Name != "myset" {
		t.Errorf("combo name: got %q, want myset", c.Name)
	}
	if c.Mode != model.ComboModeSerial {
		t.Errorf("combo mode: got %q, want serial", c.Mode)
	}
	if c.Strategy != model.StrategyCheapest {
		t.Errorf("combo strategy: got %q, want cheapest", c.Strategy)
	}
	if len(c.Models) != 3 || c.Models[0] != "m1" || c.Models[1] != "m3" || c.Models[2] != "m4" {
		t.Errorf("combo models: got %v", c.Models)
	}
}

func TestTokenUpdate_CreatesComboIfMissing(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	tk := &model.Token{Key: "sk-t1", Name: "tok1", Status: model.TokenActive,
		ModelsWhitelist: []string{"old-a", "old-b"}}
	st.CreateToken(tk)

	body := "_method=PUT&name=tok1&status=1&models_whitelist=new-a%0Anew-b"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo after update, got %d", len(combos))
	}
	c := combos[0]
	if c.Name != "auto" {
		t.Errorf("combo name: got %q, want auto (default)", c.Name)
	}
	if c.Mode != model.ComboModeLoadBalance {
		t.Errorf("combo mode: got %q, want load_balance (default)", c.Mode)
	}
	if c.Strategy != model.StrategyBalanced {
		t.Errorf("combo strategy: got %q, want balanced (default)", c.Strategy)
	}
	if len(c.Models) != 2 || c.Models[0] != "new-a" || c.Models[1] != "new-b" {
		t.Errorf("combo models: got %v", c.Models)
	}
}
