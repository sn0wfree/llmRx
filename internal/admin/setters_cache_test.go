package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/subusage"
)

// stubProber satisfies subusage.Prober for setter coverage.
type stubProber struct{}

func (stubProber) QueryChannel(context.Context, *model.Channel) (subusage.Report, error) {
	return subusage.Report{}, nil
}

// stubSessionCounter satisfies SessionCounter.
type stubSessionCounter struct{ n int }

func (s stubSessionCounter) Count() int { return s.n }

// TestHandler_Setters pins the wiring setters: each stores its value
// so later handlers read the injected dependency instead of nil.
func TestHandler_Setters(t *testing.T) {
	h := &Handler{}

	h.SetModelCosts(nil)
	if h.modelCosts != nil {
		t.Fatal("SetModelCosts(nil) must store nil")
	}
	h.SetUsageMonitor(stubProber{})
	if h.usageMon == nil {
		t.Fatal("SetUsageMonitor must store the prober")
	}
	h.SetSessions(stubSessionCounter{n: 7})
	if h.sessions == nil || h.sessions.Count() != 7 {
		t.Fatal("SetSessions must store the counter")
	}
	c := cache.NewMemoryCache(4)
	h.SetCache(c)
	if h.responseCache == nil {
		t.Fatal("SetCache must store the cache")
	}
	h.SetMCPClientManager(nil)
	if h.mcpClientMgr != nil {
		t.Fatal("SetMCPClientManager(nil) must store nil")
	}
	h.SetGuardrailEngine(nil)
	if h.guardrailEngine != nil {
		t.Fatal("SetGuardrailEngine(nil) must store nil")
	}
	fired := false
	h.SetReloadNotifier(func() { fired = true })
	if h.reloadNotifier == nil {
		t.Fatal("SetReloadNotifier must store the func")
	}
	h.fireReload()
	if !fired {
		t.Fatal("fireReload must invoke the notifier")
	}
}

// cacheJSON decodes a CacheStats/CachePurge response body.
func cacheJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

// TestCacheStats_NilCache: without a wired cache the endpoint must
// answer with zeros, not 500 — a partially-wired server stays up.
func TestCacheStats_NilCache(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.CacheStats(rec, httptest.NewRequest(http.MethodGet, "/cache/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	m := cacheJSON(t, rec)
	if m["size"] != float64(0) || m["hit_rate"] != float64(0) {
		t.Fatalf("nil cache must report zeros, got %v", m)
	}
}

// TestCacheStats_WithCache: a wired cache reports its real stats.
func TestCacheStats_WithCache(t *testing.T) {
	h := &Handler{responseCache: cache.NewMemoryCache(8)}
	rec := httptest.NewRecorder()
	h.CacheStats(rec, httptest.NewRequest(http.MethodGet, "/cache/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	m := cacheJSON(t, rec)
	if _, ok := m["hits"]; !ok {
		t.Fatalf("response must include hits: %v", m)
	}
	if _, ok := m["misses"]; !ok {
		t.Fatalf("response must include misses: %v", m)
	}
}

// TestCachePurge_NilCache: purge without a cache is a no-op success.
func TestCachePurge_NilCache(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.CachePurge(rec, httptest.NewRequest(http.MethodPost, "/cache/purge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if m := cacheJSON(t, rec); m["status"] != "ok" {
		t.Fatalf("status=%v want ok", m["status"])
	}
}

// TestCachePurge_WithCache: purge clears a wired cache and answers ok.
func TestCachePurge_WithCache(t *testing.T) {
	h := &Handler{responseCache: cache.NewMemoryCache(8)}
	rec := httptest.NewRecorder()
	h.CachePurge(rec, httptest.NewRequest(http.MethodPost, "/cache/purge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if m := cacheJSON(t, rec); m["status"] != "ok" {
		t.Fatalf("status=%v want ok", m["status"])
	}
}
