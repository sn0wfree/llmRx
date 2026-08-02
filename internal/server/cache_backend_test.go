package server

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/config"
)

func TestInitResponseCache_Memory(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{CacheBackend: "memory", CacheMaxItems: 10}}}
	c := s.initResponseCache()
	if c == nil {
		t.Fatal("memory backend returned nil")
	}
	if _, ok := c.(*cache.MemoryCache); !ok {
		t.Fatalf("memory backend = %T", c)
	}
}

func TestInitResponseCache_Disabled(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{}}}
	if c := s.initResponseCache(); c != nil {
		t.Fatalf("empty backend must be nil, got %T", c)
	}
}

func TestInitResponseCache_UnknownBackend(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{CacheBackend: "bogus"}}}
	if c := s.initResponseCache(); c != nil {
		t.Fatalf("unknown backend must be nil, got %T", c)
	}
}

func TestInitResponseCache_SQLiteStoreUsesDBCache(t *testing.T) {
	st := openSQLiteStore(t)
	s := &Server{
		cfg:   &config.Config{Server: config.ServerConfig{CacheBackend: "sqlite"}, Database: config.DatabaseConfig{Driver: "sqlite"}},
		store: st,
	}
	c := s.initResponseCache()
	if c == nil {
		t.Fatal("sqlite backend with a SQLite store returned nil")
	}
	if _, ok := c.(*cache.DBCache); !ok {
		t.Fatalf("sqlite backend = %T, want *DBCache", c)
	}
}
