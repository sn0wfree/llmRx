package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Self-service billing endpoints: invite-code registration and
// top-up redemption. Mounted WITHOUT the bearer-token middleware;
// /me/* authenticate via the llmrx_session cookie instead.

// sessionTTL mirrors the webui/admin session lifetime.
const sessionTTL = 24 * time.Hour

// registerLimiter is a tiny per-IP registration throttle (no
// token exists yet at registration time, so the shared limiter
// cannot be used). 5 registrations per IP per hour.
type registerLimiter struct {
	mu    sync.Mutex
	seen  map[string][]time.Time
	limit int
	win   time.Duration
}

// maxLimiterEntries bounds the in-memory map so long-running
// gateways don't leak entries for one-shot IPs.
const maxLimiterEntries = 10000

func newRegisterLimiter(limit int, win time.Duration) *registerLimiter {
	return &registerLimiter{seen: make(map[string][]time.Time), limit: limit, win: win}
}

// SetRegisterLimit replaces the per-IP registration throttle
// (tests / operators). limit<=0 disables throttling.
func (h *Handler) SetRegisterLimit(limit int, win time.Duration) {
	if limit <= 0 {
		h.regLimiter = nil
		return
	}
	h.regLimiter = newRegisterLimiter(limit, win)
}

// SetTokenReloader wires the bearer-token cache refresh (called
// after registration creates a new token).
func (h *Handler) SetTokenReloader(fn func() error) { h.tokenReload = fn }

func (l *registerLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.win)
	ts := l.seen[ip]
	kept := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.seen[ip] = kept
		return false
	}
	l.seen[ip] = append(kept, now)
	// Opportunistically evict stale entries when the map grows.
	if len(l.seen) > maxLimiterEntries {
		for k, v := range l.seen {
			if len(v) == 0 || v[len(v)-1].Before(cutoff) {
				delete(l.seen, k)
			}
		}
	}
	return true
}

// newSessionToken generates a hex session token (same shape as the
// webui/admin sessions).
func newSessionToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// registerRequest is POST /api/v1/register.
type registerRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code"`
}

// Register handles invite-code self-registration: validates the
// invite code, creates the user + dedicated token + plan, issues a
// session cookie (auto-login), and returns the API key.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if h.regLimiter != nil && !h.regLimiter.allow(h.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many registrations, try again later", "rate_limited")
		return
	}
	var body registerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "invalid_body")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.InviteCode = strings.TrimSpace(body.InviteCode)
	if body.Username == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required", "missing_fields")
		return
	}
	if body.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "invite code required", "missing_invite")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters", "weak_password")
		return
	}

	hashed, err := auth.Hash(body.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "hash_failed")
		return
	}

	res, err := h.store.UseInviteCode(r.Context(), body.InviteCode, body.Username, hashed)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrRedeemCodeNotFound):
			writeError(w, http.StatusBadRequest, "invite code not found", "invalid_invite")
		case errors.Is(err, store.ErrRedeemCodeUsed):
			writeError(w, http.StatusBadRequest, "invite code already used", "invite_used")
		case errors.Is(err, store.ErrRedeemCodeExpired):
			writeError(w, http.StatusBadRequest, "invite code expired", "invite_expired")
		case errors.Is(err, store.ErrRedeemWrongKind):
			writeError(w, http.StatusBadRequest, "not an invite code", "wrong_kind")
		case errors.Is(err, store.ErrUsernameTaken):
			writeError(w, http.StatusConflict, "username already taken", "username_taken")
		default:
			writeError(w, http.StatusInternalServerError, "registration failed", "register_failed")
		}
		return
	}

	// Auto-login: issue a session cookie so the user lands on
	// /admin/me already authenticated.
	tok := newSessionToken()
	exp := time.Now().Add(sessionTTL).UTC()
	res.User.SessionToken = tok
	res.User.SessionExp = &exp
	if err := h.store.UpdateUser(res.User); err != nil {
		writeError(w, http.StatusInternalServerError, "session setup failed", "session_failed")
		return
	}
	// The new token must be visible to the bearer middleware. A
	// cache-refresh failure must NOT abort registration: the user,
	// plan and token are already durable in the DB (single
	// transaction), so the key simply becomes usable on the next
	// reload (startup / admin reload). Fail-open: log loudly and
	// complete the registration.
	if h.tokenReload != nil {
		if err := h.tokenReload(); err != nil {
			logging.Warn("billing.register_cache_reload_failed",
				logging.F("username", body.Username),
				logging.F("error", err),
			)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "llmrx_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Expires:  exp,
		MaxAge:   int(sessionTTL.Seconds()),
		SameSite: http.SameSiteStrictMode,
	})

	writeJSON(w, map[string]any{
		"user":       res.User.Username,
		"role":       res.User.Role,
		"api_key":    res.Token.Key,
		"plan":       res.Plan,
		"budget_usd": res.Plan.BudgetUSD,
	})
}

// meResponse is GET /api/v1/me.
type meResponse struct {
	User     *model.User  `json:"user"`
	Token    *model.Token `json:"token"`
	Plan     *model.Plan  `json:"plan"`
	UsedUSD  float64      `json:"used_usd"`
	BudgetUSD float64     `json:"budget_usd"`
}

// Me returns the authenticated user's profile, API key and plan
// budget. Authentication: llmrx_session cookie (same as the webui).
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	u, ok := h.sessionUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required", "not_authenticated")
		return
	}
	me, err := h.userBilling(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "billing_lookup_failed")
		return
	}
	writeJSON(w, me)
}

// redeemRequest is POST /api/v1/me/redeem.
type redeemRequest struct {
	Code string `json:"code"`
}

// Redeem applies a top-up code to the authenticated user's plan
// budget.
func (h *Handler) Redeem(w http.ResponseWriter, r *http.Request) {
	u, ok := h.sessionUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required", "not_authenticated")
		return
	}
	var body redeemRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "invalid_body")
		return
	}
	body.Code = strings.TrimSpace(body.Code)
	if body.Code == "" {
		writeError(w, http.StatusBadRequest, "code required", "missing_code")
		return
	}

	me, err := h.userBilling(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "billing_lookup_failed")
		return
	}
	plan, err := h.store.RedeemTopup(r.Context(), body.Code, me.Plan.ID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrRedeemCodeNotFound):
			writeError(w, http.StatusBadRequest, "code not found", "invalid_code")
		case errors.Is(err, store.ErrRedeemCodeUsed):
			writeError(w, http.StatusBadRequest, "code already used", "code_used")
		case errors.Is(err, store.ErrRedeemCodeExpired):
			writeError(w, http.StatusBadRequest, "code expired", "code_expired")
		case errors.Is(err, store.ErrRedeemWrongKind):
			writeError(w, http.StatusBadRequest, "not a top-up code", "wrong_kind")
		default:
			writeError(w, http.StatusInternalServerError, "redemption failed", "redeem_failed")
		}
		return
	}
	writeJSON(w, map[string]any{
		"ok":         true,
		"budget_usd": plan.BudgetUSD,
		"used_usd":   plan.UsedUSD,
	})
}

// sessionUser resolves the authenticated user from the cookie.
func (h *Handler) sessionUser(r *http.Request) (*model.User, bool) {
	c, err := r.Cookie("llmrx_session")
	if err != nil || c.Value == "" {
		return nil, false
	}
	u, err := h.store.GetUserBySession(c.Value)
	if err != nil || u == nil || u.Status != 1 {
		return nil, false
	}
	return u, true
}

// userBilling resolves the user's dedicated token and plan (the
// token created at registration carries the plan). Returns a
// partial response when no token exists (admin-created users).
func (h *Handler) userBilling(u *model.User) (*meResponse, error) {
	tokens, err := h.store.GetTokens()
	if err != nil {
		return nil, err
	}
	var tok *model.Token
	for i := range tokens {
		if tokens[i].Name == u.Username && tokens[i].PlanID > 0 {
			tok = &tokens[i]
			break
		}
	}
	out := &meResponse{User: u}
	if tok == nil {
		return out, nil
	}
	plan, err := h.store.GetPlan(tok.PlanID)
	if err != nil {
		return nil, err
	}
	out.Token = tok
	out.Plan = plan
	out.UsedUSD = plan.UsedUSD
	out.BudgetUSD = plan.BudgetUSD
	return out, nil
}
