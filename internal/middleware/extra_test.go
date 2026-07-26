package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWithLimitsAndOptions_ExpiredToken(t *testing.T) {
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{ID: 7, ExpiresAt: time.Now().Add(-time.Hour)}, true
	}
	enf := &fakeEnforcer{allow: true}
	h := WithLimitsAndOptions(lookup, enf, nil)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d want 403 (expired)", rec.Code)
	}
}

func TestWithLimitsAndOptions_BudgetExceeded(t *testing.T) {
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{ID: 7}, true
	}
	enf := &fakeEnforcer{allow: false, reason: "budget exceeded"}
	h := WithLimitsAndOptions(lookup, enf, nil)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("code=%d want 402 (budget exceeded)", rec.Code)
	}
}

func TestWithLimitsAndOptions_UnknownTokenHook(t *testing.T) {
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{}, false
	}
	hookCalled := false
	hook := func(w http.ResponseWriter, r *http.Request, token string) {
		hookCalled = true
		w.WriteHeader(http.StatusTeapot)
	}
	h := WithLimitsAndOptions(lookup, nil, hook)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer unknown")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !hookCalled {
		t.Errorf("unknown token hook should be called")
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("code=%d want 418", rec.Code)
	}
}

func TestWithLimitsAndOptions_NonBearerHeader(t *testing.T) {
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{ID: 7}, true
	}
	enf := &fakeEnforcer{allow: true}
	h := WithLimitsAndOptions(lookup, enf, nil)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic abc123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401 (invalid format)", rec.Code)
	}
}

func TestAdminOnly_ValidSessionHeader(t *testing.T) {
	lookup := func(session string) (any, bool) {
		return "user", true
	}
	h := AdminOnly(lookup)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Session-Token", "valid-session")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
}

func TestAdminOnly_ValidSessionCookie(t *testing.T) {
	lookup := func(session string) (any, bool) {
		return "user", true
	}
	h := AdminOnly(lookup)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: "valid-cookie"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (cookie)", rec.Code)
	}
}

func TestAdminOnly_ValidSessionQuery(t *testing.T) {
	lookup := func(session string) (any, bool) {
		return "user", true
	}
	h := AdminOnly(lookup)(passthrough())
	req := httptest.NewRequest(http.MethodGet, "/?session_token=valid-query", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (query param)", rec.Code)
	}
}
