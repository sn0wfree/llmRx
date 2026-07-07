package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- OpenAI / passthrough fidelity ----------

func TestOpenAIChat_ForwardsAllFields(t *testing.T) {
	var captured []byte
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	temp := 0.3
	topp := 0.9
	n := 1
	seed := 42
	respFormat := &ResponseFormat{Type: "json_object"}
	req := &ChatRequest{
		Model:               "gpt-4",
		Messages:            []Message{{Role: "system", Content: "You are helpful"}, {Role: "user", Content: "hi"}},
		Stream:              false,
		Temperature:         &temp,
		TopP:                &topp,
		MaxTokens:           256,
		MaxCompletionTokens: 1024,
		N:                   &n,
		Seed:                &seed,
		ResponseFormat:      respFormat,
		Tools: []Tool{
			{Type: "function", Function: FunctionSpec{
				Name:        "get_weather",
				Description: "Look up the weather",
				Parameters:  map[string]any{"type": "object"},
			}},
		},
		ToolChoice: "auto",
		Stop:       []string{"STOP"},
		User:       "u-123",
		Metadata:   map[string]any{"trace": "abc"},
	}
	p := NewOpenAIProvider()
	resp, code, err := p.Chat(req, "sk-test", srv.URL)
	if err != nil || code != 200 {
		t.Fatalf("chat err=%v code=%d", err, code)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	if path != "/chat/completions" {
		t.Fatalf("path: %s", path)
	}

	// Verify every forwarded field reached the wire.
	var got map[string]any
	if err := json.Unmarshal(captured, &got); err != nil {
		t.Fatalf("upstream body: %s", captured)
	}
	if got["model"] != "gpt-4" {
		t.Errorf("model not forwarded: %v", got["model"])
	}
	if got["temperature"].(float64) != 0.3 {
		t.Errorf("temperature not forwarded: %v", got["temperature"])
	}
	if got["top_p"].(float64) != 0.9 {
		t.Errorf("top_p not forwarded: %v", got["top_p"])
	}
	if got["max_tokens"].(float64) != 256 {
		t.Errorf("max_tokens not forwarded: %v", got["max_tokens"])
	}
	if got["max_completion_tokens"].(float64) != 1024 {
		t.Errorf("max_completion_tokens not forwarded: %v", got["max_completion_tokens"])
	}
	if got["seed"].(float64) != 42 {
		t.Errorf("seed not forwarded: %v", got["seed"])
	}
	if got["user"] != "u-123" {
		t.Errorf("user not forwarded: %v", got["user"])
	}
	if _, ok := got["stop"]; !ok {
		t.Errorf("stop not forwarded")
	}
	if _, ok := got["response_format"]; !ok {
		t.Errorf("response_format not forwarded")
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools not forwarded: %v", got["tools"])
	}
	if got["tool_choice"] != "auto" {
		t.Errorf("tool_choice not forwarded: %v", got["tool_choice"])
	}
	meta, ok := got["metadata"].(map[string]any)
	if !ok || meta["trace"] != "abc" {
		t.Errorf("metadata not forwarded: %v", got["metadata"])
	}
}

func TestOpenAIChat_OmitsZeroFields(t *testing.T) {
	// A request with no optional fields must NOT emit those fields
	// (we use omitempty everywhere), so an upstream with strict
	// type checking can't complain about an unexpected "tools": null.
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"m","choices":[],"usage":{}}`)
	}))
	defer srv.Close()
	_, _, err := NewOpenAIProvider().Chat(&ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(captured, &got); err != nil {
		t.Fatalf("body: %s", captured)
	}
	for _, banned := range []string{
		"temperature", "top_p", "max_tokens", "max_completion_tokens",
		"tools", "tool_choice", "response_format", "stream_options",
		"metadata", "logit_bias", "parallel_tool_calls", "frequency_penalty",
		"presence_penalty", "stop", "user", "seed", "store", "reasoning_effort",
		"prompt_cache_key",
	} {
		if _, ok := got[banned]; ok {
			t.Errorf("zero-valued %s leaked into body: %v", banned, got[banned])
		}
	}
}

func TestOpenAIChat_MultimodalContentPassesThrough(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"m","choices":[],"usage":{}}`)
	}))
	defer srv.Close()
	parts := []ContentPart{
		{Type: "text", Text: "what's in this image?"},
		{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/x.png", Detail: "high"}},
	}
	_, _, err := NewOpenAIProvider().Chat(&ChatRequest{
		Model:    "gpt-4-vision",
		Messages: []Message{{Role: "user", Content: parts}},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(string(captured), "image_url") {
		t.Fatalf("image_url part not forwarded: %s", captured)
	}
	if !strings.Contains(string(captured), "what's in this image?") {
		t.Fatalf("text part not forwarded: %s", captured)
	}
}

func TestMessage_ContentString(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"nil", Message{}, ""},
		{"string", Message{Content: "hi"}, "hi"},
		{"parts-typed", Message{Content: []ContentPart{{Type: "text", Text: "abc"}, {Type: "text", Text: "def"}}}, "abcdef"},
		{"parts-map", Message{Content: []any{
			map[string]any{"type": "text", "text": "abc"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
			map[string]any{"type": "text", "text": "ghi"},
		}}, "abcghi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.msg.ContentString(); got != tc.want {
				t.Errorf("ContentString = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenAIStream_PassesIncludeUsage(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAIProvider()
	_, err := p.StreamChat(context.Background(), &ChatRequest{
		Model:        "m",
		Messages:     []Message{{Role: "user", Content: "hi"}},
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(captured, &got)
	so, ok := got["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("stream_options.include_usage missing: %v", got["stream_options"])
	}
}

// ---------- Anthropic ----------

func TestAnthropic_TranslateReq_ToolsAndParams(t *testing.T) {
	temp := 0.4
	topp := 0.95
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens:   512,
		Temperature: &temp,
		TopP:        &topp,
		Stop:        []string{"END"},
		Tools: []Tool{{
			Type: "function",
			Function: FunctionSpec{
				Name:        "search",
				Description: "web search",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		ToolChoice: "auto",
		Metadata:   map[string]any{"trace": "t1"},
	}
	out := p.translateReq(in)
	if out.Model != "claude-3" {
		t.Errorf("model: %s", out.Model)
	}
	if out.System != "be terse" {
		t.Errorf("system: %q", out.System)
	}
	if out.MaxTokens != 512 {
		t.Errorf("max_tokens: %d", out.MaxTokens)
	}
	if out.Temperature == nil || *out.Temperature != 0.4 {
		t.Errorf("temperature: %v", out.Temperature)
	}
	if out.TopP == nil || *out.TopP != 0.95 {
		t.Errorf("top_p: %v", out.TopP)
	}
	if _, ok := out.StopSeq.([]string); !ok {
		t.Errorf("stop_sequences: %v", out.StopSeq)
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "search" {
		t.Errorf("tools: %+v", out.Tools)
	}
	if out.ToolChoice == nil {
		t.Errorf("tool_choice missing")
	}
	if out.Metadata["trace"] != "t1" {
		t.Errorf("metadata: %v", out.Metadata)
	}
}

func TestAnthropic_TranslateReq_MultimodalContentString(t *testing.T) {
	// Anthropic doesn't support multimodal arrays, but our gateway
	// must extract text gracefully rather than crashing.
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{{Role: "user", Content: []ContentPart{
			{Type: "text", Text: "hello "},
			{Type: "image_url", ImageURL: &ImageURL{URL: "x"}},
			{Type: "text", Text: "world"},
		}}},
		MaxTokens: 16,
	}
	out := p.translateReq(in)
	if len(out.Messages) != 1 || out.Messages[0].Content != "hello world" {
		t.Errorf("anthropic content merge: %+v", out.Messages)
	}
}

func TestAnthropic_TranslateReq_CacheControlOnSystem(t *testing.T) {
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant.", CacheControl: &CacheCtl{Type: "ephemeral"}},
			{Role: "user", Content: "hi"},
		},
		MaxTokens: 32,
	}
	out := p.translateReq(in)
	// System must be emitted as an array of blocks when cache_control
	// is set; otherwise Anthropic rejects the request.
	blocks, ok := out.System.([]anthropicSystemBlock)
	if !ok {
		t.Fatalf("system should be array form, got %T %+v", out.System, out.System)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(blocks))
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control missing or wrong: %+v", blocks[0].CacheControl)
	}
	if blocks[0].Text != "You are a helpful assistant." {
		t.Errorf("system text: %q", blocks[0].Text)
	}
}

func TestAnthropic_TranslateReq_CacheControlOnMessage(t *testing.T) {
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "user", Content: "long-context prefix", CacheControl: &CacheCtl{Type: "ephemeral"}},
		},
		MaxTokens: 32,
	}
	out := p.translateReq(in)
	if len(out.Messages) != 1 {
		t.Fatalf("messages: %d", len(out.Messages))
	}
	parts, ok := out.Messages[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("content should be array form, got %T %+v", out.Messages[0].Content, out.Messages[0].Content)
	}
	if len(parts) != 1 || parts[0].Text != "long-context prefix" {
		t.Fatalf("content parts: %+v", parts)
	}
	if parts[0].CacheControl == nil || parts[0].CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control missing on content block")
	}
}

func TestAnthropic_TranslateReq_NoCacheControlUsesStringForm(t *testing.T) {
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "system", Content: "plain system"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens: 32,
	}
	out := p.translateReq(in)
	if s, ok := out.System.(string); !ok || s != "plain system" {
		t.Errorf("plain system should stay string, got %T %+v", out.System, out.System)
	}
	if s, ok := out.Messages[0].Content.(string); !ok || s != "hi" {
		t.Errorf("plain content should stay string, got %T %+v", out.Messages[0].Content, out.Messages[0].Content)
	}
}

func TestAnthropic_TranslateReq_CacheControlWiredJSON(t *testing.T) {
	// Round-trip through json.Marshal to confirm the wire format
	// matches Anthropic's spec.
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "system", Content: "you are terse", CacheControl: &CacheCtl{Type: "5m"}},
			{Role: "user", Content: "summarize"},
		},
		MaxTokens: 16,
	}
	out := p.translateReq(in)
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	// system should be an array
	sys, ok := got["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system should be array, got %T %+v", got["system"], got["system"])
	}
	blk := sys[0].(map[string]any)
	if blk["type"] != "text" || blk["text"] != "you are terse" {
		t.Errorf("block: %+v", blk)
	}
	cc, _ := blk["cache_control"].(map[string]any)
	if cc == nil || cc["type"] != "5m" {
		t.Errorf("cache_control: %+v", blk["cache_control"])
	}
}

// ---------- Gemini ----------

func TestGemini_TranslateReq_FullGenerationConfig(t *testing.T) {
	temp := 0.2
	topp := 0.8
	n := 2
	p := NewGeminiProvider()
	in := &ChatRequest{
		Model: "gemini-pro",
		Messages: []Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens:    64,
		Temperature:  &temp,
		TopP:         &topp,
		N:            &n,
		Stop:         "END",
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Tools: []Tool{{Function: FunctionSpec{
			Name:        "search",
			Description: "web search",
			Parameters:  map[string]any{"type": "object"},
		}}},
		ToolChoice: "auto",
	}
	out := p.translateReq(in)
	if out.SystemInstruction == nil || out.SystemInstruction.Text != "be terse" {
		t.Errorf("system: %+v", out.SystemInstruction)
	}
	if len(out.Contents) != 1 || out.Contents[0].Role != "user" {
		t.Errorf("contents: %+v", out.Contents)
	}
	if out.GenerationConfig == nil {
		t.Fatal("generationConfig missing")
	}
	gc := out.GenerationConfig
	if gc.MaxOutputTokens != 64 {
		t.Errorf("maxOutputTokens: %d", gc.MaxOutputTokens)
	}
	if gc.Temperature == nil || *gc.Temperature != 0.2 {
		t.Errorf("temperature: %v", gc.Temperature)
	}
	if gc.TopP == nil || *gc.TopP != 0.8 {
		t.Errorf("topP: %v", gc.TopP)
	}
	if gc.CandidateCount == nil || *gc.CandidateCount != 2 {
		t.Errorf("candidateCount: %v", gc.CandidateCount)
	}
	if len(gc.StopSequences) != 1 || gc.StopSequences[0] != "END" {
		t.Errorf("stopSequences: %v", gc.StopSequences)
	}
	if gc.ResponseMimeType != "application/json" {
		t.Errorf("responseMimeType: %s", gc.ResponseMimeType)
	}
	if len(out.Tools) != 1 {
		t.Errorf("tools: %+v", out.Tools)
	}
	if out.Tools[0].FunctionDeclarations[0].Name != "search" {
		t.Errorf("functionDecl name: %+v", out.Tools[0])
	}
	if out.ToolConfig == nil || out.ToolConfig.FunctionCallingConfig == nil {
		t.Fatal("toolConfig missing")
	}
	if out.ToolConfig.FunctionCallingConfig.Mode != "AUTO" {
		t.Errorf("mode: %s", out.ToolConfig.FunctionCallingConfig.Mode)
	}
}

func TestGemini_TranslateReq_ToolChoiceRequiredBecomesAny(t *testing.T) {
	p := NewGeminiProvider()
	in := &ChatRequest{
		Model:    "gemini-pro",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []Tool{{Function: FunctionSpec{Name: "f"}}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "f"}},
	}
	out := p.translateReq(in)
	if out.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Errorf("expected ANY, got %q", out.ToolConfig.FunctionCallingConfig.Mode)
	}
}

func TestGemini_TranslateReq_JsonSchemaPassesThrough(t *testing.T) {
	p := NewGeminiProvider()
	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	in := &ChatRequest{
		Model:    "gemini-pro",
		Messages: []Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &ResponseFormat{Type: "json_schema", JSONSchema: &JSONSchemaCfg{Name: "out", Schema: schema}},
	}
	out := p.translateReq(in)
	if out.GenerationConfig.ResponseSchema == nil {
		t.Fatal("responseSchema missing")
	}
	got, _ := out.GenerationConfig.ResponseSchema["type"].(string)
	if got != "object" {
		t.Errorf("schema not propagated: %v", out.GenerationConfig.ResponseSchema)
	}
}