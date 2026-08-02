package logstore

import (
	"testing"
)

// TestNewPostgresDriver_EmptyDSN: empty DSN is rejected up front.
func TestNewPostgresDriver_EmptyDSN(t *testing.T) {
	if _, err := NewPostgresDriver(""); err == nil {
		t.Fatal("empty dsn must be rejected")
	}
}

// TestNewPostgresDriver_BadDSN: an unreachable database fails the
// ping during construction (no panic, clean error).
func TestNewPostgresDriver_BadDSN(t *testing.T) {
	if _, err := NewPostgresDriver("postgres://u:x@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1"); err == nil {
		t.Fatal("unreachable dsn must fail construction")
	}
}
