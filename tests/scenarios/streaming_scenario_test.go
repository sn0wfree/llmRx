package scenarios

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestScenario_StreamingChat(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "1", Model: "gpt-4o", Choices: []provider.StreamChoice{
			{Index: 0, Delta: provider.Message{Role: "assistant", Content: "Hel"}},
		}},
		{ID: "1", Model: "gpt-4o", Choices: []provider.StreamChoice{
			{Index: 0, Delta: provider.Message{Content: "lo!"}},
		}},
	}

	code, lines := doChatStream(t, app, "sk-tok-1", "gpt-4o", userMsg("hi"))
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if len(lines) < 2 {
		t.Fatalf("expected >=2 data lines, got %d", len(lines))
	}
}
