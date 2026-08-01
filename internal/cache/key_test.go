package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
)

func TestKey_Deterministic(t *testing.T) {
	req := &provider.ChatRequest{
		Model: "gpt-4",
		Messages: []provider.Message{
			{Role: "user", Content: "hello"},
		},
	}
	k1, err := Key(req)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	k2, err := Key(req)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if k1 != k2 {
		t.Fatalf("same input produced different keys: %s vs %s", k1, k2)
	}
}

func TestKey_DifferentModels(t *testing.T) {
	req1 := &provider.ChatRequest{Model: "gpt-4", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	req2 := &provider.ChatRequest{Model: "gpt-4o", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	k1, _ := Key(req1)
	k2, _ := Key(req2)
	if k1 == k2 {
		t.Fatal("different models should produce different keys")
	}
}

func TestKey_TemperatureAboveZero(t *testing.T) {
	temperature := 0.7
	req := &provider.ChatRequest{
		Model:       "gpt-4",
		Messages:    []provider.Message{{Role: "user", Content: "hi"}},
		Temperature: &temperature,
	}
	_, err := Key(req)
	if err != ErrTemperaturePositive {
		t.Fatalf("expected ErrTemperaturePositive, got %v", err)
	}
}

func TestKey_TemperatureZero(t *testing.T) {
	temperature := 0.0
	req := &provider.ChatRequest{
		Model:       "gpt-4",
		Messages:    []provider.Message{{Role: "user", Content: "hi"}},
		Temperature: &temperature,
	}
	_, err := Key(req)
	if err != nil {
		t.Fatalf("expected no error for temperature=0, got %v", err)
	}
}

func TestKey_ToolOrdering(t *testing.T) {
	req1 := &provider.ChatRequest{
		Model: "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Tools: []provider.Tool{
			{Type: "function", Function: provider.FunctionSpec{Name: "b", Description: "second"}},
			{Type: "function", Function: provider.FunctionSpec{Name: "a", Description: "first"}},
		},
	}
	req2 := &provider.ChatRequest{
		Model: "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Tools: []provider.Tool{
			{Type: "function", Function: provider.FunctionSpec{Name: "a", Description: "first"}},
			{Type: "function", Function: provider.FunctionSpec{Name: "b", Description: "second"}},
		},
	}
	k1, _ := Key(req1)
	k2, _ := Key(req2)
	if k1 != k2 {
		t.Fatal("tool ordering should not affect key (json.Marshal sorts map keys)")
	}
}

func TestKey_ResponseFormat(t *testing.T) {
	req1 := &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}
	req2 := &provider.ChatRequest{
		Model:          "gpt-4",
		Messages:       []provider.Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &provider.ResponseFormat{Type: "json_object"},
	}
	k1, _ := Key(req1)
	k2, _ := Key(req2)
	if k1 == k2 {
		t.Fatal("response format should affect key")
	}
}

func TestKey_StreamOptions(t *testing.T) {
	req1 := &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}
	req2 := &provider.ChatRequest{
		Model:         "gpt-4",
		Messages:      []provider.Message{{Role: "user", Content: "hi"}},
		StreamOptions: &provider.StreamOptions{IncludeUsage: true},
	}
	k1, _ := Key(req1)
	k2, _ := Key(req2)
	if k1 == k2 {
		t.Fatal("stream options should affect key")
	}
}

func TestKey_HashStructure(t *testing.T) {
	req := &provider.ChatRequest{
		Model: "gpt-4",
		Messages: []provider.Message{
			{Role: "system", Content: "you are a bot"},
			{Role: "user", Content: "hello"},
		},
	}
	k, err := Key(req)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}

	h := sha256.New()
	h.Write([]byte("gpt-4"))
	h.Write([]byte{0})
	h.Write(mustJSON(canonicalMessages(req.Messages)))
	h.Write([]byte{0})
	h.Write(mustJSON(sortTools(req.Tools)))
	h.Write([]byte{0})
	// ResponseFormat is nil, skip — but the trailing separator still writes.
	h.Write([]byte{0})
	// StreamOptions is nil, skip.
	h.Write([]byte{0})
	h.Write([]byte("non-stream"))
	expected := hex.EncodeToString(h.Sum(nil))
	if k != expected {
		t.Fatalf("key mismatch:\n  got:      %s\n  expected: %s", k, expected)
	}
}

func TestCanonicalMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "hello", ToolCalls: []provider.ToolCall{
			{ID: "call_2", Function: provider.FunctionCall{Name: "b"}},
			{ID: "call_1", Function: provider.FunctionCall{Name: "a"}},
		}},
	}
	out := canonicalMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if len(out[0].ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(out[0].ToolCalls))
	}
	if out[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected first tool call id=call_1, got %s", out[0].ToolCalls[0].ID)
	}
}

func TestMustJSON(t *testing.T) {
	b := mustJSON(map[string]string{"b": "2", "a": "1"})
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("unexpected result: %v", m)
	}
}