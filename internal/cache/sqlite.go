package cache

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// DBCache stores response entries in the store's own database
// (SQLite response_cache table, or the same table on Postgres in
// P12 cluster mode — one shared table across replicas gives a
// cluster-wide hit rate instead of N independent caches).
//
// All SQL goes through the dialect: '?' bind markers, INSERT OR
// REPLACE vs ON CONFLICT, BLOB vs BYTEA.
type DBCache struct {
	db     *sql.DB
	d      dialect.Dialect
	hits   int64
	misses int64
}

// NewDBCache creates a DBCache on the given connection using the
// dialect matching the backend (dialect.SQLite or dialect.Postgres).
func NewDBCache(db *sql.DB, d dialect.Dialect) (*DBCache, error) {
	if err := migrateResponseCache(db, d); err != nil {
		return nil, fmt.Errorf("cache migrate: %w", err)
	}
	return &DBCache{db: db, d: d}, nil
}

// NewSQLiteCache is a convenience wrapper for the SQLite backend
// (kept for compatibility; server code passes the dialect explicitly).
func NewSQLiteCache(db *sql.DB) (*DBCache, error) {
	return NewDBCache(db, dialect.SQLite{})
}

func migrateResponseCache(db *sql.DB, d dialect.Dialect) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS response_cache (
			key         TEXT PRIMARY KEY,
			status_code INTEGER NOT NULL,
			headers     TEXT NOT NULL,
			body        BLOB NOT NULL,
			usage_json  TEXT,
			cost_usd    REAL NOT NULL DEFAULT 0,
			channel_id  INTEGER NOT NULL,
			stored_at   INTEGER NOT NULL,
			expires_at  INTEGER NOT NULL,
			hit_count   INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_response_cache_expires ON response_cache(expires_at)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(d.RewriteDDL(q)); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBCache) Get(_ context.Context, key string) (*Entry, bool, error) {
	row := s.db.QueryRow(s.d.RewriteQuery(
		`SELECT status_code, headers, body, usage_json, cost_usd, channel_id, stored_at, hit_count, expires_at
		 FROM response_cache WHERE key = ?`), key)

	var (
		statusCode int
		headersStr string
		bodyBytes  []byte
		usageJSON  sql.NullString
		costUSD    float64
		channelID  int64
		storedAt   int64
		hitCount   int64
		expiresAt  int64
	)
	if err := row.Scan(&statusCode, &headersStr, &bodyBytes, &usageJSON, &costUSD, &channelID, &storedAt, &hitCount, &expiresAt); err != nil {
		if err == sql.ErrNoRows {
			atomic.AddInt64(&s.misses, 1)
			return nil, false, nil
		}
		atomic.AddInt64(&s.misses, 1)
		logging.Warn("cache db get scan", logging.F("key", key), logging.F("error", err.Error()))
		return nil, false, nil
	}

	if expiresAt > 0 && time.Now().Unix() > expiresAt {
		_, _ = s.db.Exec(s.d.RewriteQuery("DELETE FROM response_cache WHERE key = ?"), key)
		atomic.AddInt64(&s.misses, 1)
		return nil, false, nil
	}

	body, err := gunzip(bodyBytes)
	if err != nil {
		logging.Warn("cache db gunzip", logging.F("key", key), logging.F("error", err.Error()))
		_, _ = s.db.Exec(s.d.RewriteQuery("DELETE FROM response_cache WHERE key = ?"), key)
		atomic.AddInt64(&s.misses, 1)
		return nil, false, nil
	}

	var headers map[string]string
	if err := json.Unmarshal([]byte(headersStr), &headers); err != nil {
		headers = nil
	}

	var usage *provider.Usage
	if usageJSON.Valid && usageJSON.String != "" {
		var u provider.Usage
		if err := json.Unmarshal([]byte(usageJSON.String), &u); err == nil {
			usage = &u
		}
	}

	_, _ = s.db.Exec(s.d.RewriteQuery("UPDATE response_cache SET hit_count = hit_count + 1 WHERE key = ?"), key)
	hitCount++
	atomic.AddInt64(&s.hits, 1)

	entry := &Entry{
		Key:        key,
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
		Usage:      usage,
		CostUSD:    costUSD,
		ChannelID:  channelID,
		StoredAt:   time.Unix(storedAt, 0),
		HitCount:   hitCount,
	}
	return entry, true, nil
}

func (s *DBCache) Set(_ context.Context, e *Entry, ttl time.Duration) error {
	expiresAt := int64(0)
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl).Unix()
	}

	bodyGZ, err := gzipBytes(e.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}

	headersJSON, _ := json.Marshal(e.Headers)

	var usageJSON *string
	if e.Usage != nil {
		b, _ := json.Marshal(e.Usage)
		s := string(b)
		usageJSON = &s
	}

	var q string
	if s.d.ReturningClause() != "" {
		// Postgres: upsert via ON CONFLICT.
		q = `INSERT INTO response_cache
		 (key, status_code, headers, body, usage_json, cost_usd, channel_id, stored_at, expires_at, hit_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET
		   status_code = EXCLUDED.status_code,
		   headers = EXCLUDED.headers,
		   body = EXCLUDED.body,
		   usage_json = EXCLUDED.usage_json,
		   cost_usd = EXCLUDED.cost_usd,
		   channel_id = EXCLUDED.channel_id,
		   stored_at = EXCLUDED.stored_at,
		   expires_at = EXCLUDED.expires_at,
		   hit_count = EXCLUDED.hit_count`
	} else {
		q = `INSERT OR REPLACE INTO response_cache
		 (key, status_code, headers, body, usage_json, cost_usd, channel_id, stored_at, expires_at, hit_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}

	_, err = s.db.Exec(s.d.RewriteQuery(q),
		e.Key, e.StatusCode, string(headersJSON), bodyGZ, usageJSON, e.CostUSD, e.ChannelID,
		time.Now().Unix(), expiresAt, e.HitCount,
	)
	if err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

func (s *DBCache) Delete(_ context.Context, key string) error {
	_, err := s.db.Exec(s.d.RewriteQuery("DELETE FROM response_cache WHERE key = ?"), key)
	return err
}

func (s *DBCache) Purge(_ context.Context) error {
	_, err := s.db.Exec(s.d.RewriteQuery("DELETE FROM response_cache"))
	return err
}

func (s *DBCache) Stats(_ context.Context) (Stats, error) {
	var size int64
	_ = s.db.QueryRow(s.d.RewriteQuery("SELECT COUNT(*) FROM response_cache")).Scan(&size)

	hits := atomic.LoadInt64(&s.hits)
	misses := atomic.LoadInt64(&s.misses)
	rate := 0.0
	if hits+misses > 0 {
		rate = float64(hits) / float64(hits+misses)
	}
	return Stats{
		Size:    size,
		Hits:    hits,
		Misses:  misses,
		HitRate: rate,
	}, nil
}

func (s *DBCache) Close() error {
	return s.db.Close()
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
