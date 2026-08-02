package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/mcp"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/sse"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

type Handler struct {
	store      store.Store
	logStore   *logstore.Manager
	pool       *pool.ChannelPool
	router     *router.RouterEngine
	tokens     *tokencache.Cache
	logBroker  *broker.Broker[*model.Log]
	rt         *runtime.Defaults
	cfg        *config.Config
	keyFile    string
	sessionTTL time.Duration
	alertMgr   AlertReloader
	responseCache cache.Cache
	mcpClientMgr *mcp.ClientManager
	guardrailEngine *guardrail.GuardrailEngine
	// reloadNotifier broadcasts local config writes to other
	// replicas (P12 M2 PG NOTIFY). nil = no-op.
	reloadNotifier func()
}

// AlertReloader is the narrow contract the admin /reload handler
// needs from an alert subsystem. Defined here (rather than importing
// *alert.Manager) to avoid an import cycle through testhelper.
type AlertReloader interface {
	Reload() error
}

func New(st store.Store, ls *logstore.Manager, cp *pool.ChannelPool, eng *router.RouterEngine, tc *tokencache.Cache, lb *broker.Broker[*model.Log], rt *runtime.Defaults, cfg *config.Config, keyFile string) *Handler {
	if rt == nil {
		rt = runtime.New()
	}
	return &Handler{store: st, logStore: ls, pool: cp, router: eng, tokens: tc, logBroker: lb, rt: rt, cfg: cfg, keyFile: keyFile, sessionTTL: 24 * time.Hour}
}

// SetStore replaces the handler's store. Intended for tests that need
// to inject a mock store to exercise error paths.
func (h *Handler) SetStore(st store.Store) { h.store = st }

// SetAlertManager lets main.go inject the alert manager for /reload.
// Accepts the AlertReloader interface to keep the import graph
// acyclic.
func (h *Handler) SetAlertManager(m AlertReloader) { h.alertMgr = m }

// SetCache wires the response cache for stats and purge endpoints.
func (h *Handler) SetCache(c cache.Cache) { h.responseCache = c }

func (h *Handler) SetMCPClientManager(m *mcp.ClientManager) { h.mcpClientMgr = m }

func (h *Handler) SetGuardrailEngine(g *guardrail.GuardrailEngine) { h.guardrailEngine = g }

// SetReloadNotifier installs the cross-replica notify sender. It is
// fired after every admin write that reloads local caches so other
// replicas reload promptly (P12 M2).
func (h *Handler) SetReloadNotifier(fn func()) { h.reloadNotifier = fn }

// fireReload notifies other replicas after a config write.
func (h *Handler) fireReload() {
	if h.reloadNotifier != nil {
		h.reloadNotifier()
	}
}

// SetSessionTTL overrides the default 24h session lifetime.
func (h *Handler) SetSessionTTL(d time.Duration) { h.sessionTTL = d }

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	r.Post("/login", h.Login)
	r.Post("/logout", h.Logout)

	authn := middleware.AdminOnly(func(s string) (any, bool) {
		u, err := h.store.GetUserBySession(s)
		if err != nil || u == nil {
			return nil, false
		}
		return u, true
	})

	// Read-only / routine admin endpoints: require RoleAdmin.
	r.Group(func(r chi.Router) {
		r.Use(authn)
		r.Use(middleware.RequireRole(model.RoleAdmin))
		r.Get("/dashboard", h.Dashboard)
		r.Get("/channels", h.ListChannels)
		r.Post("/channels", h.CreateChannel)
		r.Put("/channels/{id}", h.UpdateChannel)
		r.Delete("/channels/{id}", h.DeleteChannel)
		r.Get("/channels/{id}/keys", h.ListKeys)
		r.Post("/channels/{id}/keys", h.CreateKey)
		r.Delete("/channels/{id}/keys/{keyId}", h.DeleteKey)
		r.Post("/channels/{id}/fetch-models", h.FetchModels)
		r.Get("/providers", h.ListProviders)
		r.Post("/providers", h.CreateProvider)
		r.Delete("/providers/{id}", h.DeleteProvider)
		r.Get("/tokens", h.ListTokens)
		r.Post("/tokens", h.CreateToken)
		r.Put("/tokens/{id}", h.UpdateToken)
		r.Delete("/tokens/{id}", h.DeleteToken)
		r.Get("/tokens/{id}/combos", h.ListCombos)
		r.Post("/tokens/{id}/combos", h.CreateCombo)
		r.Put("/combos/{id}", h.UpdateCombo)
		r.Delete("/combos/{id}", h.DeleteCombo)
		r.Get("/users", h.ListUsers)
		r.Post("/users/{id}/password", h.ChangePassword)
		r.Get("/logs", h.ListLogs)
		r.Get("/logs/stream", h.StreamLogs)
		r.Get("/alerts", h.ListAlerts)
		r.Post("/alerts", h.CreateAlert)
		r.Put("/alerts/{id}", h.UpdateAlert)
		r.Delete("/alerts/{id}", h.DeleteAlert)
		r.Get("/alerts/events", h.ListAlertEvents)
		r.Post("/alerts/events/{id}/ack", h.AckAlertEvent)
		r.Get("/analytics/timeseries", h.AnalyticsTimeSeries)
		r.Get("/analytics/by-model", h.AnalyticsByModel)
		r.Get("/analytics/by-channel", h.AnalyticsByChannel)
		r.Get("/analytics/by-token", h.AnalyticsByToken)
		r.Get("/plans", h.ListPlans)
		r.Post("/plans", h.CreatePlan)
		r.Get("/plans/{id}", h.GetPlan)
		r.Put("/plans/{id}", h.UpdatePlan)
		r.Delete("/plans/{id}", h.DeletePlan)
		r.Get("/config", h.GetConfig)
		r.Get("/effective", h.EffectiveConfig)
		r.Get("/cache/stats", h.CacheStats)
		r.Post("/cache/purge", h.CachePurge)
		r.Get("/guardrails", h.ListGuardrailRules)
		r.Post("/guardrails", h.CreateGuardrailRule)
		r.Put("/guardrails/{id}", h.UpdateGuardrailRule)
		r.Delete("/guardrails/{id}", h.DeleteGuardrailRule)
		r.Get("/guardrails/events", h.ListGuardrailEvents)
		r.Get("/mcp-servers", h.ListMCPServers)
		r.Post("/mcp-servers", h.CreateMCPServer)
		r.Put("/mcp-servers/{id}", h.UpdateMCPServer)
		r.Delete("/mcp-servers/{id}", h.DeleteMCPServer)
		r.Post("/mcp-servers/{id}/refresh", h.RefreshMCPTools)
		r.Get("/mcp-servers/{id}/tools", h.ListMCPTools)
		r.Get("/mcp-servers/{id}/tools/{toolId}/pricing", h.GetMCPToolPricing)
		r.Put("/mcp-servers/{id}/tools/{toolId}/pricing", h.SetMCPToolPricing)
	})

	// Root-only high-impact operations: user lifecycle, runtime
	// config writes, secret rotation, global reload. These can lock
	// out other operators or alter billing/observability behaviour.
	r.Group(func(r chi.Router) {
		r.Use(authn)
		r.Use(middleware.RequireRole(model.RoleRoot))
		r.Post("/users", h.CreateUser)
		r.Delete("/users/{id}", h.DeleteUser)
		r.Put("/config", h.UpdateConfig)
		r.Post("/reload", h.ReloadAll)
		r.Post("/secrets/rotate", h.RotateSecrets)
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
	if !auth.Verify(u.PasswordHash, body.Password).OK {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	tok := newSessionToken()
	u.SessionToken = tok
	exp := time.Now().Add(h.sessionTTL).UTC()
	u.SessionExp = &exp
	// Transparent upgrade of legacy pre-P6 plaintext or P6 bcrypt
	// hash to P7+ argon2id on successful login.
	if auth.IsLegacy(u.PasswordHash) || auth.IsBcrypt(u.PasswordHash) {
		if nh, err := auth.Hash(body.Password); err == nil {
			u.PasswordHash = nh
		}
	}
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
	stats, err := h.logStore.Stats(nil)
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
		"logs_total":        stats.Total,
		"logs_errors":       stats.Errors,
		"prompt_tokens":     stats.PromptTokens,
		"completion_tokens": stats.CompletionTokens,
		"real_cost_usd":     stats.RealCostUSD,
		"billed_cost_usd":   stats.BilledCostUSD,
	})
}

// CacheStats returns the response cache statistics.
func (h *Handler) CacheStats(w http.ResponseWriter, r *http.Request) {
	if h.responseCache == nil {
		writeJSON(w, http.StatusOK, map[string]any{"size": 0, "hits": 0, "misses": 0, "hit_rate": 0})
		return
	}
	stats, err := h.responseCache.Stats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"size":     stats.Size,
		"hits":     stats.Hits,
		"misses":   stats.Misses,
		"hit_rate": stats.HitRate,
	})
}

// CachePurge clears all cached responses.
func (h *Handler) CachePurge(w http.ResponseWriter, r *http.Request) {
	if h.responseCache == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if err := h.responseCache.Purge(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
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
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	} else {
		switch ch.Protocol {
		case "openai", "anthropic", "gemini",
			"openai-compatible", "anthropic-messages", "google-gemini":
		default:
			writeErr(w, http.StatusBadRequest, "protocol must be openai|anthropic|gemini")
			return
		}
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
	var patch struct {
		Name                *string  `json:"name"`
		Provider            *string  `json:"provider"`
		BaseURL             *string  `json:"base_url"`
		Protocol            *string  `json:"protocol"`
		Models              []string `json:"models"`
		Intents             []string `json:"intents"`
		Priority            *int     `json:"priority"`
		InputPrice          *float64 `json:"input_price_per_1m"`
		OutputPrice         *float64 `json:"output_price_per_1m"`
		CachedInputDiscount *float64 `json:"cached_input_discount"`
		MaxFailures         *int     `json:"max_failures"`
		ResetTimeoutMs      *int     `json:"reset_timeout_ms"`
		Status              *int     `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if patch.Name != nil {
		cur.Name = *patch.Name
	}
	if patch.Provider != nil {
		cur.Provider = *patch.Provider
	}
	if patch.BaseURL != nil {
		cur.BaseURL = *patch.BaseURL
	}
	if patch.Protocol != nil {
		switch *patch.Protocol {
		case "openai", "anthropic", "gemini",
			"openai-compatible", "anthropic-messages", "google-gemini":
			cur.Protocol = *patch.Protocol
		default:
			writeErr(w, http.StatusBadRequest, "protocol must be openai|anthropic|gemini")
			return
		}
	}
	if patch.Models != nil {
		cur.Models = patch.Models
	}
	if patch.Intents != nil {
		cur.Intents = patch.Intents
	}
	if patch.Priority != nil {
		cur.Priority = *patch.Priority
	}
	if patch.InputPrice != nil {
		cur.InputPrice = *patch.InputPrice
	}
	if patch.OutputPrice != nil {
		cur.OutputPrice = *patch.OutputPrice
	}
	if patch.CachedInputDiscount != nil {
		cur.CachedInputDiscount = *patch.CachedInputDiscount
	}
	if patch.MaxFailures != nil {
		cur.CircuitBreaker.MaxFailures = *patch.MaxFailures
	}
	if patch.ResetTimeoutMs != nil {
		cur.CircuitBreaker.ResetTimeout = time.Duration(*patch.ResetTimeoutMs) * time.Millisecond
	}
	if patch.Status != nil {
		cur.Status = model.ChannelStatus(*patch.Status)
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
		KeyMasked: secrets.Mask(body.Key),
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

// ---------- fetch models ----------

// FetchModels calls the upstream API to discover available models
// for a channel. Uses the channel's provider (to resolve the
// protocol) and the channel's first active key for auth.
func (h *Handler) FetchModels(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	keys, err := h.store.GetKeys(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(keys) == 0 || keys[0].Key == "" {
		writeErr(w, http.StatusBadRequest, "channel has no API key configured")
		return
	}
	models, err := provider.ListModels(r.Context(), ch.Provider, keys[0].Key, ch.BaseURL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("upstream: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// ---------- providers ----------

func (h *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	descs := provider.AllProviders()
	writeJSON(w, http.StatusOK, map[string]any{"data": descs})
}

func (h *Handler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Protocol    string `json:"protocol"`
		BaseURL     string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == "" || body.Protocol == "" || body.BaseURL == "" {
		writeErr(w, http.StatusBadRequest, "name, protocol, and base_url are required")
		return
	}
	pd := &model.ProviderDef{
		Name:        body.Name,
		DisplayName: body.DisplayName,
		Protocol:    body.Protocol,
		BaseURL:     body.BaseURL,
	}
	if err := h.store.CreateProviderDef(pd); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	provider.RegisterProvider(provider.ProviderDesc{
		Name:           pd.Name,
		DisplayName:    pd.DisplayName,
		Protocol:       pd.Protocol,
		DefaultBaseURL: pd.BaseURL,
		Source:         "db",
	})
	writeJSON(w, http.StatusOK, pd)
}

func (h *Handler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteProviderDef(id); err != nil {
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
	plain := newAPIToken()
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
	h.fireReload()
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
	h.fireReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// UpdateToken updates an existing token's plan, RPM/TPM, whitelist,
// expiry, and enabled status. Hot-reloads the in-memory token cache
// so changes apply to the next incoming request without a restart.
func (h *Handler) UpdateToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	cur, err := h.store.GetTokenByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	var patch struct {
		PlanID          *int64   `json:"plan_id"`
		Status          *int     `json:"status"`
		RPM             *int     `json:"rpm"`
		TPM             *int     `json:"tpm"`
		ModelsWhitelist []string `json:"models_whitelist"`
		IPWhitelist     []string `json:"ip_whitelist"`
		ExpiresInDays   *int     `json:"expires_in_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if patch.PlanID != nil {
		cur.PlanID = *patch.PlanID
	}
	if patch.Status != nil {
		cur.Status = model.TokenStatus(*patch.Status)
	}
	if patch.RPM != nil {
		cur.RPM = *patch.RPM
	}
	if patch.TPM != nil {
		cur.TPM = *patch.TPM
	}
	if patch.ModelsWhitelist != nil {
		cur.ModelsWhitelist = patch.ModelsWhitelist
	}
	if patch.IPWhitelist != nil {
		cur.IPWhitelist = patch.IPWhitelist
	}
	if patch.ExpiresInDays != nil {
		if *patch.ExpiresInDays > 0 {
			cur.ExpiresAt = time.Now().Add(time.Duration(*patch.ExpiresInDays) * 24 * time.Hour)
		} else {
			cur.ExpiresAt = time.Time{}
		}
	}
	if err := h.store.UpdateToken(cur); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.tokens.Reload()
	h.fireReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": cur.ID})
}

// ReloadAll forces every in-memory cache to re-read from the store.
// Useful after manual DB edits or when external tooling (config
// files, kubectl exec, etc.) mutates state outside the admin API.
//
// Idempotent; safe to call repeatedly. Touches:
//   - token cache (TokenInfo re-resolved)
//   - channel pool (channel + key list)
//   - router engine (channel routing rules)
//   - alert rules
func (h *Handler) ReloadAll(w http.ResponseWriter, r *http.Request) {
	reloads := map[string]error{}

	if err := h.tokens.Reload(); err != nil {
		reloads["tokens"] = err
	}
	if err := h.pool.LoadFromStore(h.store); err != nil {
		reloads["channels"] = err
	}
	h.router.ReloadAllChannels()
	if h.alertMgr != nil {
		if err := h.alertMgr.Reload(); err != nil {
			reloads["alerts"] = err
		}
	}
	h.fireReload()
	if len(reloads) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"channels":       h.pool.GetAllChannels() != nil,
			"tokens":         h.tokens.Size(),
			"alerts_reloaded": h.alertMgr != nil,
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"ok":      false,
		"reloads": reloads,
	})
}

func (h *Handler) RotateSecrets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewMasterKey string `json:"new_master_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.NewMasterKey) != 64 {
		writeErr(w, http.StatusBadRequest, "master key must be 64 hex characters")
		return
	}
	if _, err := hex.DecodeString(body.NewMasterKey); err != nil {
		writeErr(w, http.StatusBadRequest, "master key is not valid hex")
		return
	}

	count, err := h.store.RotateMasterKey(body.NewMasterKey)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.keyFile != "" {
		if err := os.WriteFile(h.keyFile, []byte(body.NewMasterKey+"\n"), 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, "rotated keys but failed to persist new key: "+err.Error())
			return
		}
	}

	_ = os.Setenv("LLMRX_KEY_MASTER", body.NewMasterKey)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"rotated": count,
	})
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
	hashed, err := auth.Hash(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u := &model.User{
		Username:     body.Username,
		PasswordHash: hashed,
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

// ChangePassword updates the password for a user. Admins can change
// anyone's password (no old-password check). Non-admins can only
// change their own and must supply the current password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	caller, _ := r.Context().Value(middleware.UserKey).(*model.User)
	if caller == nil {
		writeErr(w, http.StatusUnauthorized, "no session")
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(body.NewPassword) < 6 {
		writeErr(w, http.StatusBadRequest, "new_password must be at least 6 characters")
		return
	}
	target, err := h.store.GetUser(id)
	if err != nil || target == nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	if caller.ID != id && caller.Role < model.RoleAdmin {
		writeErr(w, http.StatusForbidden, "cannot change other user's password")
		return
	}
	if caller.ID == id {
		if !auth.Verify(target.PasswordHash, body.OldPassword).OK {
			writeErr(w, http.StatusUnauthorized, "old_password incorrect")
			return
		}
	}
	hashed, err := auth.Hash(body.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	target.PasswordHash = hashed
	// Invalidate all sessions for the target to force re-login.
	target.SessionToken = ""
	target.SessionExp = nil
	if err := h.store.UpdateUser(target); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- logs ----------

func (h *Handler) ListLogs(w http.ResponseWriter, r *http.Request) {
	filter := logFilterFromQuery(r)
	logs, total, err := h.logStore.Query(filter, nil)
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
func logFilterFromQuery(r *http.Request) logstore.QueryFilter {
	q := r.URL.Query()
	f := logstore.QueryFilter{Limit: atoiOr(q.Get("limit"), 50), Offset: atoiOr(q.Get("offset"), 0)}
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

// StreamLogs serves a Server-Sent Events stream of new log entries.
// The first event is a "hello" comment so clients can confirm the
// connection is live; subsequent events are "log" frames with the
// JSON-encoded *model.Log payload. The connection stays open until
// the client disconnects.
func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	if h.logBroker == nil {
		writeErr(w, http.StatusServiceUnavailable, "log streaming not configured")
		return
	}
	w2, err := sse.New(w)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := w2.Comment("hello llmRx logs"); err != nil {
		return
	}
	ch, unsub, err := h.logBroker.Subscribe()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer unsub()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go w2.Heartbeat(ctx, 15*time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			if err := w2.EventJSON("log", entry); err != nil {
				return
			}
		}
	}
}

// ---------- alerts ----------

func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	as, err := h.store.GetAlerts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(as)})
}

func (h *Handler) CreateAlert(w http.ResponseWriter, r *http.Request) {
	var a model.Alert
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if a.Name == "" || a.Type == "" {
		writeErr(w, http.StatusBadRequest, "name and type required")
		return
	}
	if !validAlertType(a.Type) {
		writeErr(w, http.StatusBadRequest, "type must be error_rate|p95_latency|cost_spike|key_exhausted")
		return
	}
	if a.WindowSec <= 0 {
		a.WindowSec = 300
	}
	if a.CooldownSec <= 0 {
		a.CooldownSec = 300
	}
	if err := h.store.CreateAlert(&a); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) UpdateAlert(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	cur, err := h.store.GetAlert(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "alert not found")
		return
	}
	var patch struct {
		Name        *string         `json:"name,omitempty"`
		Type        *model.AlertType `json:"type,omitempty"`
		Threshold   *float64        `json:"threshold,omitempty"`
		WindowSec   *int64          `json:"window_sec,omitempty"`
		CooldownSec *int64          `json:"cooldown_sec,omitempty"`
		WebhookURL  *string         `json:"webhook_url,omitempty"`
		Enabled     *bool           `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if patch.Name != nil {
		cur.Name = *patch.Name
	}
	if patch.Type != nil {
		if !validAlertType(*patch.Type) {
			writeErr(w, http.StatusBadRequest, "type must be error_rate|p95_latency|cost_spike|key_exhausted")
			return
		}
		cur.Type = *patch.Type
	}
	if patch.Threshold != nil {
		cur.Threshold = *patch.Threshold
	}
	if patch.WindowSec != nil {
		cur.WindowSec = *patch.WindowSec
	}
	if patch.CooldownSec != nil {
		cur.CooldownSec = *patch.CooldownSec
	}
	if patch.WebhookURL != nil {
		cur.WebhookURL = *patch.WebhookURL
	}
	if patch.Enabled != nil {
		cur.Enabled = *patch.Enabled
	}
	if err := h.store.UpdateAlert(cur); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (h *Handler) DeleteAlert(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteAlert(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) ListAlertEvents(w http.ResponseWriter, r *http.Request) {
	limit := atoiOr(r.URL.Query().Get("limit"), 100)
	es, err := h.store.GetAlertEvents(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(es)})
}

func (h *Handler) AckAlertEvent(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.AckAlertEvent(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func validAlertType(t model.AlertType) bool {
	switch t {
	case model.AlertErrorRate, model.AlertP95Latency, model.AlertCostSpike, model.AlertKeyExhausted:
		return true
	}
	return false
}

func (h *Handler) AnalyticsTimeSeries(w http.ResponseWriter, r *http.Request) {
	bucket := int64(atoiOr(r.URL.Query().Get("bucket"), 3600))
	points, err := h.logStore.TimeSeries(logFilterFromQuery(r), bucket, nil)
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
	h.writeNamed(w, r, "model")
}

func (h *Handler) AnalyticsByChannel(w http.ResponseWriter, r *http.Request) {
	h.writeNamed(w, r, "channel_id")
}

func (h *Handler) AnalyticsByToken(w http.ResponseWriter, r *http.Request) {
	h.writeNamed(w, r, "token_id")
}

func (h *Handler) writeNamed(w http.ResponseWriter, r *http.Request, field string) {
	limit := atoiOr(r.URL.Query().Get("limit"), 10)
	data, err := h.logStore.TopByField(logFilterFromQuery(r), field, limit, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// ---------- runtime config ----------

func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"cost_strategy":            string(h.router.CostStrategy()),
		"breaker_max_failures":     h.rt.BreakerMaxFailures(),
		"breaker_reset_timeout_ms": h.rt.BreakerResetTimeoutMs(),
		"alert_cooldown_sec":       h.rt.AlertCooldownSec(),
		"log_retention_days":       h.rt.LogRetentionDays(),
		"markup_ratio":             h.rt.MarkupRatio(),
		"stream_timeout_sec":       h.rt.StreamTimeoutSec(),
		"stream_max_body_bytes":    h.rt.StreamMaxBodyBytes(),
		"max_log_subscribers":      h.rt.MaxLogSubscribers(),
		"log_level":                h.rt.LogLevel(),
	})
}

func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CostStrategy          *string  `json:"cost_strategy,omitempty"`
		BreakerMaxFailures    *int64   `json:"breaker_max_failures,omitempty"`
		BreakerResetTimeoutMs *int64   `json:"breaker_reset_timeout_ms,omitempty"`
		AlertCooldownSec      *int64   `json:"alert_cooldown_sec,omitempty"`
		LogRetentionDays      *int64   `json:"log_retention_days,omitempty"`
		MarkupRatio           *float64 `json:"markup_ratio,omitempty"`
		StreamTimeoutSec      *int64   `json:"stream_timeout_sec,omitempty"`
		StreamMaxBodyBytes    *int64   `json:"stream_max_body_bytes,omitempty"`
		MaxLogSubscribers     *int64   `json:"max_log_subscribers,omitempty"`
		LogLevel              *int64   `json:"log_level,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.CostStrategy != nil {
		switch *body.CostStrategy {
		case "cheapest", "fastest", "balanced":
			h.router.SetStrategy(model.CostStrategy(*body.CostStrategy))
			h.rt.SetCostStrategy(*body.CostStrategy)
		case "":
			h.router.SetStrategy(model.StrategyCheapest)
			h.rt.SetCostStrategy("cheapest")
		default:
			writeErr(w, http.StatusBadRequest, "cost_strategy must be cheapest|fastest|balanced")
			return
		}
	}
	if body.BreakerMaxFailures != nil {
		if *body.BreakerMaxFailures < 1 || *body.BreakerMaxFailures > 1000 {
			writeErr(w, http.StatusBadRequest, "breaker_max_failures must be 1..1000")
			return
		}
		h.rt.SetBreakerMaxFailures(*body.BreakerMaxFailures)
	}
	if body.BreakerResetTimeoutMs != nil {
		if *body.BreakerResetTimeoutMs < 100 || *body.BreakerResetTimeoutMs > 24*60*60*1000 {
			writeErr(w, http.StatusBadRequest, "breaker_reset_timeout_ms must be 100..86400000")
			return
		}
		h.rt.SetBreakerResetTimeoutMs(*body.BreakerResetTimeoutMs)
	}
	if body.AlertCooldownSec != nil {
		if *body.AlertCooldownSec < 0 || *body.AlertCooldownSec > 24*60*60 {
			writeErr(w, http.StatusBadRequest, "alert_cooldown_sec must be 0..86400")
			return
		}
		h.rt.SetAlertCooldownSec(*body.AlertCooldownSec)
	}
	if body.LogRetentionDays != nil {
		if *body.LogRetentionDays < 0 || *body.LogRetentionDays > 3650 {
			writeErr(w, http.StatusBadRequest, "log_retention_days must be 0..3650")
			return
		}
		h.rt.SetLogRetentionDays(*body.LogRetentionDays)
	}
	if body.MarkupRatio != nil {
		if *body.MarkupRatio < 0.01 || *body.MarkupRatio > 1000 {
			writeErr(w, http.StatusBadRequest, "markup_ratio must be 0.01..1000")
			return
		}
		h.rt.SetMarkupRatio(*body.MarkupRatio)
	}
	if body.StreamTimeoutSec != nil {
		if *body.StreamTimeoutSec < 0 || *body.StreamTimeoutSec > 3600 {
			writeErr(w, http.StatusBadRequest, "stream_timeout_sec must be 0..3600")
			return
		}
		h.rt.SetStreamTimeoutSec(*body.StreamTimeoutSec)
	}
	if body.StreamMaxBodyBytes != nil {
		if *body.StreamMaxBodyBytes < 0 || *body.StreamMaxBodyBytes > (1<<30) {
			writeErr(w, http.StatusBadRequest, "stream_max_body_bytes must be 0..1073741824")
			return
		}
		h.rt.SetStreamMaxBodyBytes(*body.StreamMaxBodyBytes)
	}
	if body.MaxLogSubscribers != nil {
		if *body.MaxLogSubscribers < 0 || *body.MaxLogSubscribers > 100000 {
			writeErr(w, http.StatusBadRequest, "max_log_subscribers must be 0..100000")
			return
		}
		h.rt.SetMaxLogSubscribers(*body.MaxLogSubscribers)
		// Live-reload: push the new cap into the broker so SSE
		// subscription requests see it on the next call.
		if h.logBroker != nil {
			h.logBroker.SetMaxSubscribers(*body.MaxLogSubscribers)
		}
	}
	if body.LogLevel != nil {
		if *body.LogLevel < 0 || *body.LogLevel > 3 {
			writeErr(w, http.StatusBadRequest, "log_level must be 0..3 (debug|info|warn|error)")
			return
		}
		h.rt.SetLogLevel(*body.LogLevel)
		// Hook for actual log filtering — see PR #2 commit 5
		// (log_level runtime filter). For now the value is
		// persisted; integration with the logger is the next
		// step.
	}
	// Persist the resulting snapshot to the runtime_settings table
	// so the changes survive restarts. Failure to persist is
	// surfaced as 500 — the in-memory change has already been
	// applied, but we don't want to silently lose it.
	if raw, err := h.rt.Marshal(); err != nil {
		writeErr(w, http.StatusInternalServerError, "marshal: "+err.Error())
		return
	} else if err := h.store.SetRuntimeSettings(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	// Re-emit the effective values.
	h.GetConfig(w, r)
}

// ---------- utils ----------

func maskKey(k string) string {
	if len(k) > 8 {
		return k[:4] + "***" + k[len(k)-4:]
	}
	return k
}

func newSessionToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newAPIToken generates a bearer token with the OpenAI-style "sk-"
// prefix. The prefix is conventional, not enforced by the auth
// middleware, but it keeps the value consistent with the README and
// most upstream providers, and makes masked display in the admin UI
// read sensibly.
func newAPIToken() string {
	return "sk-" + newSessionToken()
}

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// ---------- ComboModels ----------

func (h *Handler) ListCombos(w http.ResponseWriter, r *http.Request) {
	tokenID, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad token id")
		return
	}
	combos, err := h.store.GetComboModels(tokenID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(combos)})
}

func (h *Handler) CreateCombo(w http.ResponseWriter, r *http.Request) {
	tokenID, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad token id")
		return
	}
	var body struct {
		Name     string   `json:"name"`
		Models   []string `json:"models"`
		Mode     string   `json:"mode"`
		Strategy string   `json:"strategy"`
		Enabled  *bool    `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(body.Models) == 0 {
		writeErr(w, http.StatusBadRequest, "models list must not be empty")
		return
	}
	mode := model.ComboMode(body.Mode)
	if mode == "" {
		mode = model.ComboModeLoadBalance
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	c := &model.TokenComboModel{
		TokenID:  tokenID,
		Name:     body.Name,
		Models:   body.Models,
		Mode:     mode,
		Strategy: model.CostStrategy(body.Strategy),
		Enabled:  enabled,
	}
	if err := h.store.CreateComboModel(c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = h.tokens.Reload()
	h.fireReload()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":   c.ID,
		"name": c.Name,
	})
}

func (h *Handler) UpdateCombo(w http.ResponseWriter, r *http.Request) {
	comboID, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad combo id")
		return
	}
	existing, err := h.store.GetComboModel(comboID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "combo not found")
		return
	}
	var body struct {
		Name     *string  `json:"name"`
		Models   []string `json:"models"`
		Mode     *string  `json:"mode"`
		Strategy *string  `json:"strategy"`
		Enabled  *bool    `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.Models != nil {
		existing.Models = body.Models
	}
	if body.Mode != nil {
		existing.Mode = model.ComboMode(*body.Mode)
	}
	if body.Strategy != nil {
		existing.Strategy = model.CostStrategy(*body.Strategy)
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if err := h.store.UpdateComboModel(existing); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = h.tokens.Reload()
	h.fireReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) DeleteCombo(w http.ResponseWriter, r *http.Request) {
	comboID, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad combo id")
		return
	}
	if err := h.store.DeleteComboModel(comboID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.tokens.Reload()
	h.fireReload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- MCP Server CRUD ----------

func (h *Handler) ListMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.GetMCPServers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(servers)})
}

func (h *Handler) CreateMCPServer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		URL       string `json:"url"`
		AuthHdr   string `json:"auth_header"`
		Transport string `json:"transport"`
		Command   string `json:"command"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name == "" || body.URL == "" {
		writeErr(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if body.Transport == "" {
		body.Transport = "http"
	}
	srv := &store.MCPServer{Name: body.Name, URL: body.URL, AuthHdr: body.AuthHdr, Transport: body.Transport, Command: body.Command, Enabled: body.Enabled}
	if err := h.store.CreateMCPServer(r.Context(), srv); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": srv})
}

func (h *Handler) UpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	existing, err := h.store.GetMCPServer(r.Context(), id)
	if err != nil || existing == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var body struct {
		Name      *string `json:"name"`
		URL       *string `json:"url"`
		AuthHdr   *string `json:"auth_header"`
		Transport *string `json:"transport"`
		Command   *string `json:"command"`
		Enabled   *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.URL != nil {
		existing.URL = *body.URL
	}
	if body.AuthHdr != nil {
		existing.AuthHdr = *body.AuthHdr
	}
	if body.Transport != nil {
		existing.Transport = *body.Transport
	}
	if body.Command != nil {
		existing.Command = *body.Command
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if err := h.store.UpdateMCPServer(r.Context(), existing); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.mcpClientMgr != nil {
		h.mcpClientMgr.Invalidate(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": existing})
}

func (h *Handler) DeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteMCPServer(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.mcpClientMgr != nil {
		h.mcpClientMgr.Invalidate(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) RefreshMCPTools(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if h.mcpClientMgr == nil {
		writeErr(w, http.StatusInternalServerError, "mcp client manager not initialized")
		return
	}
	tools, err := h.mcpClientMgr.RefreshTools(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "refresh failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(tools)})
}

func (h *Handler) ListMCPTools(w http.ResponseWriter, r *http.Request) {
	serverID, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	tools, err := h.store.GetMCPTools(r.Context(), serverID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(tools)})
}

func (h *Handler) GetMCPToolPricing(w http.ResponseWriter, r *http.Request) {
	toolID, err := pathInt(r, "toolId")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad tool id")
		return
	}
	pricing, err := h.store.GetMCPToolPricing(r.Context(), toolID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": pricing})
}

func (h *Handler) SetMCPToolPricing(w http.ResponseWriter, r *http.Request) {
	toolID, err := pathInt(r, "toolId")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad tool id")
		return
	}
	var body struct {
		PricePerCallUSD float64 `json:"price_per_call_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	p := &store.MCPToolPricing{MCPToolID: toolID, PricePerCallUSD: body.PricePerCallUSD}
	if err := h.store.SetMCPToolPricing(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": p})
}