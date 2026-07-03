package server

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
)

type Server struct {
	cfg    *config.Config
	router *router.RouterEngine
	pool   *pool.ChannelPool
	engine *chi.Mux
}

func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool) *Server {
	s := &Server{
		cfg:    cfg,
		router: eng,
		pool:   cp,
		engine: chi.NewRouter(),
	}
	s.registerMiddleware()
	s.registerRoutes()
	return s
}

func (s *Server) registerMiddleware() {
	s.engine.Use(middleware.Logger)
	s.engine.Use(middleware.Recoverer)
	s.engine.Use(middleware.RealIP)
	s.engine.Use(middleware.Timeout(120 * time.Second))
	s.engine.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Task-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
}

func (s *Server) registerRoutes() {
	handler := api.New(s.router, s.pool)

	// OpenAI-compatible proxy endpoints
	s.engine.Post("/v1/chat/completions", handler.ChatCompletions)
	s.engine.Get("/v1/models", handler.ListModels)

	// Health check
	s.engine.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Server.Port)
	log.Printf("listening on %s", addr)
	return http.ListenAndServe(addr, s.engine)
}
