package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

type Handler struct {
	store      store.Store
	pool       *pool.ChannelPool
	router     *router.RouterEngine
	tokens     *tokencache.Cache
	sessionTTL time.Duration
}

func New(st store.Store, cp *pool.ChannelPool, eng *router.RouterEngine, tc *tokencache.Cache) *Handler {
	return &Handler{store: st, pool: cp, router: eng, tokens: tc, sessionTTL: 24 * time.Hour}
}

// SetSessionTTL overrides the default 24h session lifetime.
func (h *Handler) SetSessionTTL(d time.Duration) { h.sessionTTL = d }

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	r.Post("/login", h.Login)
	r.Post("/logout", h.Logout)

	r.Group(func(r chi.Router) {
		r.Use(middleware.AdminOnly(func(s string) (any, bool) {
			u, err := h.store.GetUserBySession(s)
			if err != nil || u == nil {
				return nil, false
			}
			return u, true
		}))
		r.Get("/dashboard", h.Dashboard)
		r.Get("/channels", h.ListChannels)
		r.Post("/channels", h.CreateChannel)
		r.Put("/channels/{id}", h.UpdateChannel)
		r.Delete("/channels/{id}", h.DeleteChannel)
		r.Get("/channels/{id}/keys", h.ListKeys)
		r.Post("/channels/{id}/keys", h.CreateKey)
		r.Delete("/channels/{id}/keys/{keyId}", h.DeleteKey)
		r.Get("/tokens", h.ListTokens)
		r.Post("/tokens", h.CreateToken)
		r.Delete("/tokens/{id}", h.DeleteToken)
		r.Get("/users", h.ListUsers)
		r.Post("/users", h.CreateUser)
		r.Delete("/users/{id}", h.DeleteUser)
		r.Get("/logs", h.ListLogs)
		r.Get("/analytics/timeseries", h.AnalyticsTimeSeries)
		r.Get("/analytics/by-model", h.AnalyticsByModel)
		r.Get("/analytics/by-channel", h.AnalyticsByChannel)
		r.Get("/analytics/by-token", h.AnalyticsByToken)
	})
	return r
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func pathInt(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, key), 10, 64)
}

// nonNil returns the slice if non-nil, otherwise an empty slice of
// the same element type via interface{} boxing. We accept the small
// type-erasure cost to guarantee `"data":[]` rather than `"data":null`
// in JSON responses.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ---------- auth ----------

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	u, err := h.store.GetUserByUsername(body.Username)
	if err != nil || u == nil || u.Status != 1 {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !verifyPassword(u.PasswordHash, body.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	tok := newSessionToken()
	u.SessionToken = tok
	exp := time.Now().Add(h.sessionTTL).UTC()
	u.SessionExp = &exp
	if err := h.store.UpdateUser(u); err != nil {
		writeErr(w, http.StatusInternalServerError, "persist session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "llmrx_session", Value: tok, Path: "/", HttpOnly: true,
		Expires: exp, MaxAge: int(h.sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"session_token":         tok,
		"session_expires_at":    exp.Format(time.RFC3339),
		"user": map[string]any{
			"id":       u.ID,
			"username": u.Username,
			"role":     u.Role,
		},
	})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	tok := r.Header.Get("X-Session-Token")
	if tok == "" {
		tok = readCookie(r, "llmrx_session")
	}
	if tok != "" {
		if u, err := h.store.GetUserBySession(tok); err == nil && u != nil {
			u.SessionToken = ""
			u.SessionExp = nil
			_ = h.store.UpdateUser(u)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "llmrx_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- dashboard ----------

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.LogStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	chs, _ := h.store.GetChannels()
	tokens, _ := h.store.GetTokens()
	keysByCh := map[int64]int{}
	for _, ch := range chs {
		ks, _ := h.store.GetKeys(ch.ID)
		keysByCh[ch.ID] = len(ks)
	}
	activeChannels := 0
	for _, ch := range chs {
		if ch.Status == model.ChannelEnabled {
			activeChannels++
		}
	}
	activeTokens := 0
	for _, t := range tokens {
		if t.Status == model.TokenActive {
			activeTokens++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels_total":    len(chs),
		"channels_active":   activeChannels,
		"tokens_total":      len(tokens),
		"tokens_active":     activeTokens,
		"keys_by_channel":   keysByCh,
		"logs_total":        st.Total,
		"logs_errors":       st.Errors,
		"prompt_tokens":     st.PromptTokens,
		"completion_tokens": st.CompletionTokens,
		"real_cost_usd":     st.RealCostUSD,
		"billed_cost_usd":   st.BilledCostUSD,
	})
}

// ---------- channels ----------

func (h *Handler) ListChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := h.store.GetChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(chs)})
}

func (h *Handler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var ch model.Channel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if ch.Name == "" || ch.Provider == "" || ch.BaseURL == "" {
		writeErr(w, http.StatusBadRequest, "name, provider, base_url required")
		return
	}
	if ch.Status == 0 {
		ch.Status = model.ChannelEnabled
	}
	if err := h.store.CreateChannel(&ch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.pool.LoadFromStore(h.store); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (h *Handler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	cur, err := h.store.GetChannel(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	var patch model.Channel
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if patch.Name != "" {
		cur.Name = patch.Name
	}
	if patch.Provider != "" {
		cur.Provider = patch.Provider
	}
	if patch.BaseURL != "" {
		cur.BaseURL = patch.BaseURL
	}
	if patch.Models != nil {
		cur.Models = patch.Models
	}
	if patch.Priority != 0 || r.URL.Query().Get("priority") == "0" {
		cur.Priority = patch.Priority
	}
	if patch.InputPrice != 0 {
		cur.InputPrice = patch.InputPrice
	}
	if patch.OutputPrice != 0 {
		cur.OutputPrice = patch.OutputPrice
	}
	if patch.Status != 0 {
		cur.Status = patch.Status
	}
	if patch.CircuitBreaker.MaxFailures != 0 {
		cur.CircuitBreaker.MaxFailures = patch.CircuitBreaker.MaxFailures
	}
	if patch.CircuitBreaker.ResetTimeout != 0 {
		cur.CircuitBreaker.ResetTimeout = patch.CircuitBreaker.ResetTimeout
	}
	if err := h.store.UpdateChannel(cur); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.router.ReloadChannel(cur.ID)
	if err := h.pool.LoadFromStore(h.store); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (h *Handler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteChannel(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.pool.RemoveChannel(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- keys ----------

func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ks, err := h.store.GetKeys(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range ks {
		ks[i].Key = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(ks)})
}

func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeErr(w, http.StatusBadRequest, "key required")
		return
	}
	k := &model.Key{
		ChannelID: id,
		Key:       body.Key,
		KeyMasked: maskKey(body.Key),
		Status:    model.KeyActive,
	}
	if err := h.store.CreateKey(k); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.pool.LoadFromStore(h.store); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	k.Key = ""
	writeJSON(w, http.StatusOK, k)
}

func (h *Handler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	keyID, err := pathInt(r, "keyId")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteKey(keyID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.pool.LoadFromStore(h.store); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- tokens ----------

func (h *Handler) ListTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := h.store.GetTokens()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range toks {
		toks[i].Key = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(toks)})
}

func (h *Handler) CreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string   `json:"name"`
		PlanID          int64    `json:"plan_id"`
		ExpiresInDays   int      `json:"expires_in_days"`
		ModelsWhitelist []string `json:"models_whitelist"`
		IPWhitelist     []string `json:"ip_whitelist"`
		RPM             int      `json:"rpm"`
		TPM             int      `json:"tpm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	plain := newSessionToken()
	t := &model.Token{
		Key:             plain,
		Name:            body.Name,
		PlanID:          body.PlanID,
		Status:          model.TokenActive,
		RPM:             body.RPM,
		TPM:             body.TPM,
		ModelsWhitelist: body.ModelsWhitelist,
		IPWhitelist:     body.IPWhitelist,
	}
	if body.ExpiresInDays > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour)
	}
	if err := h.store.CreateToken(t); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = h.tokens.Reload()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":   t.ID,
		"key":  plain,
		"name": t.Name,
	})
}

func (h *Handler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteToken(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.tokens.Reload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- users ----------

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := h.store.GetUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range us {
		us[i].PasswordHash = ""
		us[i].SessionToken = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(us)})
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     int    `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	u := &model.User{
		Username:     body.Username,
		PasswordHash: hashPassword(body.Password),
		Role:         model.UserRole(body.Role),
		Status:       1,
	}
	if err := h.store.CreateUser(u); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u.PasswordHash = ""
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if id == 1 {
		writeErr(w, http.StatusBadRequest, "cannot delete default admin")
		return
	}
	if _, err := h.store.GetUser(id); err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if err := h.store.UpdateUser(&model.User{ID: id, Status: 99, PasswordHash: "", SessionToken: ""}); err != nil {
		// fall back to a soft-delete via status; full delete needs extra CRUD
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- logs ----------

func (h *Handler) ListLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.LogFilter{
		Limit:  atoiOr(q.Get("limit"), 50),
		Offset: atoiOr(q.Get("offset"), 0),
	}
	if v := q.Get("token_id"); v != "" {
		filter.TokenID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("channel_id"); v != "" {
		filter.ChannelID, _ = strconv.ParseInt(v, 10, 64)
	}
	filter.Model = q.Get("model")
	if v := q.Get("status_code"); v != "" {
		filter.StatusCode, _ = strconv.Atoi(v)
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.CreatedFrom = t.Unix()
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.CreatedTo = t.Unix()
		}
	}
	logs, total, err := h.store.QueryLogs(filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":   logs,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// ---------- analytics ----------

// logFilterFromQuery parses the standard filter query string used
// by /logs and the analytics endpoints.
func logFilterFromQuery(r *http.Request) store.LogFilter {
	q := r.URL.Query()
	f := store.LogFilter{Limit: atoiOr(q.Get("limit"), 50), Offset: atoiOr(q.Get("offset"), 0)}
	if v := q.Get("token_id"); v != "" {
		f.TokenID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("channel_id"); v != "" {
		f.ChannelID, _ = strconv.ParseInt(v, 10, 64)
	}
	f.Model = q.Get("model")
	if v := q.Get("status_code"); v != "" {
		f.StatusCode, _ = strconv.Atoi(v)
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.CreatedFrom = t.Unix()
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.CreatedTo = t.Unix()
		}
	}
	return f
}

func (h *Handler) AnalyticsTimeSeries(w http.ResponseWriter, r *http.Request) {
	bucket := int64(atoiOr(r.URL.Query().Get("bucket"), 3600))
	points, err := h.store.TimeSeries(logFilterFromQuery(r), bucket)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":   points,
		"bucket": bucket,
	})
}

func (h *Handler) AnalyticsByModel(w http.ResponseWriter, r *http.Request) {
	h.writeNamed(w, r, h.store.TopByModel)
}

func (h *Handler) AnalyticsByChannel(w http.ResponseWriter, r *http.Request) {
	h.writeNamed(w, r, h.store.TopByChannel)
}

func (h *Handler) AnalyticsByToken(w http.ResponseWriter, r *http.Request) {
	h.writeNamed(w, r, h.store.TopByToken)
}

func (h *Handler) writeNamed(w http.ResponseWriter, r *http.Request, fn func(store.LogFilter, int) ([]store.NamedMetric, error)) {
	limit := atoiOr(r.URL.Query().Get("limit"), 10)
	data, err := fn(logFilterFromQuery(r), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// ---------- utils ----------

func maskKey(k string) string {
	if len(k) > 8 {
		return k[:4] + "***" + k[len(k)-4:]
	}
	return k
}

func hashPassword(pw string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + ":" + pw
}

func verifyPassword(hash, pw string) bool {
	idx := strings.IndexByte(hash, ':')
	if idx < 0 {
		return hash == pw
	}
	return hash[idx+1:] == pw
}

func newSessionToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}