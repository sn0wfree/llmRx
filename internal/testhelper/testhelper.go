// Package testhelper wires up a full in-process llmRx app for
// handler-level tests: a temp-file SQLite store, an in-memory
// channel pool, the routing engine, the token cache, the admin
// handler, the chat handler, and a mock provider that tests can
// inject scripted responses into.
package testhelper

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/admin"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/logstore"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

type App struct {
	T         *testing.T
	Store     store.Store
	Pool      *pool.ChannelPool
	Cache     *tokencache.Cache
	Engine    *router.RouterEngine
	Admin     *admin.Handler
	Chat      *api.Handler
	Provider  *MockProvider
	LogBroker *broker.Broker[*model.Log]
	RT        *runtime.Defaults
	Cfg       *config.Config
	Limiter   *ratelimit.Limiter
	Mux       http.Handler
}

// New constructs an App backed by a fresh temp-dir SQLite database
// and seeds one admin user (username=admin, password=admin).
// The mux uses WithLimits (rate limiting + token expiry + budget
// enforcement) to match production wiring. Tokens with RPM=0/TPM=0
// and no plan budget are unlimited, so existing tests are unaffected.
func New(t *testing.T) *App {
	t.Helper()

	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("logstore.EnsureDir: %v", err)
	}
	logStore, err := logstore.New(logDir, nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	st.SetLogStore(logStore)
	t.Cleanup(func() { _ = logStore.Close() })

	if err := st.CreateUser(&model.User{
		Username: "admin", PasswordHash: hashForAdminSeed(t), Role: model.RoleRoot, Status: 1,
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	cache := tokencache.New(st)
	eng := router.New(st, cp)
	logBroker := broker.New[*model.Log](128)
	rt := runtime.New()
	cfg := &config.Config{}
	limiter := ratelimit.New()
	adminH := admin.New(st, cp, eng, cache, logBroker, rt, cfg, "")

	mp := &MockProvider{}
	chatH := api.New(cfg, eng, cp, st, logBroker, rt)
	chatH.SetProvider(mp)
	chatH.SetProviders(map[string]provider.Provider{
		"":          mp,
		"openai":    mp,
		"anthropic": mp,
		"gemini":    mp,
	})

	mux := chi.NewRouter()
	mux.With(authmw.WithLimits(cache.Lookup, limiter)).Mount("/v1", chatH.Routes())
	mux.Mount("/admin/api/v1", adminH.Routes())
	mux.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return &App{
		T:         t,
		Store:     st,
		Pool:      cp,
		Cache:     cache,
		Engine:    eng,
		Admin:     adminH,
		Chat:      chatH,
		Provider:  mp,
		LogBroker: logBroker,
		RT:        rt,
		Cfg:       cfg,
		Limiter:   limiter,
		Mux:       mux,
	}
}

// NewWithoutLimits constructs an App identical to New() but mounts
// the Token middleware without rate limiting. Useful for tests that
// need to bypass the limiter (e.g. error-path tests with ScriptedStore).
func NewWithoutLimits(t *testing.T) *App {
	t.Helper()
	app := New(t)
	mux := chi.NewRouter()
	mux.With(authmw.Token(app.Cache.Lookup)).Mount("/v1", app.Chat.Routes())
	mux.Mount("/admin/api/v1", app.Admin.Routes())
	mux.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	app.Mux = mux
	return app
}

// NewWithStore constructs an App using the provided store instead of
// opening a fresh SQLite database. Uses WithLimits middleware.
func NewWithStore(t *testing.T, st store.Store) *App {
	t.Helper()

	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	cache := tokencache.New(st)
	eng := router.New(st, cp)
	logBroker := broker.New[*model.Log](128)
	rt := runtime.New()
	cfg := &config.Config{}
	limiter := ratelimit.New()
	adminH := admin.New(st, cp, eng, cache, logBroker, rt, cfg, "")

	mp := &MockProvider{}
	chatH := api.New(cfg, eng, cp, st, logBroker, rt)
	chatH.SetProvider(mp)
	chatH.SetProviders(map[string]provider.Provider{
		"":          mp,
		"openai":    mp,
		"anthropic": mp,
		"gemini":    mp,
	})

	mux := chi.NewRouter()
	mux.With(authmw.WithLimits(cache.Lookup, limiter)).Mount("/v1", chatH.Routes())
	mux.Mount("/admin/api/v1", adminH.Routes())
	mux.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return &App{
		T:         t,
		Store:     st,
		Pool:      cp,
		Cache:     cache,
		Engine:    eng,
		Admin:     adminH,
		Chat:      chatH,
		Provider:  mp,
		LogBroker: logBroker,
		RT:        rt,
		Cfg:       cfg,
		Limiter:   limiter,
		Mux:       mux,
	}
}

// --- Channel / Token / Plan helpers ---

// AddChannel inserts a channel + optional key directly via the store.
func (a *App) AddChannel(name, providerName, baseURL string, models []string, keys ...string) *model.Channel {
	a.T.Helper()
	return a.AddChannelWithPrice(name, providerName, baseURL, models, 0, 0, keys...)
}

// AddChannelWithPrice is AddChannel with explicit per-million input/output
// pricing so cost logs are non-zero in tests.
func (a *App) AddChannelWithPrice(name, providerName, baseURL string, models []string, in, out float64, keys ...string) *model.Channel {
	a.T.Helper()
	ch := &model.Channel{
		Name: name, Provider: providerName, BaseURL: baseURL, Models: models,
		InputPrice: in, OutputPrice: out,
		Status: model.ChannelEnabled,
	}
	if err := a.Store.CreateChannel(ch); err != nil {
		a.T.Fatalf("AddChannel %s: %v", name, err)
	}
	for _, k := range keys {
		if err := a.Store.CreateKey(&model.Key{
			ChannelID: ch.ID, Key: k, KeyMasked: maskKey(k), Status: model.KeyActive,
		}); err != nil {
			a.T.Fatalf("AddChannel key: %v", err)
		}
	}
	if err := a.Pool.LoadFromStore(a.Store); err != nil {
		a.T.Fatalf("AddChannel reload pool: %v", err)
	}
	return ch
}

// AddToken creates an active API token.
func (a *App) AddToken(key, name string) *model.Token {
	a.T.Helper()
	t := &model.Token{Key: key, Name: name, Status: model.TokenActive}
	if err := a.Store.CreateToken(t); err != nil {
		a.T.Fatalf("AddToken %s: %v", key, err)
	}
	if err := a.Cache.Reload(); err != nil {
		a.T.Fatalf("AddToken reload cache: %v", err)
	}
	return t
}

// AddTokenWithLimits creates a token with explicit RPM, TPM, and model
// whitelist. Useful for rate-limit and access-control scenario tests.
func (a *App) AddTokenWithLimits(key, name string, rpm, tpm int, models []string) *model.Token {
	a.T.Helper()
	t := &model.Token{
		Key: key, Name: name, Status: model.TokenActive,
		RPM: rpm, TPM: tpm, ModelsWhitelist: models,
	}
	if err := a.Store.CreateToken(t); err != nil {
		a.T.Fatalf("AddTokenWithLimits %s: %v", key, err)
	}
	if err := a.Cache.Reload(); err != nil {
		a.T.Fatalf("AddTokenWithLimits reload cache: %v", err)
	}
	return t
}

// AddPlan creates a plan with a USD budget and returns it.
func (a *App) AddPlan(name string, budgetUSD float64) *model.Plan {
	a.T.Helper()
	p := &model.Plan{Name: name, BudgetUSD: budgetUSD, Status: 1}
	if err := a.Store.CreatePlan(p); err != nil {
		a.T.Fatalf("AddPlan %s: %v", name, err)
	}
	return p
}

// BindTokenToPlan associates a token with a plan so budget enforcement applies.
func (a *App) BindTokenToPlan(tokenKey string, planID int64) {
	a.T.Helper()
	tok, err := a.Store.GetToken(tokenKey)
	if err != nil {
		a.T.Fatalf("BindTokenToPlan: lookup %s: %v", tokenKey, err)
	}
	tok.PlanID = planID
	if err := a.Store.UpdateToken(tok); err != nil {
		a.T.Fatalf("BindTokenToPlan: update: %v", err)
	}
	if err := a.Cache.Reload(); err != nil {
		a.T.Fatalf("BindTokenToPlan: reload: %v", err)
	}
}

// --- Guardrail helper ---

// AddGuardrailRule creates an enabled guardrail rule and returns it.
func (a *App) AddGuardrailRule(name string, ruleType model.GuardrailType, hook model.GuardrailHook, configJSON string) *model.GuardrailRule {
	a.T.Helper()
	r := &model.GuardrailRule{
		Name:      name,
		Type:      ruleType,
		Hook:      hook,
		OnFailure: model.GuardrailActionDeny,
		Config:    configJSON,
		Enabled:   true,
	}
	if err := a.Store.CreateGuardrailRule(r); err != nil {
		a.T.Fatalf("AddGuardrailRule %s: %v", name, err)
	}
	return r
}

// --- Combo model helper ---

// AddComboModel creates a combo model on the given token.
func (a *App) AddComboModel(tokenKey, comboName string, models []string, mode model.ComboMode) {
	a.T.Helper()
	tok, err := a.Store.GetToken(tokenKey)
	if err != nil {
		a.T.Fatalf("AddComboModel: lookup %s: %v", tokenKey, err)
	}
	combo := &model.TokenComboModel{
		TokenID: tok.ID,
		Name:    comboName,
		Models:  models,
		Mode:    mode,
		Enabled: true,
	}
	if err := a.Store.CreateComboModel(combo); err != nil {
		a.T.Fatalf("AddComboModel %s: %v", comboName, err)
	}
	if err := a.Cache.Reload(); err != nil {
		a.T.Fatalf("AddComboModel reload cache: %v", err)
	}
}

// --- Internal helpers ---

func maskKey(k string) string {
	if len(k) > 8 {
		return k[:4] + "***" + k[len(k)-4:]
	}
	return k
}

func hashForAdminSeed(t *testing.T) string {
	t.Helper()
	h, err := auth.Hash("admin")
	if err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	return h
}

// ---------------- Mock provider ----------------

// MockProvider scripts responses / errors per call. Concurrency-safe.
// Implements Provider, StreamingProvider, and EmbeddingsProvider.
type MockProvider struct {
	mu        sync.Mutex
	Responses []*provider.ChatResponse
	Statuses  []int
	Errs      []error
	Calls     int
	LastKey   string
	LastURL   string

	StreamChunks []provider.StreamChunk
	StreamErr    error

	EmbResponses []*provider.EmbeddingsResponse
	EmbErrs      []error
	EmbCalls     int
}

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) Chat(ctx context.Context, req *provider.ChatRequest, apiKey string, baseURL string) (*provider.ChatResponse, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.Calls
	m.Calls++
	m.LastKey = apiKey
	m.LastURL = baseURL

	if idx < len(m.Errs) && m.Errs[idx] != nil {
		st := httpStatusAt(m.Statuses, idx, 500)
		return nil, st, m.Errs[idx]
	}
	if idx < len(m.Responses) && m.Responses[idx] != nil {
		st := httpStatusAt(m.Statuses, idx, 200)
		return m.Responses[idx], st, nil
	}
	return &provider.ChatResponse{
		ID:    "chatcmpl-test",
		Model: req.Model,
		Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}, httpStatusAt(m.Statuses, idx, 200), nil
}

// StreamChat implements StreamingProvider for tests.
func (m *MockProvider) StreamChat(ctx context.Context, req *provider.ChatRequest, apiKey, baseURL string) (<-chan provider.StreamEvent, error) {
	if m.StreamErr != nil {
		return nil, m.StreamErr
	}
	out := make(chan provider.StreamEvent, len(m.StreamChunks)+1)
	go func() {
		defer close(out)
		for _, c := range m.StreamChunks {
			select {
			case <-ctx.Done():
				return
			case out <- provider.StreamEvent{Chunk: c}:
			}
		}
	}()
	return out, nil
}

// Embeddings implements EmbeddingsProvider for tests.
func (m *MockProvider) Embeddings(ctx context.Context, req *provider.EmbeddingsRequest, apiKey, baseURL string) (*provider.EmbeddingsResponse, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.EmbCalls
	m.EmbCalls++
	m.LastKey = apiKey
	m.LastURL = baseURL

	if idx < len(m.EmbErrs) && m.EmbErrs[idx] != nil {
		return nil, 500, m.EmbErrs[idx]
	}
	if idx < len(m.EmbResponses) && m.EmbResponses[idx] != nil {
		return m.EmbResponses[idx], 200, nil
	}
	return &provider.EmbeddingsResponse{
		Object: "list",
		Data: []provider.Embedding{
			{Object: "embedding", Embedding: []float64{0.1, 0.2, 0.3}, Index: 0},
		},
		Model: req.Model,
		Usage: provider.Usage{PromptTokens: 5, TotalTokens: 5},
	}, 200, nil
}

func httpStatusAt(s []int, idx, def int) int {
	if idx < len(s) {
		return s[idx]
	}
	return def
}
