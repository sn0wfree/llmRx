package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestRerank_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"rerank-v1"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"model":"rerank-v1","query":"hello","documents":["doc1","doc2"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp provider.RerankResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
}

func TestRerank_InvalidBody(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader("bad"))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRerank_MissingModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"query":"x","documents":["a"]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing_model") {
		t.Fatalf("expected missing_model, got %s", rec.Body.String())
	}
}

func TestRerank_MissingQuery(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"model":"m","documents":["a"]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing_query") {
		t.Fatalf("expected missing_query, got %s", rec.Body.String())
	}
}

func TestRerank_MissingDocuments(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"model":"m","query":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing_documents") {
		t.Fatalf("expected missing_documents, got %s", rec.Body.String())
	}
}

func TestRerank_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"known"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"model":"unknown","query":"x","documents":["a"]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestRerank_ModelWhitelist(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"allowed"}, "sk-key")
	tok := app.AddToken("sk-t", "t")
	tok.ModelsWhitelist = []string{"allowed"}
	app.Store.UpdateToken(tok)
	app.Cache.Reload()

	req := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"model":"forbidden","query":"x","documents":["a"]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
