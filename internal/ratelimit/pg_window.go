package ratelimit

import (
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/logging"
)

// PGWindowBackend shares RPM/TPM counters across every gateway
// replica using a minute-bucket table in Postgres:
//
//	ratelimit_buckets(key_id BIGINT, window_min BIGINT, requests INT, tokens BIGINT,
//	                  PRIMARY KEY (key_id, window_min))
//
// Each key's current + previous minute buckets are read under a
// FOR UPDATE row lock inside a transaction, checked against
// rpm/tpm, then upserted atomically. The 60-second boundary
// granularity (vs MemoryBackend's exact sliding window) is the
// accepted P12 approximation.
//
// Failure behaviour: when the database is unreachable the backend
// fails OPEN to a local MemoryBackend so the gateway keeps serving
// (limits become per-node during the outage — the documented P12
// trade-off, P12-CLUSTER.md §Risks).
type PGWindowBackend struct {
	db     *sql.DB
	d      dialect.Dialect
	memory *MemoryBackend // fail-open fallback

	// degrade indicates the backend is in local-fallback mode.
	degrade bool

	// degradeTries counts local-fallback calls; every
	// recoverProbeCalls the backend probes the DB to restore
	// cluster-wide accounting after a transient outage.
	degradeTries int

	stopClean chan struct{}
	closeOnce sync.Once
}

// recoverProbeCalls is how many fallback calls happen before the
// backend attempts to recover the shared database path.
const recoverProbeCalls = 100

// NewPGWindowBackend creates a PG-backed window backend. The
// ratelimit_buckets table is created if missing. A background
// cleaner removes buckets older than 2 minutes every sweep.
func NewPGWindowBackend(db *sql.DB, d dialect.Dialect) (*PGWindowBackend, error) {
	if db == nil {
		return nil, errors.New("ratelimit: nil db")
	}
	b := &PGWindowBackend{
		db:        db,
		d:         d,
		memory:    NewMemoryBackend(),
		stopClean: make(chan struct{}),
	}
	// MySQL-style IF NOT EXISTS is portable here; the table has no
	// SQLite-specific columns, but routing through the dialect keeps
	// the '?' rewrite and future SQL-family backends consistent.
	q := d.RewriteQuery(`CREATE TABLE IF NOT EXISTS ratelimit_buckets (
		key_id BIGINT NOT NULL,
		window_min BIGINT NOT NULL,
		requests INTEGER NOT NULL DEFAULT 0,
		tokens BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (key_id, window_min)
	)`)
	if _, err := db.Exec(q); err != nil {
		return nil, err
	}
	go b.cleanupLoop()
	return b, nil
}

// Close stops the background cleaner. Idempotent.
func (b *PGWindowBackend) Close() {
	b.closeOnce.Do(func() { close(b.stopClean) })
}

func (b *PGWindowBackend) cleanupLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			cutoff := time.Now().Unix()/60 - 2
			_, _ = b.db.Exec(b.d.RewriteQuery(`DELETE FROM ratelimit_buckets WHERE window_min < ?`), cutoff)
		case <-b.stopClean:
			return
		}
	}
}

// degradeToLocal switches to the fail-open fallback and logs the
// first occurrence.
func (b *PGWindowBackend) degradeToLocal(reason string) {
	if !b.degrade {
		b.degrade = true
		b.degradeTries = 0
		logging.Warn("ratelimit: pg backend degraded to local window", logging.F("reason", reason))
	}
}

// mayRecover attempts to restore the shared backend after a
// degradation. Called on fallback paths; every recoverProbeCalls
// calls try a lightweight ping.
func (b *PGWindowBackend) mayRecover() {
	b.degradeTries++
	if b.degradeTries < recoverProbeCalls {
		return
	}
	b.degradeTries = 0
	if err := b.db.Ping(); err != nil {
		return
	}
	b.degrade = false
	logging.Info("ratelimit: pg backend recovered")
}

// AllowWindow checks rpm/tpm against the key's current+previous
// minute buckets and records the request atomically. On any
// database error the backend degrades to the local fallback and
// retries nothing (fail-open).
func (b *PGWindowBackend) AllowWindow(key int64, rpm, tpm int, promptTokens int, now time.Time) (bool, string) {
	if rpm == 0 && tpm == 0 {
		return true, ""
	}
	if b.degrade {
		b.mayRecover()
		return b.memory.AllowWindow(key, rpm, tpm, promptTokens, now)
	}
	curMin := now.Unix() / 60
	prevMin := curMin - 1

	tx, err := b.db.Begin()
	if err != nil {
		b.degradeToLocal(err.Error())
		return b.memory.AllowWindow(key, rpm, tpm, promptTokens, now)
	}
	defer func() { _ = tx.Rollback() }()

	var curReqs, curToks int64
	err = tx.QueryRow(b.d.RewriteQuery(
		`SELECT requests, tokens FROM ratelimit_buckets WHERE key_id = ? AND window_min = ? FOR UPDATE`),
		key, curMin).Scan(&curReqs, &curToks)
	if err != nil && err != sql.ErrNoRows {
		b.degradeToLocal(err.Error())
		return b.memory.AllowWindow(key, rpm, tpm, promptTokens, now)
	}

	var prevReqs, prevToks int64
	_ = tx.QueryRow(b.d.RewriteQuery(
		`SELECT requests, tokens FROM ratelimit_buckets WHERE key_id = ? AND window_min = ?`),
		key, prevMin).Scan(&prevReqs, &prevToks)

	totalReqs := curReqs + prevReqs
	if rpm > 0 && totalReqs >= int64(rpm) {
		return false, "rpm exceeded"
	}
	totalToks := curToks + prevToks
	if tpm > 0 && totalToks+int64(promptTokens) > int64(tpm) {
		return false, "tpm exceeded"
	}

	if _, err := tx.Exec(b.d.RewriteQuery(
		`INSERT INTO ratelimit_buckets (key_id, window_min, requests, tokens)
		 VALUES (?, ?, 1, ?)
		 ON CONFLICT (key_id, window_min) DO UPDATE
		   SET requests = ratelimit_buckets.requests + 1,
		       tokens = ratelimit_buckets.tokens + EXCLUDED.tokens`),
		key, curMin, promptTokens); err != nil {
		b.degradeToLocal(err.Error())
		return b.memory.AllowWindow(key, rpm, tpm, promptTokens, now)
	}
	if err := tx.Commit(); err != nil {
		b.degradeToLocal(err.Error())
		return b.memory.AllowWindow(key, rpm, tpm, promptTokens, now)
	}
	return true, ""
}

// AccountWindow credits completion tokens to the key's current
// minute bucket (upsert, no limit check).
func (b *PGWindowBackend) AccountWindow(key int64, extraTokens int, now time.Time) {
	if extraTokens <= 0 {
		return
	}
	if b.degrade {
		b.mayRecover()
		b.memory.AccountWindow(key, extraTokens, now)
		return
	}
	curMin := now.Unix() / 60
	if _, err := b.db.Exec(b.d.RewriteQuery(
		`INSERT INTO ratelimit_buckets (key_id, window_min, requests, tokens)
		 VALUES (?, ?, 0, ?)
		 ON CONFLICT (key_id, window_min) DO UPDATE
		   SET tokens = ratelimit_buckets.tokens + EXCLUDED.tokens`),
		key, curMin, extraTokens); err != nil {
		b.degradeToLocal(err.Error())
	}
}

// AccountRequestWindow records one RPM-only request (MCP calls).
func (b *PGWindowBackend) AccountRequestWindow(key int64, now time.Time) {
	if b.degrade {
		b.mayRecover()
		b.memory.AccountRequestWindow(key, now)
		return
	}
	curMin := now.Unix() / 60
	if _, err := b.db.Exec(b.d.RewriteQuery(
		`INSERT INTO ratelimit_buckets (key_id, window_min, requests, tokens)
		 VALUES (?, ?, 1, 0)
		 ON CONFLICT (key_id, window_min) DO UPDATE
		   SET requests = ratelimit_buckets.requests + 1`),
		key, curMin); err != nil {
		b.degradeToLocal(err.Error())
	}
}

// Reset clears all window state in the shared table.
func (b *PGWindowBackend) Reset() {
	_, _ = b.db.Exec(b.d.RewriteQuery(`DELETE FROM ratelimit_buckets`))
	b.memory.Reset()
}

// TrackedKeys counts distinct keys in the shared table. On error
// the local fallback's count is returned.
func (b *PGWindowBackend) TrackedKeys() int {
	var n int
	err := b.db.QueryRow(b.d.RewriteQuery(`SELECT COUNT(DISTINCT key_id) FROM ratelimit_buckets`)).Scan(&n)
	if err != nil {
		return b.memory.TrackedKeys()
	}
	return n
}
