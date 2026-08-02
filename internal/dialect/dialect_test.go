package dialect

import (
	"testing"
)

func TestPostgresRewriteDDL(t *testing.T) {
	in := `CREATE TABLE IF NOT EXISTS alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			threshold REAL NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			status INTEGER NOT NULL DEFAULT 1,
			last_fired_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`
	want := `CREATE TABLE IF NOT EXISTS alerts (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			threshold DOUBLE PRECISION NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true,
			status INTEGER NOT NULL DEFAULT 1,
			last_fired_at BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL
		)`
	if got := (Postgres{}).RewriteDDL(in); got != want {
		t.Fatalf("RewriteDDL:\n got: %s\nwant: %s", got, want)
	}
	if got := (SQLite{}).RewriteDDL(in); got != in {
		t.Fatalf("SQLite RewriteDDL should be identity")
	}
}

func TestSQLiteRewriteQuery(t *testing.T) {
	q := `INSERT INTO channels(name, provider) VALUES (?, ?)`
	if got := (SQLite{}).RewriteQuery(q); got != q {
		t.Fatalf("SQLite RewriteQuery should be identity, got %q", got)
	}
}

func TestPostgresRewriteQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{`INSERT INTO channels(name, provider) VALUES (?, ?)`,
			`INSERT INTO channels(name, provider) VALUES ($1, $2)`},
		{`SELECT id FROM keys WHERE channel_id = ? ORDER BY id`,
			`SELECT id FROM keys WHERE channel_id = $1 ORDER BY id`},
		{`UPDATE keys SET key='', key_ciphertext=? WHERE id=?`,
			`UPDATE keys SET key='', key_ciphertext=$1 WHERE id=$2`},
		// '?' inside a string literal must be preserved.
		{`SELECT id FROM channels WHERE name = 'a?b' AND status = ?`,
			`SELECT id FROM channels WHERE name = 'a?b' AND status = $1`},
		// SQLite '' escape inside a literal.
		{`SELECT id FROM guardrails WHERE config = 'it''s ? here' AND priority > ?`,
			`SELECT id FROM guardrails WHERE config = 'it''s ? here' AND priority > $1`},
		{`SELECT 1`, `SELECT 1`},
	}
	for _, c := range cases {
		if got := (Postgres{}).RewriteQuery(c.in); got != c.want {
			t.Fatalf("RewriteQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSQLitePlaceholders(t *testing.T) {
	got := Placeholders(SQLite{}, 3)
	if got != "(?, ?, ?)" {
		t.Fatalf("SQLite Placeholders(3) = %q, want (?, ?, ?)", got)
	}
	if got := Placeholders(SQLite{}, 0); got != "" {
		t.Fatalf("SQLite Placeholders(0) = %q, want empty", got)
	}
}

func TestPostgresPlaceholders(t *testing.T) {
	got := Placeholders(Postgres{}, 3)
	if got != "($1, $2, $3)" {
		t.Fatalf("Postgres Placeholders(3) = %q, want ($1, $2, $3)", got)
	}
	got = Placeholders(Postgres{}, 2)
	if got != "($1, $2)" {
		t.Fatalf("Postgres Placeholders(2) = %q, want ($1, $2)", got)
	}
}

func TestSQLiteBool(t *testing.T) {
	d := SQLite{}
	if d.Bool(true) != int64(1) || d.Bool(false) != int64(0) {
		t.Fatalf("SQLite.Bool wrong: %v / %v", d.Bool(true), d.Bool(false))
	}
	if d.ReturningClause() != "" {
		t.Fatalf("SQLite ReturningClause should be empty")
	}
	if d.AutoIncrement() != "INTEGER PRIMARY KEY AUTOINCREMENT" {
		t.Fatalf("SQLite AutoIncrement wrong: %q", d.AutoIncrement())
	}
	if got := d.AddColumnIfMissing("users", "col", "INTEGER"); got != "" {
		t.Fatalf("SQLite AddColumnIfMissing should be empty, got %q", got)
	}
	if got := d.BoolColumn("enabled INTEGER NOT NULL DEFAULT 1"); got != "enabled INTEGER NOT NULL DEFAULT 1" {
		t.Fatalf("SQLite BoolColumn should passthrough, got %q", got)
	}
}

func TestPostgresBool(t *testing.T) {
	d := Postgres{}
	if d.Bool(true) != true || d.Bool(false) != false {
		t.Fatalf("Postgres.Bool wrong")
	}
	if d.ReturningClause() != " RETURNING id" {
		t.Fatalf("Postgres ReturningClause wrong: %q", d.ReturningClause())
	}
	if d.AutoIncrement() != "BIGSERIAL PRIMARY KEY" {
		t.Fatalf("Postgres AutoIncrement wrong: %q", d.AutoIncrement())
	}
	got := d.BoolColumn("enabled INTEGER NOT NULL DEFAULT 1")
	if got != "enabled BOOLEAN NOT NULL DEFAULT true" {
		t.Fatalf("Postgres BoolColumn = %q, want BOOLEAN DEFAULT true", got)
	}
	got = d.BoolColumn("is_default INTEGER NOT NULL DEFAULT 0")
	if got != "is_default BOOLEAN NOT NULL DEFAULT false" {
		t.Fatalf("Postgres BoolColumn = %q, want BOOLEAN DEFAULT false", got)
	}
	got = d.AddColumnIfMissing("users", "enabled", "INTEGER NOT NULL DEFAULT 1")
	if got != "ALTER TABLE users ADD COLUMN IF NOT EXISTS enabled INTEGER NOT NULL DEFAULT 1" {
		t.Fatalf("Postgres AddColumnIfMissing = %q", got)
	}
}

func TestParseBool(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{bool(true), true},
		{bool(false), false},
		{int64(1), true},
		{int64(0), false},
		{int(1), true},
		{float64(1), true},
		{[]byte("1"), true},
		{[]byte("0"), false},
		{[]byte("true"), true},
		{"false", false},
		{"t", true},
		{"yes", true},
		{nil, false},
		{"garbage", false},
	}
	for _, c := range cases {
		if got := ParseBool(c.in); got != c.want {
			t.Fatalf("ParseBool(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
