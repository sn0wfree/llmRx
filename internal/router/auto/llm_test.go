package auto

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// llmServer spins up an OpenAI-compatible stub that responds with
// the given content/status.
func llmServer(t *testing.T, status int, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			resp := map[string]any{
				"choices": []map[string]any{
					{"message": map[string]string{"content": content}},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
}

func TestLLMClassifier_ValidVerdict(t *testing.T) {
	srv := llmServer(t, http.StatusOK, `{"tier":"complex","score":0.71}`)
	defer srv.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: srv.URL, Model: "classifier-v1", Timeout: time.Second}, nil)
	got := c.Classify("some prompt")
	if got.Tier != TierComplex {
		t.Fatalf("tier = %v, want complex", got.Tier)
	}
	if got.Score != 0.71 {
		t.Fatalf("score = %v, want 0.71", got.Score)
	}
	if got.Cause != CauseLLM {
		t.Fatalf("cause = %q, want llm", got.Cause)
	}
}

func TestLLMClassifier_HTTPErrorFallsBack(t *testing.T) {
	srv := llmServer(t, http.StatusInternalServerError, "")
	defer srv.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: srv.URL, Model: "m"}, nil)
	got := c.Classify("hello")
	if got.Cause != CauseHeuristicFallback {
		t.Fatalf("cause = %q, want heuristic_fallback", got.Cause)
	}
	if got.Tier != TierSimple {
		t.Fatalf("tier = %v, want simple (heuristic verdict)", got.Tier)
	}
}

func TestLLMClassifier_TimeoutFallsBack(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: slow.URL, Model: "m", Timeout: 30 * time.Millisecond}, nil)
	start := time.Now()
	got := c.Classify("hello")
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("timeout not honored: took %v", time.Since(start))
	}
	if got.Cause != CauseHeuristicFallback {
		t.Fatalf("cause = %q, want heuristic_fallback on timeout", got.Cause)
	}
}

func TestLLMClassifier_InvalidTierFallsBack(t *testing.T) {
	srv := llmServer(t, http.StatusOK, `{"tier":"ultra","score":0.9}`)
	defer srv.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: srv.URL, Model: "m"}, nil)
	got := c.Classify("hello")
	if got.Cause != CauseHeuristicFallback {
		t.Fatalf("cause = %q, want heuristic_fallback for unknown tier", got.Cause)
	}
}

func TestLLMClassifier_UnparseableFallsBack(t *testing.T) {
	srv := llmServer(t, http.StatusOK, "not json at all")
	defer srv.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: srv.URL, Model: "m"}, nil)
	got := c.Classify("hello")
	if got.Cause != CauseHeuristicFallback {
		t.Fatalf("cause = %q, want heuristic_fallback for unparseable content", got.Cause)
	}
}

func TestLLMClassifier_EmptyChoicesFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: srv.URL, Model: "m"}, nil)
	got := c.Classify("hello")
	if got.Cause != CauseHeuristicFallback {
		t.Fatalf("cause = %q, want heuristic_fallback for empty choices", got.Cause)
	}
}

func TestLLMClassifier_EmptyInput(t *testing.T) {
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: "http://127.0.0.1:1"}, nil)
	got := c.Classify("   ")
	if got.Cause != CauseEmpty || got.Tier != TierSimple {
		t.Fatalf("empty input: %+v", got)
	}
}

func TestLLMClassifier_NetworkErrorFallsBack(t *testing.T) {
	// Port 1 never answers; the client timeout bounds the wait.
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: "http://127.0.0.1:1", Model: "m", Timeout: 100 * time.Millisecond}, nil)
	got := c.Classify(strings.Repeat("long prompt ", 50))
	if got.Cause != CauseHeuristicFallback {
		t.Fatalf("cause = %q, want heuristic_fallback on network error", got.Cause)
	}
}

func TestLLMClassifier_ScoreClamped(t *testing.T) {
	srv := llmServer(t, http.StatusOK, `{"tier":"agentic","score":7}`)
	defer srv.Close()
	c := NewLLMClassifier(LLMClassifierConfig{BaseURL: srv.URL, Model: "m"}, nil)
	got := c.Classify("x")
	if got.Score != 1 {
		t.Fatalf("score = %v, want clamped to 1", got.Score)
	}
}
