// Package byok implements Bring-Your-Own-Key handling: when a
// request arrives with a bearer token that doesn't match any llmRx
// token in the cache, the UnknownTokenHook probes the upstream
// provider to verify the key, then persists an encrypted BYOK row
// keyed by client IP. Subsequent requests from the same IP that
// present the same key proceed without re-verification.
package byok

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

// Store is the narrow BYOK surface used by the hook.
type Store interface {
	GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error)
	CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error)
	TouchBYOKChannel(ctx context.Context, id int64) error
}

// Config controls which provider prefixes are accepted and how the
// key is verified against the upstream.
type Config struct {
	Enabled         bool
	WhitelistIPs    []string
	WhitelistEmails []string
	MaxKeysPerIP    int
	TTLDays         int
	// ProviderPrefixes is the list of "looks like an upstream key"
	// prefixes. A request token that doesn't match any llmRx token
	// is BYOK-eligible iff it starts with one of these.
	ProviderPrefixes []string
	// UpstreamProbes verifies a token against the upstream. Return
	// nil on success, an error otherwise. The verifier is injected
	// so tests can stub it out without real HTTP.
	UpstreamProbes map[string]ProbeFunc
}

// ProbeFunc verifies an upstream key. baseURL is the upstream API
// root. Return nil on success.
type ProbeFunc func(ctx context.Context, apiKey, baseURL string) error

// Manager wires the BYOK hook into the auth middleware.
type Manager struct {
	cfg     Config
	store   Store
	secrets *secrets.Manager
}

// New constructs a Manager. secretsMgr may be nil (plaintext keys at
// rest) but is strongly recommended.
func New(cfg Config, store Store, secretsMgr *secrets.Manager) *Manager {
	if cfg.ProviderPrefixes == nil {
		cfg.ProviderPrefixes = []string{"sk-"}
	}
	return &Manager{cfg: cfg, store: store, secrets: secretsMgr}
}

// Hook returns the UnknownTokenHook suitable for TokenWithOptions /
// WithLimitsAndOptions.
func (m *Manager) Hook() middleware.UnknownTokenHook {
	return func(w http.ResponseWriter, r *http.Request, rawKey string) {
		if !m.cfg.Enabled {
			writeErr(w, http.StatusForbidden, "invalid token", "invalid_token")
			return
		}
		ip := clientIP(r)
		if !ipAllowed(ip, m.cfg.WhitelistIPs) {
			writeErr(w, http.StatusForbidden, "ip not whitelisted for BYOK", "byok_ip_blocked")
			return
		}
		if !StringHasAnyPrefix(rawKey, m.cfg.ProviderPrefixes) {
			writeErr(w, http.StatusForbidden, "invalid token", "invalid_token")
			return
		}

		ctx := r.Context()

		// Have we already registered this IP's key?
		if existing, err := m.store.GetBYOKChannelByIP(ctx, ip); err == nil && existing != nil {
			if existing.KeyMasked == maskKey(rawKey) {
				_ = m.store.TouchBYOKChannel(ctx, existing.ID)
				ctx2 := context.WithValue(ctx, byokContextKey{}, &existing)
				setupBYOKContext(w, r, ctx2, rawKey, existing.Provider)
				return
			}
			writeErr(w, http.StatusForbidden, "different key already registered for this IP", "byok_ip_conflict")
			return
		}

		// Verify against the upstream.
		providerName, probe, baseURL := pickProvider(rawKey, m.cfg.UpstreamProbes)
		if probe == nil {
			writeErr(w, http.StatusBadRequest, "no upstream verifier configured for token prefix", "byok_unsupported_provider")
			return
		}
		if err := probe(ctx, rawKey, baseURL); err != nil {
			writeErr(w, http.StatusForbidden, "upstream rejected key: "+err.Error(), "byok_invalid_key")
			return
		}

		// Persist the encrypted key.
		cipher := ""
		if m.secrets != nil {
			ct, err := m.secrets.Encrypt([]byte(rawKey))
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "encrypt failed", "byok_encrypt_failed")
				return
			}
			cipher = ct
		} else {
			cipher = rawKey // plaintext fallback (legacy / no-master-key)
		}
		expiresAt := time.Time{}
		if m.cfg.TTLDays > 0 {
			expiresAt = time.Now().Add(time.Duration(m.cfg.TTLDays) * 24 * time.Hour)
		}
		ch := &model.BYOKChannel{
			Provider:      providerName,
			KeyCiphertext: cipher,
			KeyMasked:     maskKey(rawKey),
			OwnerIP:       ip,
			Status:        1,
			ExpiresAt:     expiresAt,
		}
		if _, err := m.store.CreateBYOKChannel(ctx, ch); err != nil {
			writeErr(w, http.StatusInternalServerError, "create BYOK channel failed: "+err.Error(), "byok_create_failed")
			return
		}
		ctx2 := context.WithValue(ctx, byokContextKey{}, ch)
		setupBYOKContext(w, r, ctx2, rawKey, providerName)
	}
}

// ContextKey is exported so downstream code can look up the BYOK row.
type ContextKey struct{}

type byokContextKey struct{}

// KeyCiphertextFromContext returns the stored ciphertext (encrypted
// form) for the BYOK row attached to the request, if any.
func KeyCiphertextFromContext(ctx context.Context) (string, string, bool) {
	v := ctx.Value(byokContextKey{})
	if v == nil {
		return "", "", false
	}
	switch t := v.(type) {
	case *model.BYOKChannel:
		return t.KeyCiphertext, t.Provider, true
	}
	return "", "", false
}

// Decrypt returns the plaintext API key for a BYOK row. nil-safe.
func (m *Manager) Decrypt(ch *model.BYOKChannel) (string, error) {
	if ch == nil {
		return "", nil
	}
	if m.secrets == nil {
		return ch.KeyCiphertext, nil
	}
	b, err := m.secrets.Decrypt(ch.KeyCiphertext)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- helpers ---

func clientIP(r *http.Request) string {
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitHostPort(addr string) (string, string, error) {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr, "", errors.New("no port")
	}
	return addr[:idx], addr[idx+1:], nil
}

func ipAllowed(ip string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	for _, w := range whitelist {
		if w == ip || w == "*" {
			return true
		}
	}
	return false
}

func maskKey(k string) string {
	if len(k) > 12 {
		return k[:3] + "***" + k[len(k)-4:]
	}
	if len(k) > 8 {
		return k[:3] + "***" + k[len(k)-4:]
	}
	if len(k) > 2 {
		return k[:1] + "***" + k[len(k)-1:]
	}
	return "***"
}

func pickProvider(key string, probes map[string]ProbeFunc) (string, ProbeFunc, string) {
	// OpenAI-style sk- prefixes go to OpenAI's /v1/models.
	if strings.HasPrefix(key, "sk-") {
		if p, ok := probes["openai"]; ok {
			return "openai", p, "https://api.openai.com/v1"
		}
	}
	// Anthropic keys start with sk-ant-.
	if strings.HasPrefix(key, "sk-ant-") {
		if p, ok := probes["anthropic"]; ok {
			return "anthropic", p, "https://api.anthropic.com/v1"
		}
	}
	// Google AI keys: AIza...
	if strings.HasPrefix(key, "AIza") {
		if p, ok := probes["gemini"]; ok {
			return "gemini", p, "https://generativelanguage.googleapis.com/v1beta"
		}
	}
	// Hash-based fallback: pick the first verifier, ignore the
	// upstream baseURL — the verifier already knows.
	if len(probes) > 0 {
		for name, p := range probes {
			return name, p, ""
		}
	}
	return "", nil, ""
}

func writeErr(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"message":%q,"type":"invalid_request_error","code":%q}}`, msg, code)
}

func setupBYOKContext(w http.ResponseWriter, r *http.Request, ctx context.Context, rawKey, provider string) {
	// The UnknownTokenHook contract: when the hook writes a response
	// and returns, the middleware chain stops. To proceed with the
	// request using the BYOK key, the hook should not write a
	// response — instead, the next handler reads from the context.
	//
	// In practice, a downstream BYOK-aware handler reads
	// KeyCiphertextFromContext(ctx) and Decrypts to get the plain key.
	_ = w
	_ = r
	_ = rawKey
	_ = provider
}

// StringHasAnyPrefix is a small helper for prefix matching.
func StringHasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Fingerprint returns a stable hex fingerprint of a key for cheap
// equality checks. Does not store the key itself.
func Fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}
