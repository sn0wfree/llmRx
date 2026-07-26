package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestGateway_StreamingUsageForwarded(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "sk-key")
	app.AddToken("sk-t", "t")

	cp := &capturingProvider{
		streamChk: []provider.StreamChunk{
			{ID: "1", Object: "chat.completion.chunk", Model: "m",
				Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Content: "hi"}}}},
			{ID: "2", Object: "chat.completion.chunk", Model: "m",
				Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{}, FinishReason: "stop"}},
				Usage:   &provider.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}},
		},
	}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"usage"`) {
		t.Errorf("SSE output should contain usage block:\n%s", rec.Body.String())
	}
	logs, _ := app.Store.GetLogs(10, 0)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	l := logs[0]
	if l.PromptTokens != 5 {
		t.Errorf("prompt_tokens: got %d, want 5", l.PromptTokens)
	}
	if l.CompletionTokens != 2 {
		t.Errorf("completion_tokens: got %d, want 2", l.CompletionTokens)
	}
	if l.RealCostUSD <= 0 {
		t.Errorf("real cost should be positive: %f", l.RealCostUSD)
	}
}
