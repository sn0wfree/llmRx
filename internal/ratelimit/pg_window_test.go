package ratelimit

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sn0wfree/llmRx/internal/dialect"
)

// testPGDB returns a *sql.DB when LLMRX_TEST_PG_DSN is set, else
// nil (tests are skipped).
func testPGDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run PG backend tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DROP TABLE IF EXISTS ratelimit_buckets`); err != nil {
		t.Fatalf("cleanup table: %v", err)
	}
	return db
}

func TestPGWindowBackend_RPM(t *testing.T) {
	db := testPGDB(t)
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	b.Reset()

	now := time.Now()
	// rpm=2: first two allowed, third rejected.
	if ok, reason := b.AllowWindow(42, 2, 0, 10, now); !ok {
		t.Fatalf("allow 1: %s", reason)
	}
	if ok, reason := b.AllowWindow(42, 2, 0, 10, now); !ok {
		t.Fatalf("allow 2: %s", reason)
	}
	if ok, reason := b.AllowWindow(42, 2, 0, 10, now); ok || reason != "rpm exceeded" {
		t.Fatalf("allow 3: ok=%v reason=%q, want rpm exceeded", ok, reason)
	}
	// A different key has its own bucket.
	if ok, reason := b.AllowWindow(43, 2, 0, 10, now); !ok {
		t.Fatalf("other key: %s", reason)
	}
	// Two minutes later both prior buckets are out of range.
	if ok, reason := b.AllowWindow(42, 2, 0, 10, now.Add(2*time.Minute)); !ok {
		t.Fatalf("later minute allow: %s", reason)
	}
}

func TestPGWindowBackend_TPM(t *testing.T) {
	db := testPGDB(t)
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	b.Reset()

	now := time.Now()
	// tpm=100: 60 + 50 = 110 > 100 -> second request rejected.
	if ok, _ := b.AllowWindow(7, 0, 100, 60, now); !ok {
		t.Fatal("allow 60 tokens")
	}
	if ok, reason := b.AllowWindow(7, 0, 100, 50, now); ok || reason != "tpm exceeded" {
		t.Fatalf("ok=%v reason=%q, want tpm exceeded", ok, reason)
	}
	// AccountWindow credits completion tokens and pushes over.
	b.AccountWindow(7, 60, now)
	if ok, reason := b.AllowWindow(7, 0, 100, 10, now); ok || reason != "tpm exceeded" {
		t.Fatalf("after account: ok=%v reason=%q, want tpm exceeded", ok, reason)
	}
}

func TestPGWindowBackend_AccountRequest(t *testing.T) {
	db := testPGDB(t)
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	b.Reset()

	now := time.Now()
	if ok, _ := b.AllowWindow(9, 2, 0, 0, now); !ok {
		t.Fatal("allow")
	}
	b.AccountRequestWindow(9, now)
	// 2 used (allow + account), third rejected.
	if ok, reason := b.AllowWindow(9, 2, 0, 0, now); ok || reason != "rpm exceeded" {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

func TestPGWindowBackend_TrackedKeys(t *testing.T) {
	db := testPGDB(t)
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	b.Reset()

	now := time.Now()
	// Unlimited calls never write buckets; use bounded ceilings so
	// rows are created.
	b.AllowWindow(1, 100, 0, 5, now)
	b.AllowWindow(2, 100, 0, 5, now)
	if n := b.TrackedKeys(); n != 2 {
		t.Fatalf("TrackedKeys = %d, want 2", n)
	}
	b.Reset()
	if n := b.TrackedKeys(); n != 0 {
		t.Fatalf("TrackedKeys after reset = %d, want 0", n)
	}
}

func TestPGWindowBackend_Unlimited(t *testing.T) {
	db := testPGDB(t)
	b, err := NewPGWindowBackend(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewPGWindowBackend: %v", err)
	}
	defer b.Close()
	if ok, reason := b.AllowWindow(3, 0, 0, 1, time.Now()); !ok {
		t.Fatalf("unlimited: %s", reason)
	}
}
