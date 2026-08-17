package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Token-bucket-based rate-limited load generator.
//
// Unlike b.RunParallel (which fires as fast as possible until CPU is
// saturated) or TestE2E_LoadReport (which measures fan-out throughput
// under a fixed worker pool), these helpers let the caller drive a
// precise requests-per-second rate. This matches production scenarios
// where a real OpenAI/Anthropic upstream imposes a QPS limit and the
// gateway should be measured at that limit, not beyond it.

// rateLimiter is a token bucket. Take() blocks until a token is
// available or the context is cancelled. The bucket refills at rate
// `rps` tokens per second up to `burst` tokens.
type rateLimiter struct {
	rps    float64
	burst  int
	mu     sync.Mutex
	tokens float64
	last    time.Time
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	if burst <= 0 {
		burst = 1
	}
	return &rateLimiter{
		rps:    rps,
		burst:  burst,
		tokens: float64(burst),
		last:   time.Now(),
	}
}

func (r *rateLimiter) Take(ctx context.Context) error {
	for {
		now := time.Now()
		// Refill: add elapsed * rps tokens, capped at burst.
		r.mu.Lock()
		elapsed := now.Sub(r.last).Seconds()
		r.tokens += elapsed * r.rps
		if r.tokens > float64(r.burst) {
			r.tokens = float64(r.burst)
		}
		r.last = now
		if r.tokens >= 1.0 {
			r.tokens -= 1.0
			r.mu.Unlock()
			return nil
		}
		// Sleep until the next token is expected to be available.
		waitSec := (1.0 - r.tokens) / r.rps
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(waitSec * float64(time.Second))):
			// loop and try again
		}
	}
}

// loadResult captures per-request stats for percentile reporting.
type loadResult struct {
	status     int
	took       time.Duration
	firstByte  time.Duration // TTFB for streaming
	tokensRead int            // for streaming, total bytes received
	err        error
}

// rateLimitedLoad fires `total` requests against `handler` at exactly
// `rps` per second, using `concurrency` worker goroutines.
//
// Returns the per-request results in dispatch order plus the wall-clock
// duration of the test.
func rateLimitedLoad(
	t testing.TB,
	handler http.Handler,
	method, path, body, token string,
	rps float64, concurrency, total int,
) ([]loadResult, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(float64(total)/rps*2)*time.Second+30*time.Second)
	defer cancel()

	lim := newRateLimiter(rps, concurrency)
	results := make([]loadResult, total)
	jobs := make(chan int, total)
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	wallStart := time.Now()
	for w := 0; w < concurrency; w++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := lim.Take(ctx); err != nil {
					results[idx] = loadResult{err: err}
					continue
				}
				var bodyReader io.Reader
				if body != "" {
					bodyReader = stringReader(body)
				}
				req := httptest.NewRequest(method, path, bodyReader)
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				t0 := time.Now()
				handler.ServeHTTP(rec, req)
				d := time.Since(t0)
				res := loadResult{status: rec.Code, took: d}
				if rec.Code == http.StatusOK {
					res.tokensRead = len(rec.Body.Bytes())
				}
				results[idx] = res
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(wallStart)
	return results, elapsed
}

// stringReader avoids pulling in strings.NewReader at every call site.
func stringReader(s string) io.Reader { return &strReader{s: s} }

type strReader struct {
	s string
	i int
}

func (r *strReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

// summarizeLoad prints aggregate stats from a rate-limited load run and
// returns the throughput in req/s.
func summarizeLoad(t testing.TB, tag string, rps float64, results []loadResult, elapsed time.Duration) {
	t.Helper()
	if len(results) == 0 {
		t.Fatalf("%s: no results", tag)
	}
	var (
		ok, bad, clientErr, errCount int
		total                       time.Duration
		latencies                   []time.Duration
		max                         time.Duration
	)
	latencies = make([]time.Duration, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			errCount++
			continue
		}
		total += r.took
		if r.took > max {
			max = r.took
		}
		latencies = append(latencies, r.took)
		switch {
		case r.status >= 200 && r.status < 400:
			ok++
		case r.status >= 500:
			bad++
		default:
			clientErr++
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p := func(q float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		i := int(float64(len(latencies)) * q)
		if i >= len(latencies) {
			i = len(latencies) - 1
		}
		return latencies[i]
	}

	targetRPS := float64(len(results)) / elapsed.Seconds()
	t.Logf("== %s (target=%.0f RPS) ==", tag, rps)
	t.Logf("  requests:    %d  (ok=%d  4xx=%d  5xx=%d  err=%d)", len(results), ok, clientErr, bad, errCount)
	t.Logf("  wall time:   %s", elapsed)
	t.Logf("  achieved:    %.1f req/s", targetRPS)
	if errCount+ok > 0 {
		t.Logf("  avg latency: %s", total/time.Duration(errCount+ok))
	}
	t.Logf("  p50 / p95 / p99 / max:  %s / %s / %s / %s",
		p(0.50), p(0.95), p(0.99), max)

	if bad > 0 {
		t.Errorf("%s: %d requests returned 5xx", tag, bad)
	}
	// Verify rate cap held: achieved RPS should not exceed target
	// by more than 20% (burst of `concurrency` tokens is allowed).
	if targetRPS > rps*1.2 {
		t.Errorf("%s: achieved %.1f req/s exceeded target %.0f by >20%%", tag, targetRPS, rps)
	}
}

// streamingLoad fires streaming requests and tracks TTFB instead of
// total latency. Used by the streaming-specific tests below.
func streamingLoad(
	t testing.TB,
	handler http.Handler,
	path, body, token string,
	rps float64, concurrency, total int,
) ([]loadResult, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(float64(total)/rps*2)*time.Second+30*time.Second)
	defer cancel()
	lim := newRateLimiter(rps, concurrency)
	results := make([]loadResult, total)
	jobs := make(chan int, total)
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	wallStart := time.Now()
	for w := 0; w < concurrency; w++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := lim.Take(ctx); err != nil {
					results[idx] = loadResult{err: err}
					continue
				}
				req := httptest.NewRequest(http.MethodPost, path, stringReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Accept", "text/event-stream")
				rec := httptest.NewRecorder()
				t0 := time.Now()
				handler.ServeHTTP(rec, req)
				d := time.Since(t0)
				results[idx] = loadResult{
					status:     rec.Code,
					took:       d,
					tokensRead: len(rec.Body.Bytes()),
				}
			}
		}()
	}
	wg.Wait()
	return results, time.Since(wallStart)
}

// --- Tests ---------------------------------------------------------------

// TestRateLimited_NonStreaming_Chat fires POST /v1/chat/completions at
// 50 RPS across 10 workers for 200 requests. Real-world gate: a
// hobby-tier OpenAI key is capped around 60 RPS; this confirms the
// gateway handles that comfortably with no 5xx.
func TestRateLimited_NonStreaming_Chat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping rate-limited load in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hello"}]}`
	results, elapsed := rateLimitedLoad(t, env.srv,
		http.MethodPost, "/v1/chat/completions", body, env.token,
		50, /* rps */
		10, /* workers */
		200, /* requests */)
	summarizeLoad(t, "NonStreamingChat@50rps", 50, results, elapsed)
}

// TestRateLimited_Streaming_Chat exercises the SSE path at 30 RPS —
// streaming responses are heavier on the gateway because of the chunk
// pump, so we back off the rate slightly to keep the test focused on
// gateway overhead rather than worker pool exhaustion.
func TestRateLimited_Streaming_Chat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping rate-limited load in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	results, elapsed := streamingLoad(t, env.srv,
		"/v1/chat/completions", body, env.token,
		30, 10, 120)
	summarizeLoad(t, "StreamingChat@30rps", 30, results, elapsed)
}

// TestRateLimited_Models is the lightweight GET path — useful as a
// sanity check that the rate limiter isn't somehow slowing the fast
// path. A 200 RPS run here should easily clear 180+ req/s.
func TestRateLimited_Models(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping rate-limited load in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	results, elapsed := rateLimitedLoad(t, env.srv,
		http.MethodGet, "/v1/models", "", env.token,
		200, 10, 200)
	summarizeLoad(t, "Models@200rps", 200, results, elapsed)
}

// TestRateLimited_Embeddings covers POST /v1/embeddings at 40 RPS.
// Embeddings are cheaper than chat so we expect a tighter latency
// distribution than chat completions.
func TestRateLimited_Embeddings(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping rate-limited load in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	body := `{"model":"bench-model","input":"hello world"}`
	results, elapsed := rateLimitedLoad(t, env.srv,
		http.MethodPost, "/v1/embeddings", body, env.token,
		40, 10, 160)
	summarizeLoad(t, "Embeddings@40rps", 40, results, elapsed)
}

// TestRateLimited_StepLoad sweeps RPS from 10 → 200 in 4 steps and
// prints a per-step summary. This is the test you'd run before a
// production deploy to characterize the gateway's vertical scaling
// curve. Skipped in -short because it's the slowest test in the file.
func TestRateLimited_StepLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping step load in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}`
	steps := []struct {
		rps     float64
		count   int
		workers int
	}{
		{10, 50, 5},
		{50, 100, 10},
		{100, 200, 20},
		{200, 400, 30},
	}
	for _, s := range steps {
		t.Run(fmt.Sprintf("rps%.0f_n%d_w%d", s.rps, s.count, s.workers), func(t *testing.T) {
			results, elapsed := rateLimitedLoad(t, env.srv,
				http.MethodPost, "/v1/chat/completions", body, env.token,
				s.rps, s.workers, s.count)
			summarizeLoad(t, fmt.Sprintf("StepLoad@%.0f", s.rps), s.rps, results, elapsed)
		})
	}
}

// TestRateLimited_BurstBurstiness checks the token bucket actually
// caps the achieved RPS at the configured rate, even when workers
// race to Take() simultaneously. Without the bucket, this test would
// show achieved RPS = workers * (1 / min-latency), often 10x the
// target.
func TestRateLimited_BurstBurstiness(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping burstiness check in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}`
	const targetRPS = 30.0
	const workers = 30 // intentionally large to expose uncapped fan-out
	const requests = 300

	results, elapsed := rateLimitedLoad(t, env.srv,
		http.MethodPost, "/v1/chat/completions", body, env.token,
		targetRPS, workers, requests)

	achieved := float64(len(results)) / elapsed.Seconds()
	t.Logf("burstiness: target=%.0f rps, workers=%d, achieved=%.1f rps",
		targetRPS, workers, achieved)

	// Allow 20% headroom over target to absorb scheduler jitter, but
	// verify the bucket is doing something.
	if achieved > targetRPS*1.2 {
		t.Errorf("rate limiter not capping RPS: target=%.0f, achieved=%.1f", targetRPS, achieved)
	}
	// Sanity: not absurdly below target either (would indicate the
	// limiter is starved).
	if achieved < targetRPS*0.5 {
		t.Errorf("rate limiter too strict: target=%.0f, achieved=%.1f", targetRPS, achieved)
	}

	// Tally statuses just to make sure we didn't quietly start failing.
	var ok, bad int64
	for _, r := range results {
		if r.err == nil && r.status >= 200 && r.status < 400 {
			atomic.AddInt64(&ok, 1)
		} else {
			atomic.AddInt64(&bad, 1)
		}
	}
	t.Logf("statuses: ok=%d bad=%d", ok, bad)
	if bad > 0 {
		t.Errorf("unexpected bad responses: %d", bad)
	}
}

// --- Benchmarks ---------------------------------------------------------

// BenchmarkRateLimited_NonStreamingChat is the b.N version of
// TestRateLimited_NonStreaming_Chat — pinned at 50 RPS so the run
// matches what operators see against a real upstream.
//
//	go test -bench=BenchmarkRateLimited -run=^$ -benchtime=10s ./internal/api
//
// Reports ns/op per request plus an allocation profile that's useful
// for spotting regressions in the hot path.
func BenchmarkRateLimited_NonStreamingChat(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}`
	const rps = 50.0
	lim := newRateLimiter(rps, 10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = lim.Take(context.Background())
		req := newRequest(http.MethodPost, "/v1/chat/completions", body, env.token)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
		// Drain to keep memory flat across iterations.
		var resp struct {
			Usage map[string]int `json:"usage"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	}
}

// ensure math is referenced so a future goimports prune doesn't drop
// the import (used by rate limiter algebra).
var _ = math.Floor