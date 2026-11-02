package store

import (
	"time"
)

// EdgeClientRepository persists enrolled edge clients and their
// single-use enrollment codes (P15 phase 3).
//
// Secrets are stored as sha256 hashes, never in plaintext: a database
// dump must not yield usable credentials. See docs/P15-CLIENT.md §22.4
// for why sha256 rather than Argon2id.
type EdgeClientRepository interface {
	// CreateEnrollToken stores a one-time enrollment code. Only the hash
	// is persisted; the caller keeps the plaintext long enough to show
	// it to the operator once.
	CreateEnrollToken(tokenHash, clientName string, expiresAt time.Time) error

	// ConsumeEnrollToken atomically marks a code used and returns its
	// client name. It returns ErrNotFound when the code is unknown,
	// already used, or expired — the three cases are deliberately
	// indistinguishable to the caller so a prober cannot tell which.
	ConsumeEnrollToken(tokenHash string, usedClient string, now time.Time) (clientName string, err error)

	// CreateEdgeClient registers an enrolled client.
	CreateEdgeClient(client *EdgeClient) error

	// GetEdgeClientBySecret returns the client whose secret hash matches,
	// provided it is not revoked.
	GetEdgeClientBySecret(secretHash string) (*EdgeClient, error)

	// ListEdgeClients returns every registered client, revoked included
	// (the admin page needs to show them).
	ListEdgeClients() ([]EdgeClient, error)

	// RevokeEdgeClient disables a client. Idempotent.
	RevokeEdgeClient(clientID string) error

	// TouchEdgeClient records a heartbeat time.
	TouchEdgeClient(clientID string, at time.Time) error
}

// EdgeClient is a registered gateway-client instance.
type EdgeClient struct {
	ClientID   string    `json:"client_id"`
	Name       string    `json:"name"`
	SecretHash string    `json:"-"` // never serialised
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  time.Time `json:"revoked_at,omitempty"`
}

// Revoked reports whether the client has been disabled.
func (c *EdgeClient) Revoked() bool { return !c.RevokedAt.IsZero() }
