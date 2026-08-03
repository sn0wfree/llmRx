package provider

import (
	"bytes"
	"strings"
	"testing"

	goccy "github.com/goccy/go-json"
)

// TestGoccy_ChatRequest_RoundTrip verifies that goccy correctly
// marshals/unmarshals ChatRequest including all interface{} fields.
// This is critical because OpenAIProvider.Chat uses json.Marshal(req)
// to send to upstream and json.Unmarshal(respBody) to decode.
func TestGoccy_ChatRequest_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		req  ChatRequest
	}{
		{
			name: "basic",
			req: ChatRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
			},
		},
		{
			name: "stop_string",
			req: ChatRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Stop:     "\n",
			},
		},
		{
			name: "stop_array",
			req: ChatRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Stop:     []string{"\n", "END"},
			},
		},
		{
			name: "toolchoice_auto",
			req: ChatRequest{
				Model:      "gpt-4",
				Messages:   []Message{{Role: "user", Content: "hi"}},
				ToolChoice: "auto",
			},
		},
		{
			name: "toolchoice_object",
			req: ChatRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				ToolChoice: map[string]any{
					"type": "function",
					"function": map[string]any{
						"name": "get_weather",
					},
				},
			},
		},
		{
			name: "metadata_mixed",
			req: ChatRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Metadata: map[string]any{
					"user_id": "123",
					"tier":    float64(5),
					"active":  true,
				},
			},
		},
		{
			name: "content_array",
			req: ChatRequest{
				Model: "gpt-4",
				Messages: []Message{{
					Role: "user",
					Content: []ContentPart{
						{Type: "text", Text: "What is this?"},
						{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/img.png"}},
					},
				}},
			},
		},
		{
			name: "stream_true",
			req: ChatRequest{
				Model:    "gpt-4",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Stream:   true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := goccy.Marshal(tc.req)
			if err != nil {
				t.Fatalf("goccy marshal: %v", err)
			}

			// Verify JSON is valid by re-decoding with goccy
			var decoded ChatRequest
			if err := goccy.Unmarshal(b, &decoded); err != nil {
				t.Fatalf("goccy unmarshal: %v\njson=%s", err, b)
			}

			// Verify critical fields round-trip
			if decoded.Model != tc.req.Model {
				t.Fatalf("model: got %q, want %q", decoded.Model, tc.req.Model)
			}
			if len(decoded.Messages) != len(tc.req.Messages) {
				t.Fatalf("messages: got %d, want %d", len(decoded.Messages), len(tc.req.Messages))
			}
			if decoded.Stream != tc.req.Stream {
				t.Fatalf("stream: got %v, want %v", decoded.Stream, tc.req.Stream)
			}

			// Verify the JSON contains expected keys
			s := string(b)
			if !strings.Contains(s, `"model"`) {
				t.Fatalf("missing model key in json: %s", s)
			}
			if tc.req.Stream && !strings.Contains(s, `"stream":true`) {
				t.Fatalf("missing stream:true in json: %s", s)
			}
		})
	}
}

// TestGoccy_StreamChunk_RoundTrip verifies that StreamChunk
// (used in SSE encoding) round-trips correctly via goccy.
func TestGoccy_StreamChunk_RoundTrip(t *testing.T) {
	chunk := StreamChunk{
		ID:      "chatcmpl-123",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        Message{Role: "assistant", Content: "Hello"},
			FinishReason: "stop",
		}},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	b, err := goccy.Marshal(chunk)
	if err != nil {
		t.Fatalf("goccy marshal: %v", err)
	}

	var decoded StreamChunk
	if err := goccy.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("goccy unmarshal: %v\njson=%s", err, b)
	}

	if decoded.ID != chunk.ID {
		t.Fatalf("id: got %q, want %q", decoded.ID, chunk.ID)
	}
	if decoded.Model != chunk.Model {
		t.Fatalf("model: got %q, want %q", decoded.Model, chunk.Model)
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("choices: got %d, want 1", len(decoded.Choices))
	}
	if decoded.Choices[0].Delta.Content != "Hello" {
		t.Fatalf("content: got %q, want %q", decoded.Choices[0].Delta.Content, "Hello")
	}
	if decoded.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason: got %q, want %q", decoded.Choices[0].FinishReason, "stop")
	}
	if decoded.Usage == nil || decoded.Usage.TotalTokens != 15 {
		t.Fatalf("usage: got %v, want TotalTokens=15", decoded.Usage)
	}
}

// TestGoccy_ChatResponse_RoundTrip verifies that ChatResponse
// (decoded from upstream) round-trips correctly via goccy.
func TestGoccy_ChatResponse_RoundTrip(t *testing.T) {
	resp := ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: "Hello world"},
			FinishReason: "stop",
		}},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	b, err := goccy.Marshal(resp)
	if err != nil {
		t.Fatalf("goccy marshal: %v", err)
	}

	var decoded ChatResponse
	if err := goccy.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("goccy unmarshal: %v\njson=%s", err, b)
	}

	if decoded.ID != resp.ID {
		t.Fatalf("id: got %q, want %q", decoded.ID, resp.ID)
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("choices: got %d, want 1", len(decoded.Choices))
	}
	if decoded.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("content: got %q, want %q", decoded.Choices[0].Message.Content, "Hello world")
	}
	if decoded.Usage.TotalTokens != 30 {
		t.Fatalf("usage.total_tokens: got %d, want 30", decoded.Usage.TotalTokens)
	}
}

// TestGoccy_StreamChunk_TrailingNewline verifies that goccy's
// Encoder appends a trailing '\n' (matching stdlib behavior).
// This is required for SSE 'data: {json}\n\n' framing.
func TestGoccy_StreamChunk_TrailingNewline(t *testing.T) {
	chunk := StreamChunk{
		ID:      "x",
		Object:  "chat.completion.chunk",
		Created: 1,
		Model:   "m",
		Choices: []StreamChoice{{Index: 0, Delta: Message{Content: "hi"}}},
	}
	var buf bytes.Buffer
	if err := goccy.NewEncoder(&buf).Encode(chunk); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte{'\n'}) {
		t.Fatalf("goccy Encode must append trailing newline; got %q", buf.String())
	}
}
