package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// The auth status-code contract (P1 unification, RFC 7235):
//
//	401 — credential missing, malformed, unknown, or expired
//	403 — credential VALID but role/permission/IP insufficient
//
// Before the unification the same invalid_token code was returned
// with 403 in TokenWithOptions and 401 in the WithLimits
// defense-in-depth path — clients could not key retry behaviour on
// the status. This test pins one status per failure mode.
func TestAuthContract_StatusMatrix(t *testing.T) {
	now := time.Now()
	good := TokenInfo{ID: 7}
	expired := TokenInfo{ID: 8, ExpiresAt: now.Add(-time.Hour)}
	lookup := func(key string) (TokenInfo, bool) {
		switch key {
		case "sk-good":
			return good, true
		case "sk-expired":
			return expired, true
		default:
			return TokenInfo{}, false
		}
	}

	type tc struct {
		name   string
		header string
		want   int
		code   string
	}
	cases := []tc{
		{"no header", "", http.StatusUnauthorized, "missing_authorization"},
		{"malformed scheme", "Basic abc", http.StatusUnauthorized, "invalid_authorization"},
		{"bare Bearer", "Bearer ", http.StatusUnauthorized, "invalid_authorization"},
		{"unknown token", "Bearer sk-unknown", http.StatusUnauthorized, "invalid_token"},
		{"valid token", "Bearer sk-good", http.StatusOK, ""},
	}

	for _, c := range cases {
		t.Run("token/"+c.name, func(t *testing.T) {
			h := Token(lookup)(okHandler())
			req, _ := http.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
			assertCode(t, rec, c.code)
		})
	}

	// Expired token goes through WithLimits (expiry is enforced there).
	t.Run("token/expired", func(t *testing.T) {
		h := WithLimits(lookup, nil)(okHandler())
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer sk-expired")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (token_expired)", rec.Code)
		}
		assertCode(t, rec, "token_expired")
	})

	// Expiry must be enforced even when no limiter is wired — the
	// documented WithLimits contract ("Enforcer may be nil but expiry
	// is always enforced"). Before the unification a nil enforcer
	// returned the bare Token() chain and skipped the expiry check.
	t.Run("token/expired-no-enforcer", func(t *testing.T) {
		h := WithLimits(lookup, nil)(okHandler())
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer sk-expired")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (expiry must apply without a limiter)", rec.Code)
		}
		assertCode(t, rec, "token_expired")
	})

	// Admin session contract: missing/invalid → 401; role failures
	// (RequireRole/RequirePermission) stay 403 by design.
	t.Run("admin/invalid session", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Session-Token", "bad")
		rec := httptest.NewRecorder()
		AdminOnly(func(string) (any, bool) { return nil, false })(okHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (invalid_session)", rec.Code)
		}
		assertCode(t, rec, "invalid_session")
	})
}

// okHandler marks success with 200 so contract cases are readable.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func assertCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if code == "" {
		return
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Code != code {
		t.Fatalf("error.code = %q, want %q (body %s)", body.Error.Code, code, rec.Body.String())
	}
}
