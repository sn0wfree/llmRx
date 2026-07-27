package api

import (
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
	h := New(cfg, eng, cp, st, lb, rt)

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
	h := New(cfg, eng, cp, st, lb, rt)

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
	h := New(cfg, eng, cp, st, lb, rt)

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
	h := New(cfg, eng, cp, st, lb, rt)

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
		InputPrice:         1.0,
		OutputPrice:        0.0,
		CachedInputDiscount: 0.1,
	}
	usage := provider.Usage{
		PromptTokens: 1000000,
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
