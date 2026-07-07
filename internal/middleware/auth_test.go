package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func passthrough() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

func do(t *testing.T, h http.Handler, hdr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	if hdr != "" {
		req.Header.Set("Authorization", hdr)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuth_MissingHeader(t *testing.T) {
	rec := do(t, Token(func(string) (TokenInfo, bool) { return TokenInfo{}, false })(passthrough()), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuth_InvalidFormat(t *testing.T) {
	cases := []string{"Basic abc", "Bearer", "Bearer "}
	for _, h := range cases {
		rec := do(t, Token(func(string) (TokenInfo, bool) { return TokenInfo{}, true })(passthrough()), h)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%q: expected 401, got %d", h, rec.Code)
		}
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	rec := do(t, Token(func(string) (TokenInfo, bool) { return TokenInfo{}, false })(passthrough()), "Bearer wrong")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestAuth_ValidTokenPassesAndStoresContext(t *testing.T) {
	const want = "sk-test-123"
	var gotID int64
	var gotKey string
	h := Token(func(key string) (TokenInfo, bool) {
		if key != want {
			return TokenInfo{}, false
		}
		return TokenInfo{ID: 42, Key: key, Name: "n"}, true
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(TokenIDKey).(int64); ok {
			gotID = v
		}
		if v, ok := r.Context().Value(TokenKey).(string); ok {
			gotKey = v
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := do(t, h, "Bearer "+want)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotID != 42 {
		t.Fatalf("context TokenID: expected 42, got %d", gotID)
	}
	if gotKey != want {
		t.Fatalf("context TokenKey: expected %q, got %q", want, gotKey)
	}
}

func TestAuth_ErrorBodyShape(t *testing.T) {
	rec := do(t, Token(func(string) (TokenInfo, bool) { return TokenInfo{}, false })(passthrough()), "")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body.Error.Type != "invalid_request_error" || body.Error.Code != "missing_authorization" {
		t.Fatalf("unexpected error body: %+v", body)
	}
}

func TestAdminOnly_NoSession(t *testing.T) {
	rec := do(t, AdminOnly(func(string) (any, bool) { return nil, false })(passthrough()), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAdminOnly_BadSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Session-Token", "bad")
	rec := httptest.NewRecorder()
	AdminOnly(func(string) (any, bool) { return nil, false })(passthrough()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestTokenInfo_HasModelAccess(t *testing.T) {
	empty := TokenInfo{}
	if !empty.HasModelAccess("gpt-4") {
		t.Fatal("empty whitelist = unrestricted")
	}
	restricted := TokenInfo{ModelsWhitelist: []string{"gpt-4", "claude-3"}}
	if !restricted.HasModelAccess("gpt-4") {
		t.Fatal("gpt-4 should be allowed")
	}
	if !restricted.HasModelAccess("claude-3") {
		t.Fatal("claude-3 should be allowed")
	}
	if restricted.HasModelAccess("gemini-pro") {
		t.Fatal("gemini-pro should be denied")
	}
	star := TokenInfo{ModelsWhitelist: []string{"*"}}
	if !star.HasModelAccess("anything") {
		t.Fatal("* should match anything")
	}
}

func TestTokenInfo_HasIPAccess(t *testing.T) {
	empty := TokenInfo{}
	if !empty.HasIPAccess("1.2.3.4") {
		t.Fatal("empty IP whitelist = unrestricted")
	}
	restricted := TokenInfo{IPWhitelist: []string{"1.2.3.4", "10.0.0.0/8"}}
	if !restricted.HasIPAccess("1.2.3.4") {
		t.Fatal("1.2.3.4 should be allowed")
	}
	if !restricted.HasIPAccess("10.0.0.0/8") {
		t.Fatal("10.0.0.0/8 should be allowed (exact-string match)")
	}
	if restricted.HasIPAccess("8.8.8.8") {
		t.Fatal("8.8.8.8 should be denied")
	}
}

func TestWithLimits_BlocksRPM(t *testing.T) {
	enforcer := &fakeEnforcer{allow: false, reason: "rpm exceeded"}
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{ID: 7, Key: "sk-test"}, true
	}
	h := WithLimits(lookup, enforcer)(passthrough())
	rec := do(t, h, "Bearer sk-test")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rpm exceeded") {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestWithLimits_AllowsWhenAllowed(t *testing.T) {
	enforcer := &fakeEnforcer{allow: true}
	lookup := func(string) (TokenInfo, bool) {
		return TokenInfo{ID: 7, Key: "sk-test"}, true
	}
	h := WithLimits(lookup, enforcer)(passthrough())
	rec := do(t, h, "Bearer sk-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestWithLimits_NilEnforcerPassesThrough(t *testing.T) {
	h := WithLimits(func(string) (TokenInfo, bool) { return TokenInfo{}, true }, nil)(passthrough())
	rec := do(t, h, "Bearer sk-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

type fakeEnforcer struct {
	allow  bool
	reason string
}

func (f *fakeEnforcer) Allow(tokenID int64, rpm, tpm int, promptEstimate int) (bool, string) {
	return f.allow, f.reason
}