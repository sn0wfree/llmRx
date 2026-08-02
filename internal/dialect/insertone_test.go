package dialect

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "d.db")
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	return db
}

// TestInsertOne_SQLiteUsesLastInsertId: SQLite has no RETURNING
// clause, so InsertOne must fall back to LastInsertId and still
// return the generated id.
func TestInsertOne_SQLiteUsesLastInsertId(t *testing.T) {
	db := openSQLite(t)
	id, err := InsertOne(SQLite{}, db, `INSERT INTO t (name) VALUES (?)`, "first")
	if err != nil {
		t.Fatalf("InsertOne: %v", err)
	}
	if id != 1 {
		t.Fatalf("id = %d, want 1", id)
	}
	id2, err := InsertOne(SQLite{}, db, `INSERT INTO t (name) VALUES (?)`, "second")
	if err != nil || id2 != 2 {
		t.Fatalf("second insert: id=%d err=%v, want 2", id2, err)
	}
}

// TestInsertOne_ErrorPath: a failing insert returns the error.
func TestInsertOne_ErrorPath(t *testing.T) {
	db := openSQLite(t)
	// Missing table -> error.
	if _, err := InsertOne(SQLite{}, db, `INSERT INTO nope (x) VALUES (?)`, 1); err == nil {
		t.Fatal("insert into missing table must error")
	}
}

// TestInsertOneContext_SQLite: context variant behaves identically
// on SQLite (ExecContext + LastInsertId).
func TestInsertOneContext_SQLite(t *testing.T) {
	db := openSQLite(t)
	id, err := InsertOneContext(SQLite{}, db, context.Background(),
		`INSERT INTO t (name) VALUES (?)`, "ctx-insert")
	if err != nil {
		t.Fatalf("InsertOneContext: %v", err)
	}
	if id != 1 {
		t.Fatalf("id = %d, want 1", id)
	}
}

// TestInsertOne_PostgresReturnsEarly: on a dialect with a RETURNING
// clause the query would be appended — with a dead connection the
// error surfaces (exercises the RETURNING branch).
func TestInsertOne_PostgresReturnsEarly(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://u:x@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	// The RETURNING branch must be taken and fail cleanly (dead
	// connection) rather than falling into LastInsertId.
	if _, err := InsertOne(Postgres{}, db, `INSERT INTO t (name) VALUES (?)`, "x"); err == nil {
		t.Fatal("dead postgres connection must error")
	}
}

// TestParseBoolMethodForms: the Dialect-interface ParseBool methods
// delegate to the tolerant package-level converter.
func TestParseBoolMethodForms(t *testing.T) {
	var d Dialect = SQLite{}
	if !d.ParseBool(int64(1)) || d.ParseBool(int64(0)) {
		t.Fatal("SQLite.ParseBool(int64)")
	}
	d = Postgres{}
	if !d.ParseBool(true) || d.ParseBool(false) {
		t.Fatal("Postgres.ParseBool(bool)")
	}
	if !d.ParseBool([]byte("true")) || !d.ParseBool("t") || d.ParseBool("0") {
		t.Fatal("Postgres.ParseBool(string forms)")
	}
}

