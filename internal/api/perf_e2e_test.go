package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

// BenchmarkE2E_NonStreaming measures end-to-end non-streaming chat
// request latency against a mock upstream. Reports per-request
// gateway overhead (token lookup, routing, provider call, log
// write).
func BenchmarkE2E_NonStreaming(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := newRequest("POST", "/v1/chat/completions", body, env.token)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkE2E_Streaming measures end-to-end streaming latency.
// The mock yields 10 chunks.
func BenchmarkE2E_Streaming(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}],"stream":true}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := newRequest("POST", "/v1/chat/completions", body, env.token)
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkE2E_Parallel measures concurrent throughput.
func BenchmarkE2E_Parallel(b *testing.B) {
	env := newBenchEnv(b)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}`

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := newRequest("POST", "/v1/chat/completions", body, env.token)
			rec := httptest.NewRecorder()
			env.srv.ServeHTTP(rec, req)
			if rec.Code != 200 {
				return
			}
		}
	})
}

// ---------- helpers ----------

type benchEnv struct {
	store store.Store
	pool  *pool.ChannelPool
	tc    *tokencache.Cache
	eng   *router.RouterEngine
	srv   http.Handler
	token string
}

func (e *benchEnv) Close() {
	provider.SetFactoryOverride(nil)
	_ = e.store.Close()
}

func newBenchEnv(tb testing.TB) *benchEnv {
	tb.Helper()
	dir := tb.TempDir()

	// Silence stdout-spammy log messages from the router / store
	// during benchmarks so the output is readable.
	log.SetOutput(io.Discard)

	// Generate a fixed 32-byte key for deterministic benchmarks.
	hexKey := "000102030405060708090a0b0c0d0e0f" +
		"101112131415161718191a1b1c1d1e1f"
	sec, err := secrets.FromHexKey(hexKey)
	if err != nil {
		tb.Fatal(err)
	}

	st, err := store.OpenSQLite(dir + "/bench.db")
	if err != nil {
		tb.Fatal(err)
	}
	st.SetSecrets(sec)

	// Seed: one channel + one key + one token
	if err := st.CreateChannel(&model.Channel{
		Name: "bench", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.example.com/v1", Models: []string{"bench-model"},
		Priority: 5, Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		tb.Fatal(err)
	}
	if err := st.CreateKey(&model.Key{
		ChannelID: 1, Key: "sk-bench-secret-1", KeyMasked: "sk-***1",
		Status: model.KeyActive, CreatedAt: time.Now(),
	}); err != nil {
		tb.Fatal(err)
	}
	tok := &model.Token{
		Key: "sk-bench-token-1", Name: "bench-token", Status: model.TokenActive,
		RPM: 0, TPM: 0, CreatedAt: time.Now(),
	}
	if err := st.CreateToken(tok); err != nil {
		tb.Fatal(err)
	}

	cp := pool.NewChannelPool()
	_ = cp.LoadFromStore(st)
	tc := tokencache.New(st)
	_ = tc.Reload()
	rt := runtime.New()
	eng := router.New(st, cp)
	_ = rt

	mock := newMockProvider()
	provider.SetFactoryOverride(mock)

	apiHandler := New(&config.Config{}, eng, cp, st, broker.New[*model.Log](64), rt)
	apiHandler.SetProviders(map[string]provider.Provider{
		"":          mock,
		"openai":     mock,
		"openai-compatible": mock,
	})

	r := chi.NewRouter()
	r.Mount("/v1", apiHandler.Routes())

	return &benchEnv{
		store: st,
		pool:  cp,
		tc:    tc,
		eng:   eng,
		srv:   r,
		token: "sk-bench-token-1",
	}
}

func newRequest(method, path, body, token string) *http.Request {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// ---------- mock provider ----------

// mockProvider is a fast in-memory stub. Returns a fixed OpenAI
// response (or 10-chunk SSE stream when streaming).
type mockProvider struct{}

func newMockProvider() *mockProvider { return &mockProvider{} }

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(req *provider.ChatRequest, apiKey, baseURL string) (*provider.ChatResponse, int, error) {
	return &provider.ChatResponse{
		ID:      "chatcmpl-mock",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []provider.Choice{{
			Index:        0,
			Message:      provider.Message{Role: "assistant", Content: "hi"},
			FinishReason: "stop",
		}},
		Usage: provider.Usage{
			PromptTokens:     10,
			CompletionTokens: 200,
			TotalTokens:      210,
		},
	}, http.StatusOK, nil
}

func (m *mockProvider) StreamChat(ctx context.Context, req *provider.ChatRequest, apiKey, baseURL string) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 12)
	go func() {
		defer close(ch)
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				return
			case ch <- provider.StreamEvent{
				Chunk: provider.StreamChunk{
					ID:      "chatcmpl-mock",
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []provider.StreamChoice{{
						Index: 0,
						Delta: provider.Message{Content: "x"},
					}},
				},
			}:
			}
		}
		select {
		case ch <- provider.StreamEvent{Chunk: provider.StreamChunk{}}: // [DONE] handled by api package
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// ---------- result printer ----------

// TestE2E_LoadReport runs a quick load and prints a report. Uses
// t.Run / t.Logf so output is visible in -v mode. Skipped under
// -short.
func TestE2E_LoadReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load report in -short mode")
	}
	env := newBenchEnv(t)
	defer env.Close()

	body := `{"model":"bench-model","messages":[{"role":"user","content":"hi"}]}`
	const concurrency = 20
	const requests = 500

	var (
		total      time.Duration
		minLatency = time.Hour
		maxLatency time.Duration
		statuses   sync.Map
		failures   int64
	)

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
				req := newRequest("POST", "/v1/chat/completions", body, env.token)
				rec := httptest.NewRecorder()
				t0 := time.Now()
				env.srv.ServeHTTP(rec, req)
				d := time.Since(t0)
				total += d
				if d < minLatency {
					minLatency = d
				}
				if d > maxLatency {
					maxLatency = d
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

	t.Logf("== E2E load report (non-streaming, mock upstream) ==")
	t.Logf("  concurrency: %d   total requests: %d", concurrency, requests)
	t.Logf("  wall time:    %s", elapsed)
	t.Logf("  throughput:   %.1f req/s", float64(requests)/elapsed.Seconds())
	t.Logf("  avg latency:  %s", total/time.Duration(requests))
	t.Logf("  min latency:  %s", minLatency)
	t.Logf("  max latency:  %s", maxLatency)
	statuses.Range(func(k, v any) bool {
		t.Logf("  status %d:   %d responses", k, atomic.LoadInt64(v.(*int64)))
		return true
	})
	t.Logf("  failures:     %d", atomic.LoadInt64(&failures))
}
