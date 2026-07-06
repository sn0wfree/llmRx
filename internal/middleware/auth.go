package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const (
	TokenKey contextKey = "token_key"
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

// Middleware validates Bearer tokens against an in-memory whitelist
// built from cfg.Tokens. Missing/invalid tokens get 401/403 with an
// OpenAI-compatible error body.
func Middleware(validTokens map[string]string) func(http.Handler) http.Handler {
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

			if _, ok := validTokens[token]; !ok {
				writeAuthError(w, http.StatusForbidden, "invalid token", "invalid_token")
				return
			}

			ctx := context.WithValue(r.Context(), TokenKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}