package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestHandler_Accessors(t *testing.T) {
	rt := runtime.New()
	cfg := &config.Config{}
	eng := &router.RouterEngine{}
	cp := pool.NewChannelPool()
	var st store.Store
	lb := broker.New[*model.Log](16)
	h := New(cfg, eng, cp, st, nil, lb, rt)

	if h.Limits() == nil {
		t.Fatal("Limits() should not be nil")
	}
	if h.Store() != nil {
		t.Fatal("Store() should be nil initially")
	}
	if h.Markup() != 1.0 {
		t.Fatalf("default markup: got %f", h.Markup())
	}
}

func TestHandler_SetStore(t *testing.T) {
	rt := runtime.New()
	cfg := &config.Config{}
	eng := &router.RouterEngine{}
	cp := pool.NewChannelPool()
	var st store.Store
	lb := broker.New[*model.Log](16)
	h := New(cfg, eng, cp, st, nil, lb, rt)

	var fakeStore store.Store
	h.SetStore(fakeStore)
	if h.Store() != fakeStore {
		t.Fatal("SetStore did not update store")
	}
}

func TestHandler_SetMarkup(t *testing.T) {
	rt := runtime.New()
	cfg := &config.Config{}
	eng := &router.RouterEngine{}
	cp := pool.NewChannelPool()
	var st store.Store
	lb := broker.New[*model.Log](16)
	h := New(cfg, eng, cp, st, nil, lb, rt)

	h.SetMarkup(1.5)
	if got := h.Markup(); got != 1.5 {
		t.Fatalf("markup: got %f, want 1.5", got)
	}
}

func TestHandler_ProviderFor_Default(t *testing.T) {
	rt := runtime.New()
	cfg := &config.Config{}
	eng := &router.RouterEngine{}
	cp := pool.NewChannelPool()
	var st store.Store
	lb := broker.New[*model.Log](16)
	h := New(cfg, eng, cp, st, nil, lb, rt)

	p := h.providerFor("nonexistent-protocol", false)
	if p == nil {
		t.Fatal("providerFor should fall back to default")
	}
}

func TestCalcCost_Basic(t *testing.T) {
	ch := &model.Channel{
		InputPrice:  1.0,
		OutputPrice: 2.0,
	}
	usage := provider.Usage{
		PromptTokens:     1000000,
		CompletionTokens: 500000,
	}
	cost := calcCost(ch, usage)
	if cost != 1.0+1.0 {
		t.Fatalf("basic cost: got %f, want 2.0", cost)
	}
}

func TestCalcCost_CachedTokens(t *testing.T) {
	ch := &model.Channel{
		InputPrice:          1.0,
		OutputPrice:         0.0,
		CachedInputDiscount: 0.1,
	}
	usage := provider.Usage{
		PromptTokens:        1000000,
		PromptTokensDetails: &provider.PromptTokensDetails{CachedTokens: 500000},
	}
	cost := calcCost(ch, usage)
	normal := 500000.0 / 1000000.0 * 1.0
	cached := 500000.0 / 1000000.0 * 1.0 * 0.1
	want := normal + cached
	if cost < want-0.0001 || cost > want+0.0001 {
		t.Fatalf("cached cost: got %f, want %f", cost, want)
	}
}

func TestCalcCost_ZeroUsage(t *testing.T) {
	ch := &model.Channel{InputPrice: 1.0, OutputPrice: 1.0}
	cost := calcCost(ch, provider.Usage{})
	if cost != 0 {
		t.Fatalf("zero usage: got %f, want 0", cost)
	}
}

func TestCalcCost_NilPromptTokensDetails(t *testing.T) {
	ch := &model.Channel{InputPrice: 1.0, OutputPrice: 1.0}
	usage := provider.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
	}
	cost := calcCost(ch, usage)
	if cost <= 0 {
		t.Fatalf("should have positive cost, got %f", cost)
	}
}

func TestPromptTokens_Nil(t *testing.T) {
	if got := promptTokens(nil); got != 0 {
		t.Fatalf("promptTokens(nil): got %d", got)
	}
}

func TestCompletionTokens_Nil(t *testing.T) {
	if got := completionTokens(nil); got != 0 {
		t.Fatalf("completionTokens(nil): got %d", got)
	}
}

// TestWriteError_ZeroStatus covers the panic fix for
// "invalid WriteHeader code 0" — when a provider returns (resp, 0, err)
// because the upstream HTTP call never produced a response
// (DNS failure, TLS handshake, context deadline), the chat
// handler used to forward status=0 to writeError which then
// panicked in net/http. Now writeError coerces status <= 0 to
// 502 Bad Gateway.
func TestWriteError_ZeroStatus(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, 0, "upstream blew up", "upstream_error")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q", ct)
	}
	var got errorResp
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.Error.Code != "upstream_error" {
		t.Fatalf("code: got %q", got.Error.Code)
	}
	if got.Error.Message != "upstream blew up" {
		t.Fatalf("message: got %q", got.Error.Message)
	}
	if got.Error.Type != "api_error" {
		t.Fatalf("type: got %q (should be api_error for 5xx)", got.Error.Type)
	}
}

func TestWriteError_NormalStatus(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "bad", "bad_request")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestWriteError_NegativeStatus(t *testing.T) {
	// Defensive: even negative statuses should not panic.
	w := httptest.NewRecorder()
	writeError(w, -1, "weird", "weird")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", w.Code)
	}
}
