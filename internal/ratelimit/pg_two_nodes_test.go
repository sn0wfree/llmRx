package ratelimit

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sn0wfree/llmRx/internal/dialect"
)

// TestTwoNodesShareCounters: 两个独立 backend（两个节点）共享同一
// PG 表——node A 用完 rpm，node B 立即被限。
func TestTwoNodesShareCounters(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN")
	}
	dbA, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	_, _ = dbA.Exec(`DROP TABLE IF EXISTS ratelimit_buckets`)

	nodeA, err := NewPGWindowBackend(dbA, dialect.Postgres{})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeA.Close()
	nodeB, err := NewPGWindowBackend(dbB, dialect.Postgres{})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeB.Close()
	nodeA.Reset()

	now := time.Now()
	// node A 消耗掉 rpm=1
	if ok, reason := nodeA.AllowWindow(77, 1, 0, 0, now); !ok {
		t.Fatalf("nodeA allow: %s", reason)
	}
	// node B 应该被限——计数共享！
	if ok, reason := nodeB.AllowWindow(77, 1, 0, 0, now); ok {
		t.Fatal("nodeB should be rate limited (shared counter)")
	} else if reason != "rpm exceeded" {
		t.Fatalf("nodeB reason: %q", reason)
	}
}
