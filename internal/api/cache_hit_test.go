package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestCacheHitBilling verifies that response-cache hits are logged,
// billed at the cache-read rate, and counted in usage — instead of
// being free/unlogged as before.
func TestCacheHitBilling(t *testing.T) {
	app := testhelper.New(t)
	app.Chat.SetCache(cache.NewMemoryCache(1000))
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.PriceModels([]string{"m"}, 1.0, 2.0)
	tok := app.AddToken("sk-t", "t")

	// Rate card: input $1/M, cache-read defaults to 0 (not set here) →
	// set it explicitly to verify the discount is applied.
	app.Store.UpsertModelCost(&model.ModelCost{
		Model: "m", InputPricePer1M: 1.0, OutputPricePer1M: 2.0, CacheReadPricePer1M: 0.1,
	})
	_ = app.Chat.ReloadModelCosts()

	body := `{"model":"m","temperature":0,"messages":[{"role":"user","content":"hello"}]}`
	call := func() int {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-t")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.Mux.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call(); code != 200 {
		t.Fatalf("first call: %d", code)
	}
	if code := call(); code != 200 {
		t.Fatalf("cache-hit call: %d", code)
	}

	logs, total, err := app.LogStore.Query(logstore.QueryFilter{TokenID: tok.ID, Limit: 10, Offset: 0}, nil)
	if err != nil || total < 2 {
		t.Fatalf("expected 2+ logs, got %d err=%v", total, err)
	}

	var hitCount, missCount int
	var hitCost, missCost float64
	for i := range logs {
		if logs[i].CacheHit {
			hitCount++
			hitCost = logs[i].BilledCostUSD
			if logs[i].RouterPath != "cache" {
				t.Errorf("cache-hit router_path should be 'cache', got %q", logs[i].RouterPath)
			}
		} else {
			missCount++
			missCost = logs[i].BilledCostUSD
		}
	}
	if hitCount < 1 || missCount < 1 {
		t.Fatalf("expected >=1 hit and >=1 miss, got hit=%d miss=%d", hitCount, missCount)
	}
	// Cache-read (10% of input) must make the hit cheaper than the miss.
	if hitCost <= 0 || missCost <= 0 {
		t.Errorf("both rows should be billed, hit=%f miss=%f", hitCost, missCost)
	}
	if hitCost >= missCost {
		t.Errorf("cache hit should cost less than a miss, hit=%f miss=%f", hitCost, missCost)
	}
}
