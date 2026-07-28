package scenarios

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// Scenario: basic embeddings request returns vectors.
func TestScenario_BasicEmbeddings(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"text-embedding-3-small"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	app.Provider.EmbResponses = []*provider.EmbeddingsResponse{
		{
			Object: "list",
			Data: []provider.Embedding{
				{Object: "embedding", Embedding: []float64{0.1, 0.2, 0.3}, Index: 0},
			},
			Model: "text-embedding-3-small",
			Usage: provider.Usage{PromptTokens: 3, TotalTokens: 3},
		},
	}

	code, body := doEmbeddings(t, app, "sk-tok-1", "text-embedding-3-small", "hello")
	if code != 200 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	if app.Provider.EmbCalls != 1 {
		t.Fatalf("emb calls=%d, want 1", app.Provider.EmbCalls)
	}
}

// Scenario: embeddings with disallowed model returns 403.
func TestScenario_EmbeddingsModelWhitelist(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"text-embedding-3-small", "text-embedding-3-large"}, "sk-key-1")
	app.AddTokenWithLimits("sk-tok-1", "restricted", 0, 0,
		[]string{"text-embedding-3-small"})

	code, _ := doEmbeddings(t, app, "sk-tok-1", "text-embedding-3-small", "hello")
	if code != 200 {
		t.Fatalf("allowed model: got %d, want 200", code)
	}

	code, body := doEmbeddings(t, app, "sk-tok-1", "text-embedding-3-large", "hello")
	if code != 403 {
		t.Fatalf("disallowed model: got %d, want 403 (body=%v)", code, body)
	}
}

// Scenario: embeddings with no auth returns 401.
func TestScenario_EmbeddingsNoAuth(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"text-embedding-3-small"}, "sk-key-1")

	body := map[string]interface{}{"model": "text-embedding-3-small", "input": "hello"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/embeddings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("no auth: got %d, want 401", w.Code)
	}
}
