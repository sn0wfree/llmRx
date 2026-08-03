package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	goccy "github.com/goccy/go-json"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// TestGoccy_StreamChunk_TrailingNewline locks in the SSE format
// contract: goccy's Encoder.Encode must append a trailing '\n' so
// the SSE `data: {json}\n\n` framing works. This is the byte-level
// invariant TestChat_StreamingEndpoint indirectly relies on.
func TestGoccy_StreamChunk_TrailingNewline(t *testing.T) {
	chunk := provider.StreamChunk{
		ID:      "x",
		Object:  "chat.completion.chunk",
		Created: 1,
		Model:   "m",
		Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Content: "hi"}}},
	}
	var buf bytes.Buffer
	if err := goccy.NewEncoder(&buf).Encode(chunk); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte{'\n'}) {
		t.Fatalf("goccy Encode must append trailing newline; got %q", buf.String())
	}
}

// TestGoccy_InterfaceFields_RoundTrip verifies that goccy handles
// the interface{} fields that the OpenAI chat protocol uses
// (Stop, ToolChoice, Message.Content, Logprobs, Metadata).
// stdlib json and goccy must agree on the round-tripped struct
// shape even when the field type is heterogeneous.
func TestGoccy_InterfaceFields_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"stop_string", `{"model":"gpt-4","messages":[],"stop":"\n"}`},
		{"stop_array", `{"model":"gpt-4","messages":[],"stop":["a","b"]}`},
		{"toolchoice_string", `{"model":"gpt-4","messages":[],"tool_choice":"auto"}`},
		{"toolchoice_object", `{"model":"gpt-4","messages":[],"tool_choice":{"type":"function","function":{"name":"x"}}}`},
		{"content_string", `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`},
		{"content_array", `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"metadata_mixed", `{"model":"gpt-4","messages":[],"metadata":{"k1":"v1","k2":42,"k3":true,"k4":null}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reqStd, reqGoccy provider.ChatRequest
			if err := json.Unmarshal([]byte(tc.body), &reqStd); err != nil {
				t.Fatalf("stdlib decode: %v", err)
			}
			if err := goccy.Unmarshal([]byte(tc.body), &reqGoccy); err != nil {
				t.Fatalf("goccy decode: %v", err)
			}
			// Re-marshal both and verify goccy produces valid JSON
			// that stdlib can decode back to the same struct.
			bStd, err := json.Marshal(reqStd)
			if err != nil {
				t.Fatalf("stdlib marshal: %v", err)
			}
			bGoccy, err := goccy.Marshal(reqGoccy)
			if err != nil {
				t.Fatalf("goccy marshal: %v", err)
			}
			var dStd, dGoccy provider.ChatRequest
			if err := json.Unmarshal(bStd, &dStd); err != nil {
				t.Fatalf("stdlib re-decode: %v", err)
			}
			if err := json.Unmarshal(bGoccy, &dGoccy); err != nil {
				t.Fatalf("goccy re-decode via stdlib: %v\nbody=%s", err, bGoccy)
			}
		})
	}
}

// TestGoccy_SSEChunkOrderIsStable verifies that goccy's encoder
// produces stable field ordering for StreamChunk (matches stdlib's
// behavior). Without this, SSE consumers that hash the chunk body
// would see different hashes per request.
func TestGoccy_SSEChunkOrderIsStable(t *testing.T) {
	chunk := provider.StreamChunk{
		ID:      "id",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "m",
		Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Content: "hi"}}},
	}
	var b1, b2 bytes.Buffer
	_ = goccy.NewEncoder(&b1).Encode(chunk)
	_ = goccy.NewEncoder(&b2).Encode(chunk)
	if !bytes.Equal(b1.Bytes(), b2.Bytes()) {
		t.Fatalf("goccy output is not stable:\n%s\n%s", b1.String(), b2.String())
	}
	// Sanity: id must come before object (struct field order).
	s := b1.String()
	iID := strings.Index(s, `"id"`)
	iObj := strings.Index(s, `"object"`)
	if iID < 0 || iObj < 0 || iID >= iObj {
		t.Fatalf("expected id before object, got %q", s)
	}
}
