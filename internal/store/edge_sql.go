package store

import (
	"fmt"
	"time"
)

// SQL implementations of EdgeClientRepository. Kept in the store
// package (not clientapi) so the dialect helpers stay together and
// Postgres gets them for free via dbStore embedding.

// CreateEnrollToken stores a hashed one-time code.
func (s *dbStore) CreateEnrollToken(tokenHash, clientName string, expiresAt time.Time) error {
	_, err := s.exec(
		`INSERT INTO edge_enroll_tokens(token_hash, client_name, created_at, expires_at, used_at, used_client)
		 VALUES (?, ?, ?, ?, 0, '')`,
		tokenHash, clientName, toUnix(time.Now().UTC()), toUnix(expiresAt))
	if err != nil {
		return fmt.Errorf("create enroll token: %w", err)
	}
	return nil
}

// ConsumeEnrollToken marks a code used and returns its client name.
//
// The conditional UPDATE is what makes this single-use: only one caller
// can flip used_at from 0, so two concurrent /enroll requests with the
// same code cannot both succeed. Unknown, expired, and already-used
// codes all return ErrNotFound so callers cannot distinguish them.
func (s *dbStore) ConsumeEnrollToken(tokenHash string, usedClient string, now time.Time) (string, error) {
	nowUnix := toUnix(now)

	// Read the row first to check expiry with a Go comparison (avoids
	// dialect-specific date math).
	var clientName string
	var expiresAt int64
	var usedAt int64
	err := s.queryRow(
		`SELECT client_name, expires_at, used_at FROM edge_enroll_tokens WHERE token_hash = ?`,
		tokenHash).Scan(&clientName, &expiresAt, &usedAt)
	if err != nil {
		return "", ErrNotFound
	}
	if usedAt != 0 || expiresAt <= nowUnix {
		return "", ErrNotFound
	}

	// Atomic claim: the WHERE clause re-checks used_at, so a race that
	// slipped between the SELECT and this UPDATE still loses here.
	res, err := s.exec(
		`UPDATE edge_enroll_tokens SET used_at = ?, used_client = ?
		 WHERE token_hash = ? AND used_at = 0 AND expires_at > ?`,
		nowUnix, usedClient, tokenHash, nowUnix)
	if err != nil {
		return "", fmt.Errorf("consume enroll token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return clientName, nil
}

// CreateEdgeClient registers an enrolled client.
func (s *dbStore) CreateEdgeClient(c *EdgeClient) error {
	if c.ClientID == "" || c.Name == "" || c.SecretHash == "" {
		return fmt.Errorf("edge client requires id, name and secret_hash")
	}
	created := c.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := s.exec(
		`INSERT INTO edge_clients(client_id, name, secret_hash, created_at, last_seen_at, revoked_at)
		 VALUES (?, ?, ?, ?, 0, 0)`,
		c.ClientID, c.Name, c.SecretHash, toUnix(created))
	if err != nil {
		return fmt.Errorf("create edge client: %w", err)
	}
	c.CreatedAt = created
	return nil
}

// GetEdgeClientBySecret returns the live client for a secret hash.
// Revoked clients surface as ErrNotFound so the auth path has one
// failure mode.
func (s *dbStore) GetEdgeClientBySecret(secretHash string) (*EdgeClient, error) {
	var c EdgeClient
	var createdAt, lastSeen, revokedAt int64
	err := s.queryRow(
		`SELECT client_id, name, secret_hash, created_at, last_seen_at, revoked_at
		 FROM edge_clients WHERE secret_hash = ?`,
		secretHash).Scan(&c.ClientID, &c.Name, &c.SecretHash, &createdAt, &lastSeen, &revokedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	c.CreatedAt = fromUnix(createdAt)
	if lastSeen != 0 {
		c.LastSeenAt = fromUnix(lastSeen)
	}
	if revokedAt != 0 {
		c.RevokedAt = fromUnix(revokedAt)
	}
	if c.Revoked() {
		return nil, ErrNotFound
	}
	return &c, nil
}

// ListEdgeClients returns every client, including revoked ones.
func (s *dbStore) ListEdgeClients() ([]EdgeClient, error) {
	rows, err := s.query(
		`SELECT client_id, name, secret_hash, created_at, last_seen_at, revoked_at
		 FROM edge_clients ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EdgeClient
	for rows.Next() {
		var c EdgeClient
		var createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(&c.ClientID, &c.Name, &c.SecretHash, &createdAt, &lastSeen, &revokedAt); err != nil {
			return nil, err
		}
		c.CreatedAt = fromUnix(createdAt)
		if lastSeen != 0 {
			c.LastSeenAt = fromUnix(lastSeen)
		}
		if revokedAt != 0 {
			c.RevokedAt = fromUnix(revokedAt)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeEdgeClient disables a client. Idempotent.
func (s *dbStore) RevokeEdgeClient(clientID string) error {
	_, err := s.exec(
		`UPDATE edge_clients SET revoked_at = ? WHERE client_id = ? AND revoked_at = 0`,
		toUnix(time.Now().UTC()), clientID)
	return err
}

// TouchEdgeClient records a heartbeat. Best effort: a failure here must
// not fail the heartbeat itself.
func (s *dbStore) TouchEdgeClient(clientID string, at time.Time) error {
	_, err := s.exec(`UPDATE edge_clients SET last_seen_at = ? WHERE client_id = ?`,
		toUnix(at), clientID)
	return err
}
