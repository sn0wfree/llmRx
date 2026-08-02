package api

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
)

// autoRequest builds an authenticated request with the bench
// token's TokenInfo injected into the context, mirroring what
// middleware.Token does on the real server (the plain e2e benches
// exercise the no-TokenInfo fallback path).
func autoRequest(env *benchEnv, body string) *http.Request {
	req := newRequest("POST", "/v1/chat/completions", body, env.token)
	info, ok := env.tc.Lookup(env.token)
	if !ok {
		panic("bench token missing from tokencache")
	}
	return req.WithContext(context.WithValue(req.Context(), middleware.TokenInfoKey, info))
}

// addBenchAutoCombo attaches a mode:auto combo to the bench token.
// All tiers route to the single mock channel model so the bench
// measures gateway overhead on the auto path, not failover work.
func addBenchAutoCombo(tb testing.TB, env *benchEnv) {
	tb.Helper()
	tok, err := env.store.GetToken("sk-bench-token-1")
	if err != nil {
		tb.Fatalf("lookup token: %v", err)
	}
	combo := &model.TokenComboModel{
		TokenID: tok.ID,
		Name:    "auto",
		Mode:    model.ComboModeAuto,
		Tiers: map[string]model.TierConfig{
			"simple":   {Models: []string{"bench-model"}},
			"standard": {Models: []string{"bench-model"}},
			"complex":  {Models: []string{"bench-model"}},
			"agentic":  {Models: []string{"bench-model"}},
		},
		Fallback: []string{"bench-model"},
		Enabled:  true,
	}
	if err := env.store.CreateComboModel(combo); err != nil {
		tb.Fatalf("CreateComboModel(auto): %v", err)
	}
	if err := env.tc.Reload(); err != nil {
		tb.Fatalf("reload tokencache: %v", err)
	}
}

// autoBenchBody classifies as standard/complex (never "simple"):
// a short greeting would short-circuit tier sampling with the
// cold-start cheapest pick on every iteration.
const autoBenchBody = `{"model":"auto","messages":[{"role":"user","content":"Please analyze the database transaction deadlock we saw in production, compare the two possible fix approaches step by step, explain the trade-offs, and design a retry strategy with exponential backoff."}]}`

// BenchmarkE2E_AutoCombo_NonStreaming measures the full auto path
// (classify -> tier -> arm sample -> L1-L5 route -> mock call ->
// log) against a mock upstream.
func BenchmarkE2E_AutoCombo_NonStreaming(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()
	addBenchAutoCombo(b, env)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := autoRequest(env, autoBenchBody)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkE2E_AutoCombo_Streaming mirrors the streaming bench on
// the auto path.
func BenchmarkE2E_AutoCombo_Streaming(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()
	addBenchAutoCombo(b, env)

	body := `{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":true}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := autoRequest(env, body)
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkE2E_AutoCombo_Parallel measures concurrent throughput
// on the auto path (contended sampler lock + scorer).
func BenchmarkE2E_AutoCombo_Parallel(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()
	addBenchAutoCombo(b, env)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := autoRequest(env, autoBenchBody)
			rec := httptest.NewRecorder()
			env.srv.ServeHTTP(rec, req)
			if rec.Code != 200 {
				return
			}
		}
	})
}

// TestE2E_AutoLoadReport runs a quick load against the auto path
// and prints a report (visible in -v). Skipped under -short.
func TestE2E_AutoLoadReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load report in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()
	addBenchAutoCombo(t, env)

	const concurrency = 20
	const requests = 500

	var (
		totalNs  int64
		minNs    int64
		maxNs    int64
		statuses sync.Map
		failures int64
	)
	atomic.StoreInt64(&minNs, math.MaxInt64)

	jobs := make(chan int, requests)
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	start := time.Now()
	for w := 0; w < concurrency; w++ {
		go func() {
			defer wg.Done()
			for range jobs {
				req := autoRequest(env, autoBenchBody)
				rec := httptest.NewRecorder()
				t0 := time.Now()
				env.srv.ServeHTTP(rec, req)
				d := time.Since(t0)
				atomic.AddInt64(&totalNs, int64(d))
				for {
					cur := atomic.LoadInt64(&minNs)
					if int64(d) >= cur || atomic.CompareAndSwapInt64(&minNs, cur, int64(d)) {
						break
					}
				}
				for {
					cur := atomic.LoadInt64(&maxNs)
					if int64(d) <= cur || atomic.CompareAndSwapInt64(&maxNs, cur, int64(d)) {
						break
					}
				}
				if rec.Code != 200 {
					atomic.AddInt64(&failures, 1)
				}
				v, _ := statuses.LoadOrStore(rec.Code, new(int64))
				atomic.AddInt64(v.(*int64), 1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	t.Logf("== E2E auto-combo load report (non-streaming, mock upstream) ==")
	t.Logf("  concurrency: %d   total requests: %d", concurrency, requests)
	t.Logf("  wall time:    %s", elapsed)
	t.Logf("  throughput:   %.1f req/s", float64(requests)/elapsed.Seconds())
	t.Logf("  avg latency:  %s", time.Duration(atomic.LoadInt64(&totalNs)/int64(requests)))
	t.Logf("  min latency:  %s", time.Duration(atomic.LoadInt64(&minNs)))
	t.Logf("  max latency:  %s", time.Duration(atomic.LoadInt64(&maxNs)))
	statuses.Range(func(k, v any) bool {
		t.Logf("  status %d:   %d responses", k, atomic.LoadInt64(v.(*int64)))
		return true
	})
	t.Logf("  failures:     %d", atomic.LoadInt64(&failures))
}
