package store_test

import (
	"os"
	"testing"

	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/store/storetest"
)

// TestPostgresSuite runs the shared 88-method semantic suite against
// a real PostgreSQL instance, skipped unless LLMRX_TEST_PG_DSN is
// set. The migration + all CRUD semantics must match SQLite exactly
// — this is the M1 acceptance gate (docs/P12-STORE-ABSTRACTION.md §7).
//
// Local:
//
//	docker run -d --name llmrx-pg -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=llmrx_test -p 5433:5432 postgres:15-alpine
//	LLMRX_TEST_PG_DSN='postgres://postgres:test@127.0.0.1:5433/llmrx_test?sslmode=disable' \
//	  go test -run TestPostgresSuite ./internal/store/
func TestPostgresSuite(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run (see doc comment)")
	}
	storetest.RunSuite(t, func(t *testing.T) store.Store {
		t.Helper()
		p, err := store.OpenPostgres(dsn)
		if err != nil {
			t.Fatalf("OpenPostgres: %v", err)
		}
		t.Cleanup(func() { _ = p.Close() })
		return p
	}, resetPostgres)
}

// resetPostgres truncates every user table so each suite group
// starts from a clean database (SQLite gets a fresh file instead).
func resetPostgres(t *testing.T, st store.Store) {
	t.Helper()
	db := st.RawDB()
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	for _, tbl := range tables {
		if _, err := db.Exec(`TRUNCATE ` + tbl + ` RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
}
