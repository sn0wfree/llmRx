package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sn0wfree/llmRx/internal/dialect"
)

// Postgres is a full store.Store implementation backed by
// PostgreSQL. Every method is provided by the embedded dbStore,
// which shares SQL bodies with the SQLite backend and translates
// them via dialect.Postgres: '?' -> $N, BIGSERIAL ids, BOOLEAN
// columns, BIGINT timestamps, DOUBLE PRECISION reals and
// INSERT ... RETURNING id. Acceptance = the storetest suite
// (docs/P12-STORE-ABSTRACTION.md).
type Postgres struct {
	dbStore
}

// OpenPostgres opens a PostgreSQL connection, applies the shared
// migrate() (translated by RewriteDDL) and returns a Store backed
// by it. The DSN is passed directly to database/sql.Open and must
// use the pgx driver form, e.g.
// postgres://user:pass@host:5432/llmrx?sslmode=disable.
func OpenPostgres(dsn string) (*Postgres, error) {
	if dsn == "" {
		return nil, errors.New("empty dsn")
	}
	db, err := sql.Open("pgx", withStatementTimeout(dsn))
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// Multi-writer: N gateway replicas share one pool. 25 open
	// connections gives headroom for hot-path reads + ledger writes.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	p := &Postgres{dbStore: dbStore{db: db, d: dialect.Postgres{}}}
	if err := p.Ping(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := p.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

// withStatementTimeout appends `statement_timeout=30000` to the DSN
// connection options if not already present. This bounds every
// statement (reads included — they run through lazy sql.Rows, so a
// Go-side context deadline would fire before Scan) so a stuck or
// deadlocked Postgres can't hang a request goroutine forever.
func withStatementTimeout(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	opts := q.Get("options")
	if opts == "" {
		q.Set("options", "-c statement_timeout=30000")
	} else if !strings.Contains(opts, "statement_timeout") {
		q.Set("options", opts+" -c statement_timeout=30000")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RawQueryRow exposes the underlying connection for backend-specific
// needs (e.g. the sqlite cache). Queries are NOT dialect-translated
// here; callers pass backend-native SQL.
func (p *Postgres) RawQueryRow(query string, args ...any) *sql.Row {
	return p.db.QueryRow(query, args...)
}

// RawQuery is the raw multi-row counterpart of RawQueryRow.
func (p *Postgres) RawQuery(query string, args ...any) (*sql.Rows, error) {
	return p.db.Query(query, args...)
}

// RawDB exposes the underlying *sql.DB (mirrors SQLite).
func (p *Postgres) RawDB() *sql.DB { return p.db }

func init() {
	Register("postgres", func(dsn string) (Store, error) {
		return OpenPostgres(dsn)
	})
}
