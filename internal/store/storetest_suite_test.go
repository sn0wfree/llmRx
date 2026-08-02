package store_test

import (
	"path/filepath"
	"testing"

	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/store/storetest"
)

// TestSQLiteSuite runs the shared 88-method semantic suite against
// the SQLite backend. This is the same suite the Postgres backend
// must pass in P12 M1.
func TestSQLiteSuite(t *testing.T) {
	storetest.RunSuite(t, func(t *testing.T) store.Store {
		t.Helper()
		dsn := filepath.Join(t.TempDir(), "suite.db")
		s, err := store.OpenSQLite(dsn)
		if err != nil {
			t.Fatalf("OpenSQLite: %v", err)
		}
		return s
	})
}
