package cache

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sn0wfree/llmRx/internal/dialect"
)

// TestPostgresCache runs the shared DBCache behaviour against a
// real Postgres (the store-side table in cluster mode). Skipped
// unless LLMRX_TEST_PG_DSN is set.
func TestPostgresCache(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DROP TABLE IF EXISTS response_cache`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	c, err := NewDBCache(db, dialect.Postgres{})
	if err != nil {
		t.Fatalf("NewDBCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()

	// Miss.
	if _, ok, err := c.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("Get miss: ok=%v err=%v", ok, err)
	}

	// Set + Get roundtrip.
	entry := &Entry{
		Key:        "k1",
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`{"hello":"world"}`),
		ChannelID:  7,
		StoredAt:   time.Now(),
	}
	if err := c.Set(ctx, entry, 60*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := c.Get(ctx, "k1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if string(got.Body) != string(entry.Body) || got.StatusCode != 200 ||
		got.Headers["Content-Type"] != "application/json" || got.ChannelID != 7 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.HitCount != 1 {
		t.Fatalf("hit_count = %d, want 1", got.HitCount)
	}

	// Upsert overwrites (ON CONFLICT path).
	entry.Body = []byte(`{"updated":true}`)
	if err := c.Set(ctx, entry, 60*time.Second); err != nil {
		t.Fatalf("Set(2): %v", err)
	}
	if got, ok, _ := c.Get(ctx, "k1"); !ok || string(got.Body) != `{"updated":true}` {
		t.Fatalf("upsert failed: %+v", got)
	}

	// Delete.
	if err := c.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := c.Get(ctx, "k1"); ok {
		t.Fatal("entry should be gone after Delete")
	}

	// Purge.
	if err := c.Set(ctx, &Entry{Key: "a", StatusCode: 200, Body: []byte("x"), StoredAt: time.Now()}, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Purge(ctx); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	st, err := c.Stats(ctx)
	if err != nil || st.Size != 0 {
		t.Fatalf("Stats after purge: %+v err=%v", st, err)
	}
}
