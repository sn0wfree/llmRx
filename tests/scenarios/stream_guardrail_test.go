package scenarios

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestScenario_StreamOutputGuardrail(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	cfg, _ := json.Marshal(map[string]interface{}{
		"words": []string{"leaked_key"},
	})
	app.AddGuardrailRule("block-leak", model.GuardrailBlockedWords,
		model.GuardrailHookOutput, string(cfg))

	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "1", Model: "gpt-4o", Choices: []provider.StreamChoice{
			{Index: 0, Delta: provider.Message{Role: "assistant", Content: "here is leaked_key"}},
		}},
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{userMsg("show key")},
		"stream":   true,
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	raw := w.Body.String()
	if strings.Contains(raw, "[DONE]") {
		t.Fatal("should not have [DONE] when output guardrail blocks")
	}
	if !strings.Contains(raw, "event: error") {
		t.Fatal("should have error event frame when output guardrail blocks")
	}
	if !strings.Contains(raw, "block-leak") {
		t.Fatal("error frame should mention blocked rule name")
	}
}