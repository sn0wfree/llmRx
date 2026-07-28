package scenarios

import (
	"encoding/json"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// Scenario: input guardrail blocks request with banned words.
func TestScenario_InputGuardrail(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	cfg, _ := json.Marshal(map[string]interface{}{
		"words": []string{"secret"},
	})
	app.AddGuardrailRule("block-secret", model.GuardrailBlockedWords,
		model.GuardrailHookInput, string(cfg))

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o",
		userMsg("my secret password"))
	if code != 422 {
		t.Fatalf("status=%d, want 422 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "guardrail_violated" {
		t.Fatalf("error code: got %s, want guardrail_violated", c)
	}
	if app.Provider.Calls != 0 {
		t.Fatalf("provider should not be called, got calls=%d", app.Provider.Calls)
	}
}

// Scenario: output guardrail blocks response with banned content.
func TestScenario_OutputGuardrail(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	cfg, _ := json.Marshal(map[string]interface{}{
		"words": []string{"api_key"},
	})
	app.AddGuardrailRule("block-leak", model.GuardrailBlockedWords,
		model.GuardrailHookOutput, string(cfg))

	app.Provider.Responses = []*provider.ChatResponse{
		{
			ID: "1", Model: "gpt-4o",
			Choices: []provider.Choice{
				{Index: 0, Message: provider.Message{
					Role: "assistant", Content: "here is your api_key: sk-xxx",
				}, FinishReason: "stop"},
			},
			Usage: provider.Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		},
	}

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("show key"))
	if code != 422 {
		t.Fatalf("status=%d, want 422 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "guardrail_violated" {
		t.Fatalf("error code: got %s, want guardrail_violated", c)
	}
}

// Scenario: input guardrail passes clean request.
func TestScenario_GuardrailPass(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	cfg, _ := json.Marshal(map[string]interface{}{
		"words": []string{"forbidden"},
	})
	app.AddGuardrailRule("block-word", model.GuardrailBlockedWords,
		model.GuardrailHookInput, string(cfg))

	code, _ := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hello world"))
	if code != 200 {
		t.Fatalf("clean request should pass, got %d", code)
	}
}

// Scenario: both-hook guardrail blocks on input and output.
func TestScenario_GuardrailBothHook(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	cfg, _ := json.Marshal(map[string]interface{}{
		"words": []string{"badword"},
	})
	app.AddGuardrailRule("block-both", model.GuardrailBlockedWords,
		model.GuardrailHookBoth, string(cfg))

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o",
		userMsg("this is badword"))
	if code != 422 {
		t.Fatalf("input check should block, got %d (body=%v)", code, body)
	}
}
