package scenarios

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/sn0wfree/llmRx/internal/requestid"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestScenario_RequestID_Generated: middleware assigns an ID when
// the client does not supply one, and echoes it back in the response
// header. testhelper.App does not currently mount the requestid
// middleware, so we wrap it here for the test only.
func TestScenario_RequestID_Generated(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{userMsg("hi")},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	got := w.Header().Get(requestid.HeaderName)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(got) {
		t.Fatalf("response header: got %q, want 16 hex chars", got)
	}
}

// TestScenario_RequestID_Reused: when the client supplies an ID,
// the middleware reuses it instead of generating a new one.
func TestScenario_RequestID_Reused(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{userMsg("hi")},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(requestid.HeaderName, "upstream-abc-123")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if got := w.Header().Get(requestid.HeaderName); got != "upstream-abc-123" {
		t.Fatalf("response header: got %q, want upstream-abc-123", got)
	}
}