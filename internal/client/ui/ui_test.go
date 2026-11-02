package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/client"
	"github.com/sn0wfree/llmRx/internal/provider"
)

func newUI(t *testing.T) (*Server, *client.Config, *client.Proxy, string) {
	t.Helper()
	cfg := &client.Config{
		Listen:         "127.0.0.1:3425",
		Servers:        []string{"https://a.example", "https://b.example"},
		Token:          "sk-user-secret-token-abcdefgh",
		ClientToken:    "ecs_clientsidecret0123456789abcdef",
		ClientName:     "edge-test",
		CacheEnabled:   true,
		CacheTTLSec:    600,
		CacheMaxItems:  100,
		FallbackMode:   "local",
		UsageFile:      "/tmp/usage.jsonl",
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	p := client.NewProxy(cfg)
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yml")
	s, err := New(p, cfg, path, "v0.1.0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, cfg, p, path
}

func get(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func postForm(t *testing.T, s *Server, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// ---------- rendering ----------

func TestIndex_RendersCorePanels(t *testing.T) {
	s, _, _, _ := newUI(t)
	rec := get(t, s, "/ui")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"llmRx Client", "状态", "缓存", "用量", "设置",
		"127.0.0.1:3425", "https://a.example", "v0.1.0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	// Local state must not be cached.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

// The page must be self-contained: rendering it can never require a
// network fetch, because it is opened precisely when the network is
// down.
func TestIndex_HasNoExternalResources(t *testing.T) {
	s, _, _, _ := newUI(t)
	body := get(t, s, "/ui").Body.String()

	for _, bad := range []string{"cdn.", "unpkg", "jsdelivr", "googleapis", "htmx", "<script src="} {
		if strings.Contains(body, bad) {
			t.Fatalf("page references %q — it must render offline", bad)
		}
	}
	// And styles are present inline.
	if !strings.Contains(body, "<style>") {
		t.Fatal("no inline <style> block")
	}
}

// Credentials are masked and the plaintext never reaches the HTML.
func TestIndex_MasksCredentials(t *testing.T) {
	s, cfg, _, _ := newUI(t)
	body := get(t, s, "/ui").Body.String()

	if strings.Contains(body, cfg.Token) {
		t.Fatal("user token leaked into the page")
	}
	if strings.Contains(body, cfg.ClientToken) {
		t.Fatal("client token leaked into the page")
	}
	if !strings.Contains(body, "***") {
		t.Fatal("expected masked values")
	}
}

func TestIndex_RejectsPost(t *testing.T) {
	s, _, _, _ := newUI(t)
	rec := postForm(t, s, "/ui", url.Values{})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// Connectivity is derived from the breaker: open → offline, failures
// but not open → degraded.
func TestIndex_ConnectivityStates(t *testing.T) {
	s, _, p, _ := newUI(t)

	if !strings.Contains(get(t, s, "/ui").Body.String(), "服务端可达") {
		t.Fatal("a fresh client should report reachable")
	}

	// Trip the breaker through its public behaviour: three failures.
	// The breaker is internal, so drive it the way the proxy does.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"m","temperature":0,"messages":[]}`))
		// Point at an unreachable server so the transport fails.
		p.Handler().ServeHTTP(rec, req)
	}
	if open, fails := p.ServerBreaker(); !open && fails == 0 {
		t.Skip("breaker did not engage; environment dependent")
	}

	body := get(t, s, "/ui").Body.String()
	if !strings.Contains(body, "熔断") && !strings.Contains(body, "不可达") && !strings.Contains(body, "不稳定") {
		t.Fatalf("expected a non-ok connectivity state in body")
	}
}

// ---------- cache ----------

// Purging is the destructive action; assert it redirects with a flash
// and leaves the cache empty. Seeding goes through the real proxy path
// (a mock upstream + a deterministic request) rather than reaching into
// internals.
func TestPurge_ClearsCache(t *testing.T) {
	s, cfg, p, _ := newUI(t)
	ctx := context.Background()

	// Serve one deterministic request so the cache has an entry.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer up.Close()
	cfg.Servers = []string{up.URL}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","temperature":0,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	p.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if got := p.Cache().Stats(ctx); got.Size == 0 {
		t.Fatalf("cache seed failed, size = %d", got.Size)
	}

	rec := postForm(t, s, "/ui/cache/purge", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "flash=") {
		t.Fatalf("Location = %q, want a flash redirect", loc)
	}
	if got := p.Cache().Stats(ctx); got.Size != 0 {
		t.Fatalf("cache size after purge = %d", got.Size)
	}
}

func TestPurge_RequiresPost(t *testing.T) {
	s, _, _, _ := newUI(t)
	if rec := get(t, s, "/ui/cache/purge"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------- settings ----------

func TestSettings_PersistsAndRedirects(t *testing.T) {
	s, cfg, _, path := newUI(t)

	form := url.Values{
		"servers":         {"https://new1.example\nhttps://new2.example"},
		"cache_enabled":   {"on"},
		"cache_ttl_sec":   {"120"},
		"cache_max_items": {"50"},
		"fallback_mode":   {"cache_then_fail"},
	}
	rec := postForm(t, s, "/ui/settings", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "flash=") {
		t.Fatalf("Location = %q", loc)
	}

	if got := cfg.ServersSnapshot(); len(got) != 2 || got[0] != "https://new1.example" {
		t.Fatalf("servers = %v", got)
	}
	if cfg.CacheTTLSec != 120 || cfg.CacheMaxItems != 50 {
		t.Fatalf("cache settings not applied: %+v", cfg)
	}
	if cfg.FallbackModeOrDefault() != "cache_then_fail" {
		t.Fatalf("fallback = %q", cfg.FallbackModeOrDefault())
	}

	// And it really hit disk, with 0600.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 0600", perm)
	}
}

// Tokens must survive a settings round-trip untouched: the form shows
// masks, so writing them back would destroy the real credentials.
func TestSettings_DoesNotClobberCredentials(t *testing.T) {
	s, cfg, _, path := newUI(t)
	beforeToken := cfg.Token
	beforeClient := cfg.ClientToken

	postForm(t, s, "/ui/settings", url.Values{
		"servers":       {"https://x.example"},
		"cache_ttl_sec": {"900"},
	})

	if cfg.Token != beforeToken {
		t.Fatalf("user token changed to %q", cfg.Token)
	}
	if cfg.ClientToken != beforeClient {
		t.Fatalf("client token changed to %q", cfg.ClientToken)
	}
	// The file must contain the real values, not the masks.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), beforeToken) {
		t.Fatal("written config lost the user token")
	}
	if strings.Contains(string(raw), "***") {
		t.Fatal("written config contains a mask string")
	}
}

func TestSettings_RejectsUnknownFallbackMode(t *testing.T) {
	s, cfg, _, _ := newUI(t)
	before := cfg.FallbackModeOrDefault()

	postForm(t, s, "/ui/settings", url.Values{
		"servers":       {"https://x.example"},
		"fallback_mode": {"teleport"},
	})
	if got := cfg.FallbackModeOrDefault(); got != before {
		t.Fatalf("fallback mode changed to %q by an invalid value", got)
	}
}

func TestSettings_RequiresPost(t *testing.T) {
	s, _, _, _ := newUI(t)
	if rec := get(t, s, "/ui/settings"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// A save failure must surface as an error flash, not a 500 or a silent
// success.
func TestSettings_SaveFailureReported(t *testing.T) {
	s, _, _, _ := newUI(t)
	s.saveConfig = func(*client.Config) error {
		return os.ErrPermission
	}
	rec := postForm(t, s, "/ui/settings", url.Values{"servers": {"https://x.example"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("Location = %q, want an error redirect", loc)
	}
}

// ---------- helpers ----------

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":                      "(未设置)",
		"short":                 "***",
		"sk-1234567890abcdef":   "sk-12***cdef",
	}
	for in, want := range cases {
		if got := maskSecret(in); got != want {
			t.Fatalf("maskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatRate(t *testing.T) {
	if got := formatRate(cache.Stats{}); got != "—" {
		t.Fatalf("empty stats = %q", got)
	}
	if got := formatRate(cache.Stats{Hits: 9, Misses: 1}); got != "90.0%" {
		t.Fatalf("9/10 = %q, want 90.0%%", got)
	}
	if got := formatRate(cache.Stats{Hits: 1, Misses: 2}); got != "33.3%" {
		t.Fatalf("1/3 = %q, want 33.3%%", got)
	}
}

func TestParseInt(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"0":      0,
		"120":    120,
		"12a":    12, // stops at the first non-digit
		"-5":     0,
		"999999999999": 1000000000, // clamped
	}
	for in, want := range cases {
		if got := parseInt(in); got != want {
			t.Fatalf("parseInt(%q) = %d, want %d", in, got, want)
		}
	}
}

var _ = provider.ChatRequest{}
