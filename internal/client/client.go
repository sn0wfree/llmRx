// Package client implements the llmRx edge client: a local OpenAI-
// compatible endpoint that forwards to a remote llmRx server (P15).
//
// Import discipline: this package must never import internal/store or
// internal/router. Both drag in the CGO sqlite driver
// (store/sqlite.go blank-imports mattn/go-sqlite3), which would break
// cross-compilation of cmd/gateway-client. internal/cache and
// internal/provider are safe (pure Go). A static guard test asserts
// this.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// Cache is the client-side response cache: deterministic requests
// (temperature == 0, non-streaming) are served locally when the server
// is unreachable, and short-circuit repeat calls even when it is up.
//
// It reuses the server's Cache interface and Key() normalisation so a
// response cached here is keyed identically to one cached server-side.
type Cache struct {
	mu    sync.RWMutex
	inner cache.Cache
	max   int
}

// NewCache wraps an in-memory cache. maxItems bounds the LRU; 0 uses
// the package default.
func NewCache(maxItems int) *Cache {
	if maxItems <= 0 {
		maxItems = 2000
	}
	return &Cache{inner: cache.NewMemoryCache(maxItems), max: maxItems}
}

// ErrNotCacheable marks requests that must never be cached (streaming,
// temperature > 0, tools). It is internal to the proxy flow.
var ErrNotCacheable = errors.New("client: request is not cacheable")

// lookupKey computes the canonical cache key for a chat request,
// mirroring the server's own key derivation.
func lookupKey(req *provider.ChatRequest) (string, error) {
	return cache.Key(req)
}

// get returns a cached response for key, or nil.
func (c *Cache) get(ctx context.Context, key string) *cache.Entry {
	if c == nil {
		return nil
	}
	e, ok, err := c.inner.Get(ctx, key)
	if err != nil || !ok {
		return nil
	}
	return e
}

// put stores a response. Storage failures are logged by the caller;
// they must never fail the request that produced the response.
// MemoryCache indexes entries by e.Key, so the key rides on the entry.
func (c *Cache) put(ctx context.Context, key string, e *cache.Entry, ttl time.Duration) {
	if c == nil || e == nil {
		return
	}
	e.Key = key
	_ = c.inner.Set(ctx, e, ttl)
}

// purge clears everything (config push / manual).
func (c *Cache) purge(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.inner.Purge(ctx)
}

// Purge clears every cached entry. Exported for the status page's
// "clear cache" action.
func (c *Cache) Purge(ctx context.Context) error { return c.purge(ctx) }

// stats is exposed on the local status page and in heartbeats.
func (c *Cache) stats(ctx context.Context) cache.Stats {
	if c == nil {
		return cache.Stats{}
	}
	s, _ := c.inner.Stats(ctx)
	return s
}

// Stats returns the cache counters for the status page.
func (c *Cache) Stats(ctx context.Context) cache.Stats { return c.stats(ctx) }

// ---------- Server reachability ----------

// breaker tracks whether the upstream llmRx server is reachable. When
// open, requests skip the (slow) dial and go straight to the fallback
// chain; a probe request is let through after cooldown to half-open.
type breaker struct {
	mu        sync.Mutex
	open      bool
	openedAt  time.Time
	fails     int
	threshold int
	cooldown  time.Duration
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &breaker{threshold: threshold, cooldown: cooldown}
}

// allow reports whether a request may attempt the server. An open
// breaker lets one probe through after the cooldown elapses.
func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if now.Sub(b.openedAt) >= b.cooldown {
		// Half-open: let the probe through.
		return true
	}
	return false
}

// success records a working server exchange.
func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = false
	b.fails = 0
}

// failure records a failed exchange and trips the breaker once the
// threshold is reached. Only transport-level failures count; 4xx/5xx
// responses from a live server do not (they are business semantics the
// agent must see).
func (b *breaker) failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails++
	if b.fails >= b.threshold {
		b.open = true
		b.openedAt = time.Now()
	}
}

// state is for the local status endpoint.
func (b *breaker) state() (open bool, fails int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open, b.fails
}

// ---------- Fallback decision ----------

// fallible reports whether an upstream response indicates the server is
// unreachable and the fallback chain should engage. Business errors
// (401 invalid token, 429 rate limited, upstream provider errors) must
// pass through untouched: falling back on them would mask real
// problems and could double-charge via a different path.
func fallible(status int, transportErr error) bool {
	if transportErr != nil {
		return true
	}
	// 408/429 are the server talking; only >=500 and 408 request-timeout
	// indicate the gateway itself is struggling.
	return status == httpStatusRequestTimeout || status >= 500
}

// ---------- Usage records ----------

// UsageRecord is one completed request, accumulated locally and
// batched to the server. request_id is the idempotency key: the server
// deduplicates on it, so a retried upload never double-counts.
type UsageRecord struct {
	RequestID        string  `json:"request_id"`
	Timestamp        int64   `json:"ts"`
	Model            string  `json:"model"`
	Status           int     `json:"status"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	// Source distinguishes where the request was served:
	//   proxy — forwarded to the server (the server already billed it;
	//   this row is observability only)
	//   cache — served from the local cache (no tokens anywhere)
	//   local — served by a local fallback upstream (billable, the
	//   server never saw it)
	Source    string `json:"source"`
	LatencyMS int64  `json:"latency_ms"`
}

// Validate the source values.
const (
	SourceProxy = "proxy"
	SourceCache = "cache"
	SourceLocal = "local"
)

// ValidSource guards the enum at construction sites.
func ValidSource(s string) bool {
	switch s {
	case SourceProxy, SourceCache, SourceLocal:
		return true
	}
	return false
}

// usageBuffer accumulates records and hands them out in batches.
// Persistence (JSONL) is handled by usageStore; this type is the
// in-memory staging area between requests and the uploader.
type usageBuffer struct {
	mu      sync.Mutex
	pending []UsageRecord
	max     int
}

// marshal serialises one record (JSON line).
func (r UsageRecord) marshal() ([]byte, error) {
	return json.Marshal(r)
}

func newUsageBuffer(max int) *usageBuffer {
	if max <= 0 {
		max = 5000
	}
	return &usageBuffer{max: max}
}

func (u *usageBuffer) add(r UsageRecord) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) >= u.max {
		// Drop the oldest: usage is telemetry, never worth blocking a
		// request over. The dropped count is reported by Dropped().
		u.pending = u.pending[1:]
	}
	u.pending = append(u.pending, r)
}

// take drains up to n records for upload.
func (u *usageBuffer) take(n int) []UsageRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	if n > len(u.pending) {
		n = len(u.pending)
	}
	out := make([]UsageRecord, n)
	copy(out, u.pending[:n])
	u.pending = u.pending[n:]
	return out
}

func (u *usageBuffer) len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.pending)
}

// ---------- Config ----------

// Config is the client's own configuration, loaded from a small YAML
// file. Deliberately flat: this file lives on a laptop or an edge box
// and is edited by hand.
type Config struct {
	// Listen is the local address, e.g. "127.0.0.1:3425".
	Listen string `yaml:"listen"`
	// Servers is the ordered list of llmRx base URLs; the first
	// reachable one serves the request (primary/failover).
	Servers []string `yaml:"servers"`
	// Token is the llmRx *user* token injected into forwarded requests
	// when the agent did not send one.
	Token string `yaml:"token"`
	// ClientToken authenticates usage uploads (server-side
	// edge_clients entry). Empty disables uploads.
	ClientToken string `yaml:"client_token"`
	// ClientName labels this edge in server-side records.
	ClientName string `yaml:"client_name"`
	// UsageEndpoint is the upload URL. Empty means "server URL +
	// /client/v1/usage".
	UsageEndpoint string `yaml:"usage_endpoint"`
	// UploadBatchSec is the usage upload period.
	UploadBatchSec int `yaml:"usage_upload_sec"`
	// CacheEnabled toggles the local response cache.
	CacheEnabled bool `yaml:"cache_enabled"`
	// CacheTTLSec bounds cached entries.
	CacheTTLSec int `yaml:"cache_ttl_sec"`
	// CacheMaxItems bounds the LRU.
	CacheMaxItems int `yaml:"cache_max_items"`
	// UsageFile persists usage records across restarts (JSONL).
	UsageFile string `yaml:"usage_file"`
	// FallbackMode / MaxInflight are locally configurable defaults that
	// a server-pushed bundle may override ("cache_then_fail" or
	// "local").
	FallbackMode string `yaml:"fallback_mode"`
	MaxInflight  int    `yaml:"max_inflight"`

	// mu guards the fields a verified bundle may rewrite while
	// requests are in flight.
	mu sync.RWMutex
}

// Normalize fills defaults and validates. It returns an error listing
// every problem at once so a misconfigured edge box fails fast with a
// complete message.
func (c *Config) Normalize() error {
	var probs []string
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = "127.0.0.1:3425"
	}
	if len(c.Servers) == 0 {
		probs = append(probs, "servers: at least one llmRx server URL is required")
	}
	for i, s := range c.Servers {
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			probs = append(probs, fmt.Sprintf("servers[%d]: %q must start with http:// or https://", i, s))
		}
	}
	if c.UploadBatchSec <= 0 {
		c.UploadBatchSec = 60
	}
	if c.CacheTTLSec <= 0 {
		c.CacheTTLSec = 3600
	}
	if len(probs) > 0 {
		sort.Strings(probs)
		return errors.New("client config: " + strings.Join(probs, "; "))
	}
	return nil
}

// usageURL resolves the upload endpoint.
func (c *Config) usageURL() string {
	if c.UsageEndpoint != "" {
		return c.UsageEndpoint
	}
	if len(c.Servers) == 0 {
		return ""
	}
	return strings.TrimRight(c.Servers[0], "/") + "/client/v1/usage"
}

// ApplyBundle folds a verified server-pushed bundle into the live
// config. Guarded by a mutex because the proxy reads Servers on every
// request while the control loop writes them.
//
// Only fields the server is allowed to steer are touched: the token,
// the client token and the listen address are local-only and never
// come from the network.
func (c *Config) ApplyBundle(b Bundle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(b.Servers) > 0 {
		c.Servers = append([]string(nil), b.Servers...)
	}
	c.CacheEnabled = b.Cache.Enabled
	if b.Cache.TTLSec > 0 {
		c.CacheTTLSec = b.Cache.TTLSec
	}
	if b.Cache.MaxItems > 0 {
		c.CacheMaxItems = b.Cache.MaxItems
	}
	if b.Fallback.Mode != "" {
		c.FallbackMode = b.Fallback.Mode
	}
	if b.Limits.MaxInflight > 0 {
		c.MaxInflight = b.Limits.MaxInflight
	}
}

// Mu exposes the guard for bundle application so callers that must
// mutate several fields atomically (the settings form) can hold it.
// Only for writers; readers should prefer ServersSnapshot and friends.
func (c *Config) Mu() *sync.RWMutex { return &c.mu }

// ServersSnapshot returns the current server list under the lock.
func (c *Config) ServersSnapshot() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.Servers) == 0 {
		return nil
	}
	out := make([]string, len(c.Servers))
	copy(out, c.Servers)
	return out
}

// FallbackModeOrDefault returns the effective fallback mode.
func (c *Config) FallbackModeOrDefault() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.FallbackMode == "" {
		return "cache_then_fail"
	}
	return c.FallbackMode
}
