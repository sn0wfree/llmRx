package webui

import (
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
