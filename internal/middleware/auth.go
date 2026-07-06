package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const (
	TokenKey  contextKey = "token_key"
	TokenIDKey contextKey = "token_id"
	UserKey   contextKey = "user_key"
)

type errorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func writeAuthError(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := errorBody{}
	body.Error.Message = msg
	body.Error.Type = "invalid_request_error"
	body.Error.Code = code
	_ = json.NewEncoder(w).Encode(body)
}

// TokenInfo is what TokenLookup resolves a bearer token to. The
// caller-side stores the ID under the request context so handlers
// can persist a foreign key without re-querying.
type TokenInfo struct {
	ID   int64
	Key  string
	Name string
}

// TokenLookup resolves a bearer token to its TokenInfo. Returning
// ok=false yields a 403 response.
type TokenLookup func(key string) (TokenInfo, bool)

// Token returns a middleware that resolves tokens through the given
// lookup function (typically backed by an in-memory cache plus the
// store on miss).
func Token(lookup TokenLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing authorization header", "missing_authorization")
				return
			}

			token := strings.TrimPrefix(auth, "Bearer ")
			if token == auth || token == "" {
				writeAuthError(w, http.StatusUnauthorized, "invalid authorization format", "invalid_authorization")
				return
			}

			info, ok := lookup(token)
			if !ok {
				writeAuthError(w, http.StatusForbidden, "invalid token", "invalid_token")
				return
			}

			ctx := context.WithValue(r.Context(), TokenIDKey, info.ID)
			ctx = context.WithValue(ctx, TokenKey, info.Key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AdminOnly checks a session_token via the provided lookup. The
// resolved user is placed in the request context under UserKey.
func AdminOnly(lookup func(session string) (any, bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := r.Header.Get("X-Session-Token")
			if tok == "" {
				tok = readCookie(r, "llmrx_session")
			}
			if tok == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing admin session", "missing_session")
				return
			}
			u, ok := lookup(tok)
			if !ok {
				writeAuthError(w, http.StatusForbidden, "invalid session", "invalid_session")
				return
			}
			ctx := context.WithValue(r.Context(), UserKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}