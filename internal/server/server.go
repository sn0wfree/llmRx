package server

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/sn0wfree/llmRx/internal/admin"
	"github.com/sn0wfree/llmRx/internal/alert"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
	"github.com/sn0wfree/llmRx/internal/webui"
)

type Server struct {
	cfg     *config.Config
	keyFile string
	router  *router.RouterEngine
	pool    *pool.ChannelPool
	store   store.Store
	tokens  *tokencache.Cache
	admin   *admin.Handler
	engine  *chi.Mux
}

func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, tc *tokencache.Cache, lb *broker.Broker[*model.Log], rt *runtime.Defaults, keyFile string) *Server {
	s := &Server{
		cfg:     cfg,
		keyFile: keyFile,
		router:  eng,
		pool:    cp,
		store:   st,
		tokens:  tc,
		engine:  chi.NewRouter(),
	}
	s.registerMiddleware()
	s.registerRoutes(lb, rt)
	return s
}

func (s *Server) registerMiddleware() {
	s.engine.Use(chimw.Logger)
	s.engine.Use(chimw.Recoverer)
	s.engine.Use(chimw.RealIP)
	s.engine.Use(chimw.Timeout(120 * time.Second))
	s.engine.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Task-Type", "X-Session-Token"},
		ExposedHeaders:   []string{"X-Session-Token"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
}

func (s *Server) registerRoutes(lb *broker.Broker[*model.Log], rt *runtime.Defaults) {
	handler := api.New(s.cfg, s.router, s.pool, s.store, lb, rt)
	adminHandler := admin.New(s.store, s.pool, s.router, s.tokens, lb, rt, s.cfg, s.keyFile)
	s.admin = adminHandler

	s.engine.With(authmw.WithLimits(s.tokens.Lookup, handler.Limits())).
		Mount("/v1", handler.Routes())

	s.engine.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Phase 0: html/template admin UI. Legacy JSON API still
	// available under /admin/api/v1 for backwards compatibility.
	webAPIBridge := webui.NewWebAPIBridge(s.store)
	webAPIBridge.SetReloader(func() error {
		if err := s.tokens.Reload(); err != nil {
			return err
		}
		return s.pool.LoadFromStore(s.store)
	})
	webUI, err := webui.New(s.store, webAPIBridge)
	if err != nil {
		log.Fatalf("webui: %v", err)
	}
	s.engine.Mount("/admin", webUI.Routes())
	s.engine.Mount("/admin/api/v1", adminHandler.Routes())
}

func (s *Server) Start() error {
	host := s.cfg.Server.Host
	if host == "" {
		host = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", host, s.cfg.Server.Port)
	log.Printf("listening on %s (tokens=%d)", addr, s.tokens.Size())
	return http.ListenAndServe(addr, s.engine)
}

// SetAlertManager injects the alert manager into the admin handler
// so that POST /api/v1/reload can also refresh alert rules.
func (s *Server) SetAlertManager(m *alert.Manager) {
	if s.admin != nil {
		s.admin.SetAlertManager(m)
	}
}