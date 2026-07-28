package scenarios

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// Scenario: serial combo model fails over to second model.
func TestScenario_ComboSerialFailover(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddChannel("ch2", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o-mini"}, "sk-key-2")
	app.AddToken("sk-tok-1", "test-token")
	app.AddComboModel("sk-tok-1", "smart-1",
		[]string{"gpt-4o", "gpt-4o-mini"}, model.ComboModeSerial)

	app.Provider.Errs = []error{
		providerError("upstream 503: service unavailable"),
		nil,
	}
	app.Provider.Responses = []*provider.ChatResponse{
		nil,
		{
			ID: "chatcmpl-2", Model: "gpt-4o-mini",
			Choices: []provider.Choice{
				{Index: 0, Message: provider.Message{Role: "assistant", Content: "Fallback!"},
					FinishReason: "stop"},
			},
			Usage: provider.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		},
	}

	code, body := doChat(t, app, "sk-tok-1", "smart-1", userMsg("hi"))
	if code != 200 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	if app.Provider.Calls != 2 {
		t.Fatalf("provider calls=%d, want 2 (failover)", app.Provider.Calls)
	}
}

// Scenario: load_balance combo routes to an available channel.
func TestScenario_ComboLoadBalance(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddChannel("ch2", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o-mini"}, "sk-key-2")
	app.AddToken("sk-tok-1", "test-token")
	app.AddComboModel("sk-tok-1", "lb-1",
		[]string{"gpt-4o", "gpt-4o-mini"}, model.ComboModeLoadBalance)

	code, _ := doChat(t, app, "sk-tok-1", "lb-1", userMsg("hi"))
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if app.Provider.Calls != 1 {
		t.Fatalf("provider calls=%d, want 1", app.Provider.Calls)
	}
}

// Scenario: upstream error returns appropriate status to client.
func TestScenario_UpstreamError(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	app.Provider.Errs = []error{providerError("upstream 500: internal error")}
	app.Provider.Statuses = []int{500}

	code, body := doChat(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
	if code != 500 {
		t.Fatalf("status=%d, want 500 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "upstream_error" {
		t.Fatalf("error code: got %s, want upstream_error", c)
	}
}

// Scenario: request with no matching channel returns 503.
func TestScenario_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-tok-1", "test-token")

	code, body := doChat(t, app, "sk-tok-1", "nonexistent-model", userMsg("hi"))
	if code != 503 {
		t.Fatalf("status=%d, want 503 (body=%v)", code, body)
	}
	if c := errorCode(body); c != "no_channel" {
		t.Fatalf("error code: got %s, want no_channel", c)
	}
}

type testError string

func (e testError) Error() string { return string(e) }

func providerError(msg string) error { return testError(msg) }
