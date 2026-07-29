package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/rbac"
)

type contextKey string

const (
	TokenKey    contextKey = "token_key"
	TokenIDKey  contextKey = "token_id"
	TokenInfoKey contextKey = "token_info"
	UserKey     contextKey = "user_key"
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
//
// RPM / TPM are the per-minute / per-token limits configured on the
// token row (0 = unlimited). ModelWhitelist is the list of models
// the token is allowed to call (empty = any). PlanID ties the token
// to a billing Plan whose markup_ratio applies on top of the channel
// markup. PlanBudgetUSD / PlanUsedUSD are the plan's billing
// budget and accumulated usage snapshot taken at Reload time
// (refreshed asynchronously by the store on write).
//
// ExpiresAt is the absolute expiry timestamp. A zero value means
// the token never expires.
type TokenInfo struct {
	ID              int64
	Key             string
	Name            string
	PlanID          int64
	RPM             int
	TPM             int
	ModelsWhitelist []string
	IPWhitelist     []string
	ExpiresAt       time.Time
	PlanBudgetUSD   float64
	PlanUsedUSD     float64
	// PlanMarkupRatio is loaded alongside the budget during cache
	// reload so billedCost() doesn't hit the store on every request.
	// 0 (unset) means "no markup" — the cached value falls through to
	// the same path as a missing plan.
	PlanMarkupRatio float64
	// ComboModels maps virtual model names to their combo definitions.
	// Populated by tokencache.Reload() from the token_combo_models
	// table. If a request's model field matches a key here, the
	// combo routing path is taken instead of direct L1-L5.
	ComboModels map[string]model.TokenComboModel
}

// HasModelAccess returns true if the requested model is allowed by
// the token's whitelist. Empty whitelist = no restriction. Combo
// model names are treated as valid entries regardless of the
// whitelist — the combo definition itself constrains which real
// models can be reached.
func (t TokenInfo) HasModelAccess(model string) bool {
	if model == "auto" {
		return true
	}
	if _, isCombo := t.ComboModels[model]; isCombo {
		return true
	}
	if len(t.ComboModels) > 0 {
		// Token has at least one model set — permission is scoped
		// to enabled sets only. Whitelist is supplemental (a
		// token may also whitelist a model name that doesn't
		// appear in any combo's models list).
		for _, c := range t.ComboModels {
			if !c.Enabled {
				continue
			}
			for _, m := range c.Models {
				if m == model {
					return true
				}
			}
		}
		for _, m := range t.ModelsWhitelist {
			if m == model || m == "*" {
				return true
			}
		}
		return false
	}
	if len(t.ModelsWhitelist) == 0 {
		return true
	}
	for _, m := range t.ModelsWhitelist {
		if m == model || m == "*" {
			return true
		}
	}
	return false
}

// HasIPAccess returns true if the request IP is allowed by the
// token's IP whitelist. Empty whitelist = no restriction.
func (t TokenInfo) HasIPAccess(ip string) bool {
	if len(t.IPWhitelist) == 0 {
		return true
	}
	for _, w := range t.IPWhitelist {
		if w == ip || w == "*" {
			return true
		}
	}
	return false
}

// TokenLookup resolves a bearer token to its TokenInfo. Returning
// ok=false yields a 403 response.
type TokenLookup func(key string) (TokenInfo, bool)

// UnknownTokenHook is invoked when a request presents a bearer
// token that the TokenLookup didn't recognise. The Phase 1.5 BYOK
// path uses this hook to (a) detect an upstream-provider key by
// prefix, (b) verify it via the upstream's test endpoint, (c)
// auto-create a (BYOK) channel row, and (d) proceed with the
// request using the consumer's key. When the hook is nil the
// default 403 response is returned. The hook must be safe for
// concurrent use.
type UnknownTokenHook func(w http.ResponseWriter, r *http.Request, rawKey string)

// Token returns a middleware that resolves tokens through the given
// lookup function (typically backed by an in-memory cache plus the
// store on miss).
func Token(lookup TokenLookup) func(http.Handler) http.Handler {
	return TokenWithOptions(lookup, nil)
}

// TokenWithOptions is the same as Token but lets the caller install
// an UnknownTokenHook for future BYOK use. Phase 1.5 reserved.
func TokenWithOptions(lookup TokenLookup, onUnknown UnknownTokenHook) func(http.Handler) http.Handler {
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
				if onUnknown != nil {
					onUnknown(w, r, token)
					return
				}
				writeAuthError(w, http.StatusForbidden, "invalid token", "invalid_token")
				return
			}

			ctx := context.WithValue(r.Context(), TokenIDKey, info.ID)
			ctx = context.WithValue(ctx, TokenKey, info.Key)
			ctx = context.WithValue(ctx, TokenInfoKey, info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// LimitEnforcer decides whether a request is allowed under the
// token's per-minute / per-token rate limits and the plan's USD
// budget. The implementation is injected by the caller so
// middleware stays storage-free.
//
// budgetUSD == 0 means the plan has no configured budget
// (unlimited). usedUSD is the plan's running total as of the last
// cache reload. estimatedCostUSD is the operator's guess for the
// upcoming request — usually 0 in the middleware (we can't know
// the real cost up-front) and refined after the upstream response
// in api/router.go. When estimatedCostUSD > 0 and the resulting
// total would exceed budgetUSD, the enforcer returns
// reason="budget exceeded" so the middleware can return 402.
type LimitEnforcer interface {
	Allow(tokenID int64, rpm, tpm int, promptEstimate int, budgetUSD, usedUSD, estimatedCostUSD float64) (allowed bool, reason string)
}

// WithLimits wraps Token() with RPM/TPM + token-expiry + plan
// budget enforcement. Enforcer may be nil (limits ignored) but
// expiry is always enforced when TokenInfo.ExpiresAt is set.
func WithLimits(lookup TokenLookup, enforcer LimitEnforcer) func(http.Handler) http.Handler {
	return WithLimitsAndOptions(lookup, enforcer, nil)
}

// WithLimitsAndOptions is the same as WithLimits but also accepts
// an UnknownTokenHook for Phase 1.5 BYOK. The hook is invoked when
// the token is not in the cache; the default 403 path is taken if
// hook is nil.
func WithLimitsAndOptions(lookup TokenLookup, enforcer LimitEnforcer, onUnknown UnknownTokenHook) func(http.Handler) http.Handler {
	base := TokenWithOptions(lookup, onUnknown)
	if enforcer == nil {
		return base
	}
	return func(next http.Handler) http.Handler {
		return base(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info, _ := lookup(extractBearer(r))
			if info.ID == 0 {
				next.ServeHTTP(w, r)
				return
			}
			if !info.ExpiresAt.IsZero() && time.Now().After(info.ExpiresAt) {
				writeAuthError(w, http.StatusForbidden, "token expired", "token_expired")
				return
			}
			// The middleware can't know the prompt size yet; it
			// accounts for the request itself (1 unit). Streaming
			// completion usage is accounted later in emitLog.
			ok, reason := enforcer.Allow(info.ID, info.RPM, info.TPM, 1, info.PlanBudgetUSD, info.PlanUsedUSD, 0)
			if !ok {
				status := http.StatusTooManyRequests
				code := "rate_limited"
				if reason == "budget exceeded" {
					status = http.StatusPaymentRequired
					code = "budget_exceeded"
				}
				observability.RecordRateLimitBlock(code)
				writeAuthError(w, status, reason, code)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// AdminOnly checks a session_token via the provided lookup. The
// resolved user is placed in the request context under UserKey.
//
// Lookup order: X-Session-Token header, llmrx_session cookie, then
// the ?session_token= query parameter (needed for EventSource which
// cannot set custom headers).
func AdminOnly(lookup func(session string) (any, bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := r.Header.Get("X-Session-Token")
			if tok == "" {
				tok = readCookie(r, "llmrx_session")
			}
			if tok == "" {
				tok = r.URL.Query().Get("session_token")
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

// RequireRole gates an already-authenticated admin route by the
// caller's User.Role. AdminOnly must run first (or the lookup must
// have placed a *model.User under UserKey) so that UserKey is
// populated; otherwise the request is rejected as forbidden.
//
// Use model.RoleAdmin for routine CRUD (channels, tokens, plans,
// alerts, logs read, analytics). Use model.RoleRoot for
// user-management, secret rotation, runtime config PUT, and global
// reload — operations that can lock out other operators or change
// global behaviour.
func RequireRole(min model.UserRole) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := r.Context().Value(UserKey).(*model.User)
			if u == nil {
				writeAuthError(w, http.StatusUnauthorized, "no session", "missing_session")
				return
			}
			if u.Role < min {
				writeAuthError(w, http.StatusForbidden, "insufficient role", "insufficient_role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequirePermission gates a route by a fine-grained RBAC permission.
// Like RequireRole, it requires a *model.User under UserKey (populated
// by AdminOnly). The user's effective permission set is computed from
// their role + any explicit per-user overrides.
//
// Map a User.Role to its rbac.Role name:
//
//	RoleUser  -> rbac.RoleViewer
//	RoleAdmin -> rbac.RoleAdmin
//	RoleRoot  -> rbac.RoleRoot
//
// (Operator role is reserved for the "operator" UserRole tier if/when
// the gateway exposes one; for now we map RoleUser->viewer.)
func RequirePermission(perm rbac.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := r.Context().Value(UserKey).(*model.User)
			if u == nil {
				writeAuthError(w, http.StatusUnauthorized, "no session", "missing_session")
				return
			}
			role := mapRole(u.Role)
			perms := rbac.Resolve(role, u.Permissions)
			if !rbac.Allow(perms, perm) {
				writeAuthError(w, http.StatusForbidden, "permission denied", "permission_denied")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func mapRole(r model.UserRole) rbac.Role {
	switch r {
	case model.RoleRoot:
		return rbac.RoleRoot
	case model.RoleAdmin:
		return rbac.RoleAdmin
	default:
		return rbac.RoleViewer
	}
}

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}