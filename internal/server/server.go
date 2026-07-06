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
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/config"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
	"github.com/sn0wfree/llmRx/internal/webui"
)

type Server struct {
	cfg    *config.Config
	router *router.RouterEngine
	pool   *pool.ChannelPool
	store  store.Store
	tokens *tokencache.Cache
	engine *chi.Mux
}

func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, tc *tokencache.Cache) *Server {
	s := &Server{
		cfg:    cfg,
		router: eng,
		pool:   cp,
		store:  st,
		tokens: tc,
		engine: chi.NewRouter(),
	}
	s.registerMiddleware()
	s.registerRoutes()
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

func (s *Server) registerRoutes() {
	handler := api.New(s.cfg, s.router, s.pool, s.store)
	adminHandler := admin.New(s.store, s.pool, s.router, s.tokens)

	s.engine.With(authmw.Token(s.tokens.Lookup)).
		Post("/v1/chat/completions", handler.ChatCompletions)

	s.engine.Get("/v1/models", handler.ListModels)

	s.engine.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	s.engine.Mount("/admin", webui.Handler())

	s.engine.Mount("/api/v1", adminHandler.Routes())
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Server.Port)
	log.Printf("listening on %s (tokens=%d)", addr, s.tokens.Size())
	return http.ListenAndServe(addr, s.engine)
}