package middleware

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const (
	TokenKey contextKey = "token_key"
)

func Auth(validTokens map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(auth, "Bearer ")
			if token == auth {
				http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			if _, ok := validTokens[token]; !ok {
				http.Error(w, `{"error":"invalid token"}`, http.StatusForbidden)
				return
			}

			ctx := context.WithValue(r.Context(), TokenKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
