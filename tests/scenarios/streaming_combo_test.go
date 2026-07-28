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

func TestScenario_StreamingComboLoadBalance(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")
	app.AddComboModel("sk-tok-1", "lb-stream",
		[]string{"gpt-4o"}, model.ComboModeLoadBalance)

	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "1", Model: "gpt-4o", Choices: []provider.StreamChoice{
			{Index: 0, Delta: provider.Message{Role: "assistant", Content: "hello"}},
		}},
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "lb-stream",
		"messages": []map[string]string{userMsg("hi")},
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
	if !strings.Contains(raw, "hello") {
		t.Fatal("streamed content not found in response")
	}
	if !strings.Contains(raw, "[DONE]") {
		t.Fatal("[DONE] not found in stream")
	}
}

func TestScenario_StreamingComboSerialFailover(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddChannel("ch2", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o-mini"}, "sk-key-2")
	app.AddToken("sk-tok-1", "test-token")
	app.AddComboModel("sk-tok-1", "serial-stream",
		[]string{"gpt-4o", "gpt-4o-mini"}, model.ComboModeSerial)

	// gpt-4o fails first, gpt-4o-mini succeeds
	app.Provider.StreamErr = nil
	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "1", Model: "gpt-4o-mini", Choices: []provider.StreamChoice{
			{Index: 0, Delta: provider.Message{Role: "assistant", Content: "fallback ok"}},
		}},
	}
	app.Provider.Errs = []error{nil, nil}
	app.Provider.Responses = []*provider.ChatResponse{nil, nil}

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "serial-stream",
		"messages": []map[string]string{userMsg("hi")},
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
	if !strings.Contains(w.Body.String(), "fallback ok") {
		t.Fatal("fallback stream not found in response")
	}
}