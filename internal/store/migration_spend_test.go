package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestIncrementTokenSpend_ZeroAmount(t *testing.T) {
	s := openTemp(t)

	tok := mkToken(t, s, "t1", "k1")

	err := s.IncrementTokenSpend(tok.ID, 0)
	if err != nil {
		t.Fatalf("zero amount should be no-op, got err: %v", err)
	}

	got, err := s.GetTokenByID(tok.ID)
	if err != nil {
		t.Fatalf("GetTokenByID: %v", err)
	}
	if got.UsedUSD != 0 {
		t.Errorf("expected 0 used_usd, got %f", got.UsedUSD)
	}
}

func TestIncrementTokenSpend_NotFound(t *testing.T) {
	s := openTemp(t)

	err := s.IncrementTokenSpend(9999, 1.5)
	if err == nil {
		t.Fatal("expected error for non-existent token")
	}
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestIncrementTokenSpend_Success(t *testing.T) {
	s := openTemp(t)

	tok := mkToken(t, s, "t1", "k1")

	if err := s.IncrementTokenSpend(tok.ID, 1.5); err != nil {
		t.Fatalf("IncrementTokenSpend: %v", err)
	}
	if err := s.IncrementTokenSpend(tok.ID, 0.5); err != nil {
		t.Fatalf("IncrementTokenSpend 2nd: %v", err)
	}

	got, err := s.GetTokenByID(tok.ID)
	if err != nil {
		t.Fatalf("GetTokenByID: %v", err)
	}
	if got.UsedUSD < 1.99 || got.UsedUSD > 2.01 {
		t.Errorf("expected ~2.0 used_usd, got %f", got.UsedUSD)
	}
}

func TestMigrate_OldSchemaGetsNewColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	oldDB, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, err = oldDB.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role INTEGER NOT NULL DEFAULT 0,
		status INTEGER NOT NULL DEFAULT 1,
		session_token TEXT NOT NULL DEFAULT '',
		session_exp INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		plan_id INTEGER NOT NULL DEFAULT 0,
		key TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0,
		rpm INTEGER NOT NULL DEFAULT 0,
		tpm INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		provider TEXT NOT NULL,
		protocol TEXT NOT NULL DEFAULT 'openai',
		base_url TEXT NOT NULL,
		models TEXT NOT NULL,
		intents TEXT NOT NULL DEFAULT '[]',
		priority INTEGER NOT NULL DEFAULT 0,
		input_price REAL NOT NULL DEFAULT 0,
		output_price REAL NOT NULL DEFAULT 0,
		cached_input_discount REAL NOT NULL DEFAULT 0.1,
		circuit_breaker TEXT NOT NULL DEFAULT '{}',
		status INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);`)
	if err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	oldDB.Close()

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite on old schema: %v", err)
	}
	defer s.Close()

	rows, err := s.db.Query(`PRAGMA table_info(tokens)`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	colSet := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		colSet[name] = true
	}

	required := []string{"used_usd", "key_ciphertext"}
	for _, col := range required {
		if !colSet[col] {
			t.Errorf("after migration, tokens should have column %q", col)
		}
	}

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()

	rows2, err := s2.db.Query(`PRAGMA table_info(tokens)`)
	if err != nil {
		t.Fatalf("pragma 2: %v", err)
	}
	defer rows2.Close()
	count := 0
	for rows2.Next() {
		count++
	}
	if count < len(required)+5 {
		t.Errorf("expected many columns after migration, got %d", count)
	}
}

func TestAddColumnIfMissing_AlreadyExists(t *testing.T) {
	s := openTemp(t)

	err := s.addColumnIfMissing("tokens", "used_usd", "REAL NOT NULL DEFAULT 0")
	if err != nil {
		t.Fatalf("addColumnIfMissing on existing col should no-op, got: %v", err)
	}
}

func TestAddColumnIfMissing_NewTable(t *testing.T) {
	s := openTemp(t)

	_, err := s.db.Exec(`CREATE TABLE test_addcol (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := s.addColumnIfMissing("test_addcol", "new_col", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("addColumnIfMissing: %v", err)
	}

	rows, err := s.db.Query(`PRAGMA table_info(test_addcol)`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "new_col" {
			found = true
		}
	}
	if !found {
		t.Error("new_col should exist after addColumnIfMissing")
	}
}

func mkToken(t *testing.T, s *SQLite, name, key string) *model.Token {
	t.Helper()
	tok := &model.Token{
		Key:    key,
		Name:   name,
		Status: model.TokenActive,
		RPM:    100,
		TPM:    10000,
	}
	if err := s.CreateToken(tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return tok
}
