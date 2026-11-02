package client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Control-plane client (P15 phase 2): polls the server for liveness and
// a signed config bundle. The client only ever dials out; the server
// stays passive.
//
// Signature verification is mandatory: a bundle that fails HMAC
// validation is discarded and the previous config stays in force. That
// is what makes a pull-based config channel safe over the public
// internet.

// Bundle mirrors the server's ConfigBundle. Kept as a separate type so
// the client does not import internal/clientapi (which pulls in the log
// store and thus CGO).
type Bundle struct {
	Version  int       `json:"version"`
	IssuedAt time.Time `json:"issued_at"`
	Servers  []string  `json:"servers,omitempty"`
	Cache    struct {
		Enabled  bool `json:"enabled"`
		TTLSec   int  `json:"ttl_sec"`
		MaxItems int  `json:"max_items"`
	} `json:"cache"`
	Fallback struct {
		Mode string `json:"mode"`
	} `json:"fallback"`
	Limits struct {
		MaxInflight int `json:"max_inflight"`
	} `json:"limits"`
	MinClientVersion string `json:"min_client_version,omitempty"`
}

type signedBundleWire struct {
	Config    Bundle `json:"config"`
	Signature string `json:"signature"`
	Algorithm string `json:"algorithm"`
}

// verifyBundle checks the HMAC over the canonical JSON of the bundle.
// Must match the server's signBundle exactly.
func verifyBundle(b Bundle, signature, token string) bool {
	payload, err := json.Marshal(b)
	if err != nil {
		return false
	}
	want := hmacHex(payload, token)
	return hmac.Equal([]byte(want), []byte(signature))
}

// hmacHex is the shared HMAC primitive (hex-encoded SHA-256), used by
// verifyBundle and exposed to tests that need to forge a valid
// signature without importing the server package.
func hmacHex(payload []byte, token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// Control polls the server and applies signed config.
type Control struct {
	baseURL string
	name    string
	token   string
	version string
	client  *http.Client

	// applied stores the currently active bundle version (atomic).
	applied atomic.Int64
	// lastErr is the most recent control-plane failure, for the status
	// endpoint.
	mu      sync.Mutex
	lastErr string
	lastOK  time.Time

	// onBundle is invoked after a valid new bundle is accepted.
	onBundle func(Bundle)
}

// NewControl wires the poller. baseURL is the server root (no /v1).
func NewControl(baseURL, name, token, clientVersion string) *Control {
	return &Control{
		baseURL: strings.TrimRight(baseURL, "/"),
		name:    name,
		token:   token,
		version: clientVersion,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// OnBundle registers the callback invoked when a new bundle is applied.
func (c *Control) OnBundle(fn func(Bundle)) { c.onBundle = fn }

// AppliedVersion returns the bundle version currently in force.
func (c *Control) AppliedVersion() int { return int(c.applied.Load()) }

// LastStatus reports the last error and success time for /healthz.
func (c *Control) LastStatus() (lastErr string, lastOK time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr, c.lastOK
}

// Run polls until ctx ends: heartbeat every interval, then fetch the
// config when the server reports a newer version.
func (c *Control) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// One immediate round so a restart does not wait a full interval
	// before it has a config.
	c.round(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.round(ctx)
		}
	}
}

// round performs one heartbeat, and fetches the config if needed.
func (c *Control) round(ctx context.Context) {
	need, serverVersion, err := c.heartbeat(ctx)
	if err != nil {
		c.setErr(err)
		return
	}
	if !need {
		c.setOK()
		return
	}
	if err := c.fetchAndApply(ctx, serverVersion); err != nil {
		c.setErr(err)
		return
	}
	c.setOK()
}

func (c *Control) heartbeat(ctx context.Context) (need bool, serverVersion int, err error) {
	body, _ := json.Marshal(map[string]any{
		"client_version": c.version,
		"config_version": c.AppliedVersion(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/client/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("heartbeat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("heartbeat: status %d", resp.StatusCode)
	}
	var out struct {
		ServerVersion int  `json:"server_version"`
		NeedConfig    bool `json:"need_config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return false, 0, fmt.Errorf("heartbeat decode: %w", err)
	}
	return out.NeedConfig, out.ServerVersion, nil
}

func (c *Control) fetchAndApply(ctx context.Context, serverVersion int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/client/v1/config", nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch config: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var wire signedBundleWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if wire.Algorithm != "hmac-sha256" {
		return fmt.Errorf("config: unsupported signature algorithm %q", wire.Algorithm)
	}
	// A bundle that fails verification is dropped and the previous
	// config stays in force — never apply unverified remote config.
	if !verifyBundle(wire.Config, wire.Signature, c.token) {
		return fmt.Errorf("config: signature verification failed")
	}
	// Refuse a bundle that rolls the version backwards: that is either
	// a replay or an operator mistake, and silently downgrading a
	// client's routing is worse than staying on the known-good config.
	if wire.Config.Version < c.AppliedVersion() {
		return fmt.Errorf("config: refusing downgrade from %d to %d",
			c.AppliedVersion(), wire.Config.Version)
	}
	c.applied.Store(int64(wire.Config.Version))
	if c.onBundle != nil {
		c.onBundle(wire.Config)
	}
	return nil
}

func (c *Control) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Llmrx-Client", c.name)
}

func (c *Control) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastErr = err.Error()
}

func (c *Control) setOK() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastErr = ""
	c.lastOK = time.Now()
}
