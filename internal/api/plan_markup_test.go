package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestGateway_PlanMarkupApplied(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1.0, 0.0, "sk-key")
	app.Chat.SetMarkup(1.0)

	plan := &model.Plan{Name: "pro", MarkupRatio: 2.0, Status: 1}
	app.Store.CreatePlan(plan)

	tok := app.AddToken("sk-t", "t")
	tok.PlanID = plan.ID
	app.Store.UpdateToken(tok)
	app.Cache.Reload()

	cp := &capturingProvider{
		resp: &provider.ChatResponse{
			ID: "c1", Object: "chat.completion", Model: "m",
			Choices: []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
			Usage:   provider.Usage{PromptTokens: 1000000, CompletionTokens: 0, TotalTokens: 1000000},
		},
		status: 200,
	}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	logs, _, _ := app.LogStore.Query(logstore.QueryFilter{Limit: 10, Offset: 0}, nil)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	l := logs[0]
	if l.RealCostUSD < 0.99 || l.RealCostUSD > 1.01 {
		t.Errorf("real cost: %f", l.RealCostUSD)
	}
	if l.BilledCostUSD < 1.99 || l.BilledCostUSD > 2.01 {
		t.Errorf("billed cost should be 2x with plan markup: got %f", l.BilledCostUSD)
	}
}
