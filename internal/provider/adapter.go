package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatRequest is the OpenAI-compatible request body. All fields are
// optional except Model and Messages. Anything the upstream supports
// (temperature, max_tokens, tools, tool_choice, response_format,
// stream_options, metadata, etc.) is forwarded verbatim by the
// OpenAIProvider because the JSON encoder uses the struct tags.
//
// AnthropicProvider and GeminiProvider translate these fields into
// the wire format of each protocol before sending.
type ChatRequest struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Stream              bool            `json:"stream,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`            // legacy, prefer MaxCompletionTokens for GPT-5+
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"` // newer field
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	Stop                any             `json:"stop,omitempty"`          // string or []string
	N                   *int            `json:"n,omitempty"`
	Seed                *int            `json:"seed,omitempty"`
	User                string          `json:"user,omitempty"`
	Logprobs            *bool           `json:"logprobs,omitempty"`
	TopLogprobs         *int            `json:"top_logprobs,omitempty"`
	LogitBias           map[string]int  `json:"logit_bias,omitempty"`
	ResponseFormat      *ResponseFormat `json:"response_format,omitempty"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"` // string or ToolChoice
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	Store               *bool           `json:"store,omitempty"`
	Metadata            map[string]any  `json:"metadata,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	PromptCacheKey      string          `json:"prompt_cache_key,omitempty"` // Anthropic hint; ignored by OpenAI
}

// Message mirrors the OpenAI Messages shape. Content may be a plain
// string (text-only) or an array of content parts (text / image_url).
// ToolCalls / ToolCallID carry the function-calling side of the
// conversation; the gateway forwards both fields as-is.
type Message struct {
	Role         string      `json:"role"`
	Content      any         `json:"content,omitempty"`
	Name         string      `json:"name,omitempty"`
	ToolCalls    []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID   string      `json:"tool_call_id,omitempty"`
	CacheControl *CacheCtl   `json:"cache_control,omitempty"` // Anthropic extension
	Refusal      string      `json:"refusal,omitempty"`
}

// ContentPart is one item in a multimodal message Content array.
// The provider implementations only care about "text" and
// "image_url"; other types pass through unchanged so the upstream
// can decide how to interpret them.
type ContentPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *ImageURL   `json:"image_url,omitempty"`
	CacheCtl *CacheCtl   `json:"cache_control,omitempty"`
}

// ImageURL describes a multimodal image attachment.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// CacheCtl is Anthropic's prompt-cache directive (5m | 1h | ephemeral).
// Other providers ignore it.
type CacheCtl struct {
	Type string `json:"type"`
}

// Tool is one function definition exposed to the model.
type Tool struct {
	Type     string       `json:"type,omitempty"` // "function"
	Function FunctionSpec `json:"function"`
}

// FunctionSpec describes a callable function.
type FunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

// ToolCall is a request from the model to invoke a function.
type ToolCall struct {
	Index    *int       `json:"index,omitempty"` // only present in streaming deltas
	ID       string     `json:"id,omitempty"`
	Type     string     `json:"type,omitempty"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall is the payload of a ToolCall.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // JSON-encoded string
}

// ResponseFormat is the "structured output" envelope. We model the
// three known shapes: text (default), json_object, and json_schema.
type ResponseFormat struct {
	Type       string         `json:"type"` // text | json_object | json_schema
	JSONSchema *JSONSchemaCfg `json:"json_schema,omitempty"`
}

// JSONSchemaCfg is the json_schema flavour of ResponseFormat.
type JSONSchemaCfg struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

// StreamOptions is the OpenAI switch to ask for usage on the final
// streaming chunk.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatResponse is the non-streaming completion result. The fields
// not handled by the choice are forwarded transparently to the
// client, so an upstream that sets SystemFingerprint or Logprobs
// is preserved.
type ChatResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	Choices           []Choice `json:"choices"`
	Usage             Usage    `json:"usage"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
}

// Choice is one of N completions. Logprobs may be nil (most models
// don't return them unless the client asked for them).
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Logprobs     any     `json:"logprobs,omitempty"`
}

// StreamChunk is one server-sent event from an OpenAI-compatible
// streaming chat completion. The object is always "chat.completion.chunk".
type StreamChunk struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []StreamChoice `json:"choices"`
	Usage             *Usage         `json:"usage,omitempty"`
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
}

// StreamChoice is one delta inside a stream chunk.
type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
	Logprobs     any     `json:"logprobs,omitempty"`
}

// Usage mirrors the OpenAI usage block. PromptTokensDetails and
// CompletionTokensDetails capture cached-token and reasoning info
// (Anthropic / OpenAI GPT-5+) which the cost calculator can later
// discount.
type Usage struct {
	PromptTokens            int                  `json:"prompt_tokens"`
	CompletionTokens        int                  `json:"completion_tokens"`
	TotalTokens             int                  `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails any                  `json:"completion_tokens_details,omitempty"`
}

// PromptTokensDetails reports cached-input breakdown.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// ---------- helpers ----------

// ContentString returns the message Content as a flat string. If the
// upstream supplied a multimodal Content array, only the text parts
// are concatenated; image parts are dropped (the upstream typically
// understands a clean text-only summary).
func (m Message) ContentString() string {
	switch v := m.Content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	}
	// array of parts
	parts, _ := m.Content.([]any)
	if parts == nil {
		// Try typed slice (decoded into []ContentPart).
		if cp, ok := m.Content.([]ContentPart); ok {
			parts = make([]any, len(cp))
			for i := range cp {
				parts[i] = cp[i]
			}
		}
	}
	var b strings.Builder
	for _, p := range parts {
		cp, ok := p.(ContentPart)
		if !ok {
			// json decoded into map[string]any instead of typed struct
			if mm, ok2 := p.(map[string]any); ok2 {
				if t, _ := mm["type"].(string); t == "text" {
					if txt, ok3 := mm["text"].(string); ok3 {
						b.WriteString(txt)
					}
				}
			}
			continue
		}
		if cp.Type == "text" {
			b.WriteString(cp.Text)
		}
	}
	return b.String()
}

// FloatPtr returns *f so request fields can be encoded only when set.
func FloatPtr(f float64) *float64 { return &f }
func IntPtr(i int) *int             { return &i }
func BoolPtr(b bool) *bool          { return &b }

// FloatOr returns *p or &def.
func FloatOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}
func IntOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// Provider is the contract for an upstream chat backend. The
// non-streaming Chat method is the primary path; Streaming is
// optional — implementations that don't support streaming return
// ErrStreamUnsupported and the API layer will fall back to
// non-streaming.
type Provider interface {
	Name() string
	Chat(req *ChatRequest, apiKey string, baseURL string) (*ChatResponse, int, error)
}

// StreamingProvider is an optional capability some providers
// implement to allow true token-by-token streaming via SSE.
type StreamingProvider interface {
	Provider
	StreamChat(ctx context.Context, req *ChatRequest, apiKey, baseURL string) (<-chan StreamEvent, error)
}

// StreamEvent is one delivery from a streaming provider. The chunk
// is the parsed OpenAI chunk; Err signals the end (non-nil = upstream
// failure or context cancel).
type StreamEvent struct {
	Chunk StreamChunk
	Err   error
}

type OpenAIProvider struct {
	client *http.Client
}

func NewOpenAIProvider() *OpenAIProvider {
	return &OpenAIProvider{
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OpenAIProvider) Name() string {
	return "openai-compatible"
}

func (p *OpenAIProvider) Chat(req *ChatRequest, apiKey string, baseURL string) (*ChatResponse, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal response: %w", err)
	}

	return &chatResp, resp.StatusCode, nil
}

// StreamChat implements StreamingProvider. It POSTs with stream=true,
// reads the SSE response line-by-line, and emits one StreamEvent per
// parsed chunk. The channel is closed when the upstream closes the
// stream or ctx is cancelled. Final usage (if the upstream emits it
// in the last chunk) is delivered on the closing event via Err==nil
// and the chunk's Usage field set.
func (p *OpenAIProvider) StreamChat(ctx context.Context, req *ChatRequest, apiKey, baseURL string) (<-chan StreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	out := make(chan StreamEvent, 8)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		var data strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				// Dispatch the accumulated data block.
				if data.Len() == 0 {
					continue
				}
				payload := strings.TrimSpace(data.String())
				data.Reset()
				if payload == "[DONE]" {
					return
				}
				var chunk StreamChunk
				if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
					// Skip malformed lines but keep going.
					continue
				}
				select {
				case <-ctx.Done():
					out <- StreamEvent{Err: ctx.Err()}
					return
				case out <- StreamEvent{Chunk: chunk}:
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data.WriteString(strings.TrimPrefix(line, "data:"))
			}
			// Other SSE lines (event:, id:, retry:, comments) are
			// ignored — we only care about the data payload.
		}
		if err := scanner.Err(); err != nil {
			out <- StreamEvent{Err: err}
		}
	}()
	return out, nil
}
