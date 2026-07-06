// Package testhelper wires up a full in-process llmRx app for
// handler-level tests: a temp-file SQLite store, an in-memory
// channel pool, the routing engine, the token cache, the admin
// handler, the chat handler, and a mock provider that tests can
// inject scripted responses into.
package testhelper

import (
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/admin"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/config"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

type App struct {
	T        *testing.T
	Store    store.Store
	Pool     *pool.ChannelPool
	Cache    *tokencache.Cache
	Engine   *router.RouterEngine
	Admin    *admin.Handler
	Chat     *api.Handler
	Provider *MockProvider
	Cfg      *config.Config
	Mux      http.Handler // fully wired mux: /v1/chat/completions, /v1/models, /api/v1, /health
}

// New constructs an App backed by a fresh temp-dir SQLite database
// and seeds one admin user (username=admin, password=admin).
func New(t *testing.T) *App {
	t.Helper()

	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.CreateUser(&model.User{
		Username: "admin", PasswordHash: hashForAdminSeed(), Role: model.RoleRoot, Status: 1,
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	cache := tokencache.New(st)
	eng := router.New(st, cp)
	adminH := admin.New(st, cp, eng, cache)

	mp := &MockProvider{}
	chatH := api.New(&config.Config{}, eng, cp, st)
	chatH.SetProvider(mp)

	mux := chi.NewRouter()
	mux.With(authmw.Token(cache.Lookup)).Mount("/v1", chatH.Routes())
	mux.Mount("/api/v1", adminH.Routes())
	mux.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return &App{
		T:        t,
		Store:    st,
		Pool:     cp,
		Cache:    cache,
		Engine:   eng,
		Admin:    adminH,
		Chat:     chatH,
		Provider: mp,
		Cfg:      &config.Config{},
		Mux:      mux,
	}
}

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

func maskKey(k string) string {
	if len(k) > 8 {
		return k[:4] + "***" + k[len(k)-4:]
	}
	return k
}

// hashForAdminSeed mirrors admin.handler's verifyPassword contract:
// seed expects "salt:plaintext" and verify compares after the colon.
func hashForAdminSeed() string {
	return "seedsalt:admin"
}

// ---------------- Mock provider ----------------

// MockProvider scripts responses / errors per call. Concurrency-safe.
type MockProvider struct {
	mu        sync.Mutex
	Responses []*provider.ChatResponse
	Statuses  []int
	Errs      []error
	Calls     int
	LastKey   string
	LastURL   string
}

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) Chat(req *provider.ChatRequest, apiKey string, baseURL string) (*provider.ChatResponse, int, error) {
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
	// Default: 200 OK with empty usage
	return &provider.ChatResponse{
		ID:    "chatcmpl-test",
		Model: req.Model,
		Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}, httpStatusAt(m.Statuses, idx, 200), nil
}

func httpStatusAt(s []int, idx, def int) int {
	if idx < len(s) {
		return s[idx]
	}
	return def
}