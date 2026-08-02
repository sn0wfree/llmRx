package ratelimit

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sn0wfree/llmRx/internal/dialect"
)

// TestPGWindowBackend_NilDB: construction with a nil connection is
// rejected without touching a database.
func TestPGWindowBackend_NilDB(t *testing.T) {
	if _, err := NewPGWindowBackend(nil, dialect.Postgres{}); err == nil {
		t.Fatal("nil db must be rejected")
	}
}

// TestPGWindowBackend_BadConn: an unreachable database fails
// construction (the CREATE TABLE probe cannot run) — the error
// path, not a panic.
func TestPGWindowBackend_BadConn(t *testing.T) {
	// Port 1 is not listening anywhere; sql.Open is lazy so the
	// failure surfaces on the first Exec.
	db, err := sql.Open("pgx", "postgres://u:x@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := NewPGWindowBackend(db, dialect.Postgres{}); err == nil {
		t.Fatal("unreachable db must fail construction")
	}
}

// TestPGWindowBackend_DegradeToLocal: after construction against a
// live database, closing the connection forces the fail-open
// degrade path — requests keep being served by the local window.
// Requires a real PG (same skip as the other PG tests).
func TestPGWindowBackend_DegradeToLocal(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	b.Reset()

	now := time.Now()
	if ok, _ := b.AllowWindow(1, 10, 0, 0, now); !ok {
		t.Fatal("allow before degrade")
	}

	// Kill the connection: the next window call must fall back to
	// the local memory window (fail-open, still serving).
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	if ok, reason := b.AllowWindow(1, 10, 0, 0, now); !ok {
		t.Fatalf("degraded allow: reason=%q (must fail open)", reason)
	}
	// Account paths also serve locally.
	b.AccountWindow(1, 5, now)
	b.AccountRequestWindow(1, now)

	// TrackedKeys falls back to the local count.
	if n := b.TrackedKeys(); n == 0 {
		t.Fatal("TrackedKeys must report the local window after degrade")
	}
}

// TestPGWindowBackend_MayRecoverProbe: while degraded, the backend
// periodically probes the database; with the connection still dead
// it stays degraded (no panic, no crash).
func TestPGWindowBackend_MayRecoverProbe(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	b.Reset()

	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	now := time.Now()
	// Large ceiling so the local fallback window never fills up
	// across the probe loop; only the degrade state matters here.
	for i := 0; i < recoverProbeCalls+5; i++ {
		b.AllowWindow(1, 1_000_000, 0, 0, now)
	}
	// db.Ping keeps failing -> must still be in degraded mode but
	// serving via the local window.
	if !b.degrade {
		t.Fatal("backend must remain degraded while the db is down")
	}
	if ok, _ := b.AllowWindow(1, 1_000_000, 0, 0, now); !ok {
		t.Fatal("still failing open")
	}
}

// TestPGWindowBackend_CloseIdempotent: Close stops the cleaner
// without panicking when called twice.
func TestPGWindowBackend_CloseIdempotent(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	b.Close()
	b.Close() // must not panic
}
