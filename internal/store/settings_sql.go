package store

import (
	"errors"
	"time"

	"database/sql"
)

func (s *dbStore) RawQueryRow(query string, args ...any) *sql.Row {
	return s.queryRow(query, args...)
}

func (s *dbStore) RawQuery(query string, args ...any) (*sql.Rows, error) {
	return s.query(query, args...)
}

func (s *dbStore) RawDB() *sql.DB { return s.db }

// ---------------- runtime settings ----------------

// GetRuntimeSettings returns the persisted JSON snapshot written by
// SetRuntimeSettings, or (nil, nil) when no row exists yet.
func (s *dbStore) GetRuntimeSettings() ([]byte, error) {
	var raw []byte
	err := s.queryRow(`SELECT settings_json FROM runtime_settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// SetRuntimeSettings upserts the single row. payload must be valid
// JSON; callers should validate before persisting.
func (s *dbStore) SetRuntimeSettings(payload []byte) error {
	now := time.Now().Unix()
	_, err := s.exec(`
		INSERT INTO runtime_settings(id, settings_json, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET settings_json = excluded.settings_json, updated_at = excluded.updated_at
	`, string(payload), now)
	return err
}

// ---------- BYOK (Bring Your Own Key) ----------
//
// Consumer-supplied upstream API keys. When a request arrives with a
// bearer token that isn't in our cache AND the token looks like an
// upstream-provider key (prefix match), the BYOK hook verifies the
// key against the upstream, stores the encrypted ciphertext, and
// proceeds with the request using the consumer's key.
