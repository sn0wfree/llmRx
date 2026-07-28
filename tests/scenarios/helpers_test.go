package scenarios

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// helper: send a chat completion request and return status code + body.
func doChat(t *testing.T, app *testhelper.App, token, model string, messages ...map[string]string) (int, map[string]interface{}) {
	t.Helper()
	body := map[string]interface{}{
		"model":    model,
		"messages": messages,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	return w.Code, parseJSON(t, w.Body.Bytes())
}

// helper: send a streaming chat completion request, collect all SSE data lines.
func doChatStream(t *testing.T, app *testhelper.App, token, model string, messages ...map[string]string) (int, []string) {
	t.Helper()
	body := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	var lines []string
	for _, line := range bytes.Split(w.Body.Bytes(), []byte("\n")) {
		s := string(line)
		if len(s) > 6 && s[:6] == "data: " {
			lines = append(lines, s[6:])
		}
	}
	return w.Code, lines
}

// helper: send an embeddings request.
func doEmbeddings(t *testing.T, app *testhelper.App, token, model, input string) (int, map[string]interface{}) {
	t.Helper()
	body := map[string]interface{}{
		"model": model,
		"input": input,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	return w.Code, parseJSON(t, w.Body.Bytes())
}

// helper: send a request with no auth.
func doChatNoAuth(t *testing.T, app *testhelper.App, model string) (int, map[string]interface{}) {
	t.Helper()
	return doChat(t, app, "", model, map[string]string{"role": "user", "content": "hi"})
}

func parseJSON(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	if len(b) == 0 {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parseJSON: %v (body=%s)", err, string(b))
	}
	return m
}

func errorMsg(m map[string]interface{}) string {
	if m == nil {
		return ""
	}
	if e, ok := m["error"].(map[string]interface{}); ok {
		if msg, ok := e["message"].(string); ok {
			return msg
		}
	}
	return ""
}

func errorCode(m map[string]interface{}) string {
	if m == nil {
		return ""
	}
	if e, ok := m["error"].(map[string]interface{}); ok {
		if code, ok := e["code"].(string); ok {
			return code
		}
	}
	return ""
}

func userMsg(content string) map[string]string {
	return map[string]string{"role": "user", "content": content}
}

func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}
