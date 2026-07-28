package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// ---- Embeddings ----

func TestEmbeddings_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"text-embed-3"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"model":"text-embed-3","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp provider.EmbeddingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Data))
	}
}

func TestEmbeddings_InvalidBody(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader("not json"))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_body") {
		t.Fatalf("expected invalid_body, got %s", rec.Body.String())
	}
}

func TestEmbeddings_MissingModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"input":"x"}`))
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

func TestEmbeddings_ModelWhitelistEnforced(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"allowed"}, "sk-key")
	tok := app.AddToken("sk-t", "t")
	tok.ModelsWhitelist = []string{"allowed"}
	app.Store.UpdateToken(tok)
	app.Cache.Reload()

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"forbidden","input":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model_not_allowed") {
		t.Fatalf("expected model_not_allowed, got %s", rec.Body.String())
	}
}

func TestEmbeddings_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"known"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"unknown","input":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestEmbeddings_UpstreamError(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.Provider.EmbErrs = []error{errors.New("upstream boom")}

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// ---- ImageGenerations ----

func TestImages_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"dall-e-3"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"model":"dall-e-3","prompt":"a cat"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp provider.ImagesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Data))
	}
}

func TestImages_DefaultModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"dall-e-3"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"prompt":"a cat"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestImages_MissingPrompt(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"dall-e-3"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"dall-e-3"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing_prompt") {
		t.Fatalf("expected missing_prompt, got %s", rec.Body.String())
	}
}

func TestImages_InvalidBody(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"dall-e-3"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("nope"))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestImages_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"dall-e-3"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"unknown-img","prompt":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestImages_ModelWhitelist(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"allowed"}, "sk-key")
	tok := app.AddToken("sk-t", "t")
	tok.ModelsWhitelist = []string{"allowed"}
	app.Store.UpdateToken(tok)
	app.Cache.Reload()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"forbidden","prompt":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
