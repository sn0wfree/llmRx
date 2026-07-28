package scenarios

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestScenario_BasicChat(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	app.Provider.Responses = []*provider.ChatResponse{
		{
			ID: "chatcmpl-1", Model: "gpt-4o",
			Choices: []provider.Choice{
				{Index: 0, Message: provider.Message{Role: "assistant", Content: "Hello!"}, FinishReason: "stop"},
			},
			Usage: provider.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		},
	}

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
	if code != 200 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	if app.Provider.Calls != 1 {
		t.Fatalf("provider calls=%d, want 1", app.Provider.Calls)
	}
	if app.Provider.LastKey != "sk-key-1" {
		t.Fatalf("lastKey=%s, want sk-key-1", app.Provider.LastKey)
	}
}
