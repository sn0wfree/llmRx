package clientapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Edge client enrollment (P15 phase 3).
//
// Flow: an admin issues a single-use code (15 min TTL), hands it to the
// operator out of band, and the client exchanges it here for a
// client_id/secret pair. Only hashes are persisted on both sides, so a
// database dump yields nothing usable.

const (
	// EnrollTokenTTL is how long an issued code stays valid. Short
	// enough that a leaked chat message goes stale, long enough to
	// paste it into a colleague's terminal.
	EnrollTokenTTL = 15 * time.Minute

	enrollTokenPrefix = "enr_"
	clientIDPrefix    = "ec_"
	clientSecretPfx   = "ecs_"
)

// EnrollStore is the narrow persistence surface enrollment needs.
type EnrollStore interface {
	CreateEnrollToken(tokenHash, clientName string, expiresAt time.Time) error
	ConsumeEnrollToken(tokenHash, usedClient string, now time.Time) (string, error)
	CreateEdgeClient(c *store.EdgeClient) error
	GetEdgeClientBySecret(secretHash string) (*store.EdgeClient, error)
	ListEdgeClients() ([]store.EdgeClient, error)
	RevokeEdgeClient(clientID string) error
	TouchEdgeClient(clientID string, at time.Time) error
}

// SetEnrollStore wires the persistent client registry. When nil, only
// the static config.yml edge_clients are honoured (no enrollment).
func (ing *Ingestor) SetEnrollStore(st EnrollStore) { ing.enrollStore = st }

// IssueEnrollToken creates a single-use code for clientName and returns
// the PLAINTEXT once. It is never recoverable afterwards.
func (ing *Ingestor) IssueEnrollToken(clientName string) (token string, expiresAt time.Time, err error) {
	if ing.enrollStore == nil {
		return "", time.Time{}, errors.New("enrollment disabled: no client registry configured")
	}
	clientName = strings.TrimSpace(clientName)
	if clientName == "" {
		return "", time.Time{}, errors.New("client name is required")
	}
	raw, err := randomHex(16) // 32 hex chars = 128 bits
	if err != nil {
		return "", time.Time{}, err
	}
	token = enrollTokenPrefix + raw
	expiresAt = time.Now().UTC().Add(EnrollTokenTTL)
	if err := ing.enrollStore.CreateEnrollToken(hashToken(token), clientName, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// enrollRequest is what the client posts to redeem a code.
type enrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	ClientName  string `json:"client_name"`
}

// enrollResponse carries the credentials. The secret appears exactly
// once, here.
type enrollResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Name         string `json:"name"`
}

// handleEnroll redeems a one-time code for a client credential.
//
// Unauthenticated by design: the enroll token IS the credential. Every
// failure mode (unknown, used, expired) returns the same 401 so a
// prober learns nothing about which codes exist.
func (ing *Ingestor) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "enroll requires POST")
		return
	}
	if ing.enrollStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			`{"error":{"code":"enroll_disabled","message":"enrollment is not enabled on this server"}}`)
		return
	}

	var req enrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.EnrollToken = strings.TrimSpace(req.EnrollToken)
	if req.EnrollToken == "" {
		writeJSONError(w, http.StatusBadRequest, "enroll_token is required")
		return
	}

	// Generate the credentials before consuming the code, so a failure
	// to generate randomness does not burn the operator's code.
	clientID := clientIDPrefix + mustRandomHex(8)    // ec_ + 16 hex
	secret := clientSecretPfx + mustRandomHex(24)    // ecs_ + 48 hex
	secretHash := hashToken(secret)

	// Claim the code. The client_id is written as used_client so the
	// audit trail shows which credential consumed it.
	name, err := ing.enrollStore.ConsumeEnrollToken(hashToken(req.EnrollToken), clientID, time.Now().UTC())
	if err != nil {
		// Same response for unknown/used/expired.
		logging.Warn("edge enroll rejected",
			logging.F("reason", "invalid_or_expired_code"),
			logging.F("remote", r.RemoteAddr))
		writeJSONError(w, http.StatusUnauthorized,
			`{"error":{"code":"invalid_enroll_token","message":"enroll token is invalid, expired or already used"}}`)
		return
	}

	// A caller-supplied name is advisory only: the code was issued for a
	// specific name, and that name is what gets registered. Otherwise
	// anyone holding a code could claim another client's identity.
	_ = req.ClientName

	if err := ing.enrollStore.CreateEdgeClient(&store.EdgeClient{
		ClientID:   clientID,
		Name:       name,
		SecretHash: secretHash,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "register client: "+err.Error())
		return
	}

	// Deliberately NOT cached in ing.clients: the database is the source
	// of truth, and caching here would let a revoked secret keep
	// working until restart.

	logging.Info("edge client enrolled",
		logging.F("client_id", clientID), logging.F("name", name))

	writeJSON(w, http.StatusCreated, enrollResponse{
		ClientID:     clientID,
		ClientSecret: secret,
		Name:         name,
	})
}

// ---------- credential verification ----------

// verifyClient resolves a bearer secret to a client name. It checks the
// static config list first (bootstrap path) and then the database.
//
// The database lookup runs on every call rather than caching results:
// revocation must take effect immediately, and a 30s heartbeat plus a
// per-request usage upload are far too infrequent for the query to
// matter.
func (ing *Ingestor) verifyClient(secret string) (name string, ok bool) {
	if secret == "" {
		return "", false
	}
	// Static config entries (config.yml edge_clients).
	ing.mu.Lock()
	for _, c := range ing.clients {
		if c.Token != "" && equalFoldConstantTime(secret, c.Token) {
			ing.mu.Unlock()
			return c.Name, true
		}
	}
	ing.mu.Unlock()

	if ing.enrollStore == nil {
		return "", false
	}
	// Enrolled clients: hash and look up. Revoked rows return not-found.
	c, err := ing.enrollStore.GetEdgeClientBySecret(hashToken(secret))
	if err != nil || c == nil {
		return "", false
	}
	return c.Name, true
}

// clientBySecret returns the full enrolled record (id + name) when the
// secret belongs to a database client. Used for last_seen tracking.
func (ing *Ingestor) clientBySecret(secret string) (*store.EdgeClient, bool) {
	if ing.enrollStore == nil {
		return nil, false
	}
	c, err := ing.enrollStore.GetEdgeClientBySecret(hashToken(secret))
	if err != nil || c == nil {
		return nil, false
	}
	return c, true
}

// IssueEnrollTokenJSON is the admin-facing wrapper: it returns a
// JSON-marshalable map so internal/admin need not know this type.
func (ing *Ingestor) IssueEnrollTokenJSON(clientName string) (any, error) {
	token, expiresAt, err := ing.IssueEnrollToken(clientName)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"token":      token,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
		"name":       strings.TrimSpace(clientName),
		// The code appears exactly once; make that explicit to whoever
		// calls the API.
		"notice": "this token cannot be retrieved again",
	}, nil
}

// RevokeEdgeClientJSON disables a client.
func (ing *Ingestor) RevokeEdgeClientJSON(clientID string) error {
	if ing.enrollStore == nil {
		return errors.New("enrollment disabled: no client registry configured")
	}
	return ing.enrollStore.RevokeEdgeClient(clientID)
}

// ---------- helpers ----------

func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// mustRandomHex panics on entropy failure. crypto/rand failing means
// the process cannot produce safe credentials at all, so continuing is
// worse than crashing.
func mustRandomHex(n int) string {
	s, err := randomHex(n)
	if err != nil {
		panic(err)
	}
	return s
}
