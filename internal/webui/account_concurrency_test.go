package webui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Concurrency knobs — small enough to keep tests under a few hundred
// milliseconds on a CI box, large enough to expose real contention in
// the SQLite log store and template renderer.
const (
	concurrencyWorkers = 50
	concurrencyIters   = 40 // total requests per test = workers * iters
)

// result tracks the outcome of each request in the fan-out so the test
// can assert no errors, no 5xx, and reasonable latency distribution.
type result struct {
	status int
	took   time.Duration
}

// runConcurrent dispatches `n` GET requests to `path` across `workers`
// goroutines, each carrying a session cookie. Returns a slice of
// results in dispatch order.
//
// The Handler.UserRoutes() instance is the same one the server mounts at
// /account; using ServeHTTP directly avoids the noise of a real network
// listener while still exercising chi middleware, the session lookup,
// the SQLite store, the template renderer, and the logstore query.
func runConcurrent(t *testing.T, h *Handler, cookie, path string, workers, n int) []result {
	t.Helper()
	results := make([]result, n)
	jobs := make(chan int, n)
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
				rec := httptest.NewRecorder()
				start := time.Now()
				h.UserRoutes().ServeHTTP(rec, req)
				results[idx] = result{
					status: rec.Code,
					took:   time.Since(start),
				}
			}
		}()
	}
	wg.Wait()
	return results
}

// summarize prints aggregate stats and returns the slowest single
// request duration. The intent is to make a failure self-describing
// instead of just "FAIL:".
func summarize(t *testing.T, tag string, results []result) time.Duration {
	t.Helper()
	if len(results) == 0 {
		t.Fatalf("%s: no results", tag)
	}
	var (
		ok, bad, clientErr int
		max                time.Duration
		total              time.Duration
	)
	for _, r := range results {
		total += r.took
		if r.took > max {
			max = r.took
		}
		switch {
		case r.status >= 200 && r.status < 400:
			ok++
		case r.status >= 500:
			bad++
		default:
			clientErr++ // 4xx, redirect-mismatch, etc.
		}
	}
	avg := total / time.Duration(len(results))
	t.Logf("%s: n=%d  ok=%d  4xx=%d  5xx=%d  avg=%s  max=%s",
		tag, len(results), ok, clientErr, bad, avg, max)
	if bad > 0 {
		t.Errorf("%s: %d requests returned 5xx — likely contention bug", tag, bad)
	}
	if ok != len(results) {
		t.Errorf("%s: %d requests did not return 2xx/3xx (4xx=%d)",
			tag, len(results)-ok, clientErr)
	}
	return max
}

// seedRecentLogsTB pre-populates the log store with `n` rows tagged to
// the user's token so MeUsagePage and the recent-activity table have
// something to render — otherwise they'd be cheap no-ops and would
// hide contention in the JSON marshalling path.
//
// Accepts testing.TB so the same helper is reusable from benchmarks.
func seedRecentLogsTB(tb testing.TB, h *Handler, st store.Store, tokenName string, n int) {
	tb.Helper()
	tok, err := st.GetTokenByName(tokenName)
	if err != nil || tok == nil {
		tb.Fatalf("get token %q: %v", tokenName, err)
	}
	for i := 0; i < n; i++ {
		row := &model.Log{
			TokenID:          tok.ID,
			Model:            "gpt-4",
			PromptTokens:     10,
			CompletionTokens: 5,
			BilledCostUSD:    0.01,
			StatusCode:       200,
			CreatedAt:        time.Now().Add(-time.Duration(i) * time.Second),
		}
		if err := h.logStore.Insert(row); err != nil {
			tb.Fatalf("insert log %d: %v", i, err)
		}
	}
	// logStore is async — drain before any reader-side goroutine races
	// for the not-yet-flushed rows.
	h.logStore.Flush()
}

// --- Tests ---------------------------------------------------------------

// TestConcurrent_AccountHome loads /account/ across many goroutines and
// confirms every request renders successfully with no template or store
// contention. This is the single most-hit page for a logged-in user.
func TestConcurrent_AccountHome(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "conc", model.RoleUser, 50, 12.5)
	// Pre-seed some recent activity so the template exercises its
	// real path (table render + log query) instead of the empty branch.
	seedRecentLogsTB(t, h, st, "conc", 10)

	results := runConcurrent(t, h, cookie, "/", concurrencyWorkers, concurrencyWorkers*concurrencyIters)
	max := summarize(t, "AccountHome", results)
	// 500ms ceiling is loose; the renderer + logstore.Query should
	// easily beat this on a single node. CI noise accounts for the
	// headroom.
	if max > 500*time.Millisecond {
		t.Errorf("AccountHome: slowest request %s exceeded 500ms budget", max)
	}
}

// TestConcurrent_AccountUsage hits the heavier /account/usage page
// which runs both TimeSeries AND TopByField aggregations, plus a full
// HTML chart. It's the most DB-heavy of the three user pages.
func TestConcurrent_AccountUsage(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "usage-conc", model.RoleUser, 50, 0)
	seedRecentLogsTB(t, h, st, "usage-conc", 100)

	results := runConcurrent(t, h, cookie, "/usage", concurrencyWorkers, concurrencyWorkers*concurrencyIters)
	max := summarize(t, "AccountUsage", results)
	if max > 1*time.Second {
		t.Errorf("AccountUsage: slowest request %s exceeded 1s budget", max)
	}
}

// TestConcurrent_AccountTopup is the lightest of the three pages
// (no log queries) — included so we have a baseline for renderer cost
// alone, in case AccountHome regressions turn out to be DB-bound.
func TestConcurrent_AccountTopup(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "topup-conc", model.RoleUser, 50, 0)

	results := runConcurrent(t, h, cookie, "/topup", concurrencyWorkers, concurrencyWorkers*concurrencyIters)
	max := summarize(t, "AccountTopup", results)
	if max > 500*time.Millisecond {
		t.Errorf("AccountTopup: slowest request %s exceeded 500ms budget", max)
	}
}

// TestConcurrent_AccountExportCSV exercises the streaming CSV path.
// It writes a new CSV per request, so each goroutine allocates its own
// buffer — useful for catching write races in the CSV writer.
func TestConcurrent_AccountExportCSV(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "csv-conc", model.RoleUser, 50, 0)
	seedRecentLogsTB(t, h, st, "csv-conc", 50)

	results := runConcurrent(t, h, cookie, "/export.csv", concurrencyWorkers, concurrencyWorkers*concurrencyIters)
	max := summarize(t, "AccountExportCSV", results)
	if max > 1*time.Second {
		t.Errorf("AccountExportCSV: slowest request %s exceeded 1s budget", max)
	}
}

// TestConcurrent_MixedPages simulates a realistic user session: the
// same user hops between /, /usage, /topup. This is the most likely
// shape of production traffic and surfaces cross-request contention
// that single-page tests would miss (e.g. shared SQLite WAL contention).
func TestConcurrent_MixedPages(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "mix", model.RoleUser, 50, 5)
	seedRecentLogsTB(t, h, st, "mix", 100)

	paths := []string{"/", "/usage", "/topup", "/"}
	const total = concurrencyWorkers * concurrencyIters
	results := make([]result, total)
	jobs := make(chan int, total)
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(concurrencyWorkers)
	for w := 0; w < concurrencyWorkers; w++ {
		go func() {
			defer wg.Done()
			for jobIdx := range jobs {
				path := paths[jobIdx%len(paths)]
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
				rec := httptest.NewRecorder()
				start := time.Now()
				h.UserRoutes().ServeHTTP(rec, req)
				results[jobIdx] = result{status: rec.Code, took: time.Since(start)}
			}
		}()
	}
	wg.Wait()

	max := summarize(t, "MixedPages", results)
	if max > 1*time.Second {
		t.Errorf("MixedPages: slowest request %s exceeded 1s budget", max)
	}
}

// TestConcurrent_MultipleUsers checks the session cache and store
// lookup paths under contention from distinct sessions. Each goroutine
// authenticates as a different user, simulating a small org where
// everyone hits /account at once.
func TestConcurrent_MultipleUsers(t *testing.T) {
	h, st := newTestWebUI(t)
	const users = 10
	cookies := make([]string, users)
	for i := 0; i < users; i++ {
		_, cookie := seedUserWithPlan(t, h, st,
			fmt.Sprintf("multi-%d", i), model.RoleUser, 50, 0)
		cookies[i] = cookie
		seedRecentLogsTB(t, h, st, fmt.Sprintf("multi-%d", i), 5)
	}

	var (
		wg      sync.WaitGroup
		okCount int64
	)
	wg.Add(users)
	for i := 0; i < users; i++ {
		cookie := cookies[i]
		go func() {
			defer wg.Done()
			res := runConcurrent(t, h, cookie, "/", concurrencyIters, concurrencyIters)
			for _, r := range res {
				if r.status >= 200 && r.status < 400 {
					atomic.AddInt64(&okCount, 1)
				}
			}
		}()
	}
	wg.Wait()

	if want := int64(users * concurrencyIters); okCount != want {
		t.Errorf("MultipleUsers: ok=%d, want %d", okCount, want)
	}
}

// --- Benchmarks ----------------------------------------------------------

// BenchmarkAccountHome measures per-request render cost for /account
// against a fully-seeded user. Run with:
//
//	go test -bench=BenchmarkAccountHome -run=^$ ./internal/webui
//
// Reports ns/op for trend tracking without goroutine-orchestration
// noise. Compare against the latency distribution in
// TestConcurrent_AccountHome for the real-world shape.
func BenchmarkAccountHome(b *testing.B) {
	h, st := newTestWebUI(b)
	_, cookie := seedUserWithPlan(b, h, st, "bench-home", model.RoleUser, 50, 0)
	seedRecentLogsTB(b, h, st, "bench-home", 10)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.UserRoutes().ServeHTTP(rec, req)
	}
}

// BenchmarkAccountUsage is the heavier cousin — runs the full
// /account/usage render path including chart + aggregations.
func BenchmarkAccountUsage(b *testing.B) {
	h, st := newTestWebUI(b)
	_, cookie := seedUserWithPlan(b, h, st, "bench-usage", model.RoleUser, 50, 0)
	seedRecentLogsTB(b, h, st, "bench-usage", 100)

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.UserRoutes().ServeHTTP(rec, req)
	}
}