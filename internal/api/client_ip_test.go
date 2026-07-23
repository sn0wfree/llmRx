package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/config"
)

// newTestHandlerWithProxyConfig constructs a minimal handler
// suitable for clientIP tests — no store, no pool, no router.
// Only h.cfg is consulted by clientIP so the other fields stay
// zero values.
func newTestHandlerWithProxyConfig(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	return &Handler{cfg: cfg}
}

func TestClientIP_DefaultIgnoresProxyHeaders(t *testing.T) {
	h := newTestHandlerWithProxyConfig(t, &config.Config{})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.10:54321"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("X-Real-IP", "10.0.0.2")

	if got := h.clientIP(r); got != "203.0.113.10:54321" {
		t.Fatalf("default clientIP = %q, want %q (no proxy trust)", got, r.RemoteAddr)
	}
}

func TestClientIP_TrustsProxyHeadersFromUntrustedPeer(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{TrustProxyHeaders: true, TrustedProxyCIDRs: []string{"10.0.0.0/8"}}}
	h := newTestHandlerWithProxyConfig(t, cfg)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.10:54321" // outside trusted CIDR
	r.Header.Set("X-Forwarded-For", "10.0.0.1")

	if got := h.clientIP(r); got != "203.0.113.10:54321" {
		t.Fatalf("clientIP from untrusted peer = %q, want %q (header must be ignored)", got, r.RemoteAddr)
	}
}

func TestClientIP_TrustsProxyHeadersFromTrustedPeer(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{TrustProxyHeaders: true, TrustedProxyCIDRs: []string{"10.0.0.0/8"}}}
	h := newTestHandlerWithProxyConfig(t, cfg)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:54321" // inside trusted CIDR
	r.Header.Set("X-Forwarded-For", "203.0.113.42")

	if got := h.clientIP(r); got != "203.0.113.42" {
		t.Fatalf("clientIP from trusted peer = %q, want %q (XFF must be honoured)", got, "203.0.113.42")
	}
}

func TestClientIP_XForwardedForChain(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{TrustProxyHeaders: true}}
	h := newTestHandlerWithProxyConfig(t, cfg)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	// XFF chain: client, proxy1, proxy2. Leftmost is the original client.
	r.Header.Set("X-Forwarded-For", "203.0.113.42, 198.51.100.7, 10.0.0.99")

	if got := h.clientIP(r); got != "203.0.113.42" {
		t.Fatalf("XFF chain leftmost = %q, want %q", got, "203.0.113.42")
	}
}

func TestClientIP_XRealIPFallback(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{TrustProxyHeaders: true}}
	h := newTestHandlerWithProxyConfig(t, cfg)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Real-IP", "203.0.113.99")

	if got := h.clientIP(r); got != "203.0.113.99" {
		t.Fatalf("X-Real-IP = %q, want %q", got, "203.0.113.99")
	}
}

func TestClientIP_EmptyTrustedCIDRsMeansTrustEveryone(t *testing.T) {
	// TrustProxyHeaders=true with no CIDR list is "trust every
	// peer" — useful for tightly-controlled deployments where
	// every connection is fronted by a known proxy.
	cfg := &config.Config{Server: config.ServerConfig{TrustProxyHeaders: true}}
	h := newTestHandlerWithProxyConfig(t, cfg)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "8.8.8.8:54321"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")

	if got := h.clientIP(r); got != "1.1.1.1" {
		t.Fatalf("empty CIDR list should still trust = %q, want %q", got, "1.1.1.1")
	}
}