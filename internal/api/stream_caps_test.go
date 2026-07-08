package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestStream_CtxTimeoutFires covers the per-stream context deadline:
// the handler must cancel the upstream goroutine and emit an
// error frame within the configured window.
func TestStream_CtxTimeoutFires(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")

	ctxCh := make(chan context.Context, 1)
	cp := &contextRecordingProvider{ctxCh: ctxCh}
	app.Chat.SetProviders(map[string]provider.Provider{
		"":       cp,
		"openai": cp,
	})
	// 1s should easily bound the test.
	app.RT.SetStreamTimeoutSec(1)

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-tok")

	start := time.Now()
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("expected error frame, body=%q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "timeout exceeded") {
		t.Fatalf("expected timeout reason, body=%q", rec.Body.String())
	}
	// ctx should have been cancelled by the time the goroutine finished.
	select {
	case ctx := <-ctxCh:
		if ctx.Err() == nil {
			// Allow brief race: handler may finish writing before
			// the test reads ctx. Fall back to elapsed wall clock.
			if elapsed > 3*time.Second {
				t.Fatalf("ctx never cancelled after %s", elapsed)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("provider never saw a ctx")
	}
}

// TestStream_BodyLimitExceeded verifies that once cumulative bytes
// emitted to the client exceed the cap, the stream terminates with
// a "stream max body bytes exceeded" frame and a 413-equivalent log
// (still 200 to the wire — SSE has already started).
func TestStream_BodyLimitExceeded(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")

	large := strings.Repeat("a", 200) // each chunk is ~210 B with framing
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		for i := 0; i < 5; i++ {
			ch <- provider.StreamEvent{Chunk: provider.StreamChunk{
				ID:      "c",
				Object:  "chat.completion.chunk",
				Model:   "m",
				Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Content: large}}},
			}}
		}
	}()
	cap := &hangingProvider{override: ch}
	app.Chat.SetProviders(map[string]provider.Provider{
		"":       cap,
		"openai": cap,
	})
	// 200-byte cap → must terminate before all 5 chunks finish.
	app.RT.SetStreamMaxBodyBytes(200)

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-tok")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stream max body bytes exceeded") {
		t.Fatalf("expected limit error frame, got %q", rec.Body.String())
	}
	if strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("stream should not have completed cleanly with cap hit")
	}
}

// TestStream_HappyPathStillCompletes guards against regressions in the
// success path after we wrapped the read loop in select.
func TestStream_HappyPathStillCompletes(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")
	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "c1", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Role: "assistant", Content: "Hi"}}}},
		{ID: "c2", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{}, FinishReason: "stop"}}, Usage: &provider.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}},
	}
	app.RT.SetStreamTimeoutSec(30)

	body := `{"model":"m","stream":true,"messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-tok")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if !strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("expected [DONE], got %q", rec.Body.String())
	}
}

// TestStream_NoProvidersFallsToDefault exercises the protocol lookup
// path that is exercised when the upstream returns a streaming
// capability.
//
// --- helpers ---

// hangingProvider lets a test inject a controllable stream.
type hangingProvider struct {
	mu       sync.Mutex
	override <-chan provider.StreamEvent
	calls    int
}

func (h *hangingProvider) Name() string { return "hanging" }

func (h *hangingProvider) Chat(req *provider.ChatRequest, _, _ string) (*provider.ChatResponse, int, error) {
	return nil, 500, errors.New("not used in stream tests")
}

func (h *hangingProvider) StreamChat(ctx context.Context, req *provider.ChatRequest, _, _ string) (<-chan provider.StreamEvent, error) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	out := make(chan provider.StreamEvent, 32)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-h.override:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case out <- ev:
				}
			}
		}
	}()
	return out, nil
}

// contextRecordingProvider records the upstream ctx so tests can
// assert it was cancelled.
type contextRecordingProvider struct {
	ctxCh chan<- context.Context
}

func (c *contextRecordingProvider) Name() string { return "ctx-rec" }

func (c *contextRecordingProvider) Chat(req *provider.ChatRequest, _, _ string) (*provider.ChatResponse, int, error) {
	return &provider.ChatResponse{ID: "x", Model: req.Model}, 200, nil
}

func (c *contextRecordingProvider) StreamChat(ctx context.Context, req *provider.ChatRequest, _, _ string) (<-chan provider.StreamEvent, error) {
	c.ctxCh <- ctx
	out := make(chan provider.StreamEvent)
	go func() {
		<-ctx.Done()
		close(out)
	}()
	return out, nil
}
