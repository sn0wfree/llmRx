package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// ErrStreamUnsupported is returned by providers that don't implement
// the StreamingProvider interface.
var ErrStreamUnsupported = errors.New("streaming not supported by protocol")

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
	Stop                any             `json:"stop,omitempty"` // string or []string
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
	Role         string     `json:"role"`
	Content      any        `json:"content,omitempty"`
	Name         string     `json:"name,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID   string     `json:"tool_call_id,omitempty"`
	CacheControl *CacheCtl  `json:"cache_control,omitempty"` // Anthropic extension
	Refusal      string     `json:"refusal,omitempty"`

	// contentStr caches the result of ContentString() after the first
	// call, avoiding repeated allocation on the hot path.
	contentStr string
}

// ContentPart is one item in a multimodal message Content array.
// The provider implementations only care about "text" and
// "image_url"; other types pass through unchanged so the upstream
// can decide how to interpret them.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	CacheCtl *CacheCtl `json:"cache_control,omitempty"`
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
	Index    *int         `json:"index,omitempty"` // only present in streaming deltas
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"` // "function"
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
func (m *Message) ContentString() string {
	if m.contentStr != "" || m.Content == nil {
		return m.contentStr
	}
	switch v := m.Content.(type) {
	case nil:
		return ""
	case string:
		m.contentStr = v
		return v
	case []byte:
		m.contentStr = string(v)
		return m.contentStr
	}
	parts, _ := m.Content.([]any)
	if parts == nil {
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
	m.contentStr = b.String()
	return m.contentStr
}

// FloatPtr returns *f so request fields can be encoded only when set.
func FloatPtr(f float64) *float64 { return &f }
func IntPtr(i int) *int           { return &i }
func BoolPtr(b bool) *bool        { return &b }

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
	Chat(ctx context.Context, req *ChatRequest, apiKey string, baseURL string) (*ChatResponse, int, error)
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

// ModelLister is an optional capability. Protocols that can fetch
// the list of available models from the upstream API implement this.
// (Defined here instead of registry.go to keep it next to Provider.)
type ModelLister interface {
	ListModels(ctx context.Context, apiKey, baseURL string) ([]string, error)
}

// EmbeddingsRequest is the OpenAI-compatible embeddings request body.
type EmbeddingsRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"`                     // string or []string
	EncodingFormat string `json:"encoding_format,omitempty"` // "float" (default) or "base64"
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

// Embedding is a single embedding vector entry.
type Embedding struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// EmbeddingsResponse is the OpenAI-compatible embeddings response.
type EmbeddingsResponse struct {
	Object string      `json:"object"`
	Data   []Embedding `json:"data"`
	Model  string      `json:"model"`
	Usage  Usage       `json:"usage"`
}

// EmbeddingsProvider is an optional capability for providers that
// support the /v1/embeddings endpoint (e.g., OpenAI text-embedding-3).
type EmbeddingsProvider interface {
	Embeddings(ctx context.Context, req *EmbeddingsRequest, apiKey, baseURL string) (*EmbeddingsResponse, int, error)
}

// ---------- /v1/images/generations ----------

// ImagesRequest is the OpenAI-compatible image generation request.
type ImagesRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`            // "1024x1024" etc.
	Quality        string `json:"quality,omitempty"`         // "standard" / "hd"
	Style          string `json:"style,omitempty"`           // "vivid" / "natural"
	ResponseFormat string `json:"response_format,omitempty"` // "url" / "b64_json"
	User           string `json:"user,omitempty"`
}

// ImageObject is a single generated image (URL or b64).
type ImageObject struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImagesResponse is the OpenAI-compatible image generation response.
type ImagesResponse struct {
	Created int64         `json:"created"`
	Data    []ImageObject `json:"data"`
	Usage   *Usage        `json:"usage,omitempty"`
}

// ImagesProvider is an optional capability for providers that support
// image generation (OpenAI DALL-E, Stability, etc.).
type ImagesProvider interface {
	Images(ctx context.Context, req *ImagesRequest, apiKey, baseURL string) (*ImagesResponse, int, error)
}

// ---------- /v1/audio/speech & /v1/audio/transcriptions ----------

// AudioSpeechRequest is the OpenAI-compatible TTS request.
type AudioSpeechRequest struct {
	Model          string  `json:"model"` // "tts-1", "tts-1-hd"
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`                     // "alloy", "echo", "fable", "onyx", "nova", "shimmer"
	ResponseFormat string  `json:"response_format,omitempty"` // "mp3", "opus", "aac", "flac"
	Speed          float64 `json:"speed,omitempty"`
}

// AudioTranscriptionRequest is the OpenAI-compatible STT request.
// File content is sent as multipart/form-data in the HTTP layer;
// this struct captures the JSON metadata fields.
type AudioTranscriptionRequest struct {
	Model    string `json:"model"`
	Language string `json:"language,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Format   string `json:"format,omitempty"` // "json", "text", "srt", "verbose_json"
}

// AudioTranscriptionResponse is the STT response.
type AudioTranscriptionResponse struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// AudioProvider is an optional capability for providers that support
// TTS and/or STT (OpenAI Audio, Google Speech, etc.).
type AudioProvider interface {
	// Speech returns the raw audio bytes (encoded in req.ResponseFormat).
	Speech(ctx context.Context, req *AudioSpeechRequest, apiKey, baseURL string) ([]byte, int, error)
	// Transcription sends the audio bytes (encoded in audioMime) and
	// returns the recognized text.
	Transcription(ctx context.Context, req *AudioTranscriptionRequest, audioData []byte, audioMime, apiKey, baseURL string) (*AudioTranscriptionResponse, int, error)
}

// ---------- /v1/rerank ----------

// RerankRequest matches the Cohere-compatible /v1/rerank contract
// (also used by Jina, BGE-reranker, etc.).
type RerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
}

// RerankResult is one scored document.
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Document       *string `json:"document,omitempty"`
}

// RerankResponse is the rerank response.
type RerankResponse struct {
	Model   string         `json:"model"`
	Results []RerankResult `json:"results"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// RerankProvider is an optional capability for providers that support
// document reranking (Cohere, Jina, BGE, etc.).
type RerankProvider interface {
	Rerank(ctx context.Context, req *RerankRequest, apiKey, baseURL string) (*RerankResponse, int, error)
}

type OpenAIProvider struct {
	client *http.Client
}

// sharedTransport is a process-wide *http.Transport with a high
// MaxIdleConnsPerHost so concurrent requests to the same upstream
// reuse the existing TCP connection instead of dialing fresh ones.
// Default stdlib MaxIdleConnsPerHost is 2, which causes connection
// thrashing under modest concurrency to a single upstream.
var sharedTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   50,
	IdleConnTimeout:       90 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

func NewOpenAIProvider() *OpenAIProvider {
	return &OpenAIProvider{
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: sharedTransport,
		},
	}
}

func (p *OpenAIProvider) Name() string {
	return "openai-compatible"
}

func (p *OpenAIProvider) Chat(ctx context.Context, req *ChatRequest, apiKey string, baseURL string) (*ChatResponse, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
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

	if resp.StatusCode != http.StatusOK {
		// Only read a bounded snippet for the error message; a
		// hostile upstream returning a multi-MB HTML error page
		// shouldn't blow up the gateway.
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, readErrorSnippet(resp.Body))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
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
		// OpenAI streaming chunks are typically a few hundred bytes
		// (a few choices + maybe a usage block). 4 KiB initial +
		// 64 KiB ceiling fits any normal chunk while keeping the
		// per-stream allocation small.
		scanner.Buffer(make([]byte, 4*1024), 64*1024)
		// Pool the per-event accumulator so we don't churn a
		// strings.Builder for every SSE event. Reset() reuses the
		// backing storage.
		var dataBuf bytes.Buffer
		for scanner.Scan() {
			raw := scanner.Bytes()
			if len(raw) == 0 {
				// Dispatch the accumulated data block.
				if dataBuf.Len() == 0 {
					continue
				}
				payload := bytes.TrimSpace(dataBuf.Bytes())
				dataBuf.Reset()
				if bytes.Equal(payload, []byte("[DONE]")) {
					return
				}
				var chunk StreamChunk
				if err := json.Unmarshal(payload, &chunk); err != nil {
					// Skip malformed lines but keep going.
					continue
				}
				select {
				case <-ctx.Done():
					// The consumer (api/router.go) returns on ctx
					// cancellation without draining out, so sending
					// here would block forever and leak this
					// goroutine + the upstream connection.
					return
				case out <- StreamEvent{Chunk: chunk}:
				}
				continue
			}
			if bytes.HasPrefix(raw, []byte("data:")) {
				dataBuf.Write(raw[5:])
			}
			// Other SSE lines (event:, id:, retry:, comments) are
			// ignored — we only care about the data payload.
		}
		if err := scanner.Err(); err != nil {
			select {
			case <-ctx.Done():
				return
			case out <- StreamEvent{Err: err}:
			}
		}
	}()
	return out, nil
}

// ListModels fetches the list of available models from the upstream
// API by calling GET {baseURL}/models. Works with any OpenAI-compatible
// upstream (OpenAI, MiniMax, DeepSeek, Moonshot, Qwen, etc.).
func (p *OpenAIProvider) ListModels(ctx context.Context, apiKey, baseURL string) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(body))
	}

	// OpenAI-shaped response: {object: "list", data: [{id: "...", ...}]}
	var result struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// Embeddings sends an embeddings request to the upstream API.
func (p *OpenAIProvider) Embeddings(ctx context.Context, req *EmbeddingsRequest, apiKey, baseURL string) (*EmbeddingsResponse, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/embeddings", bytes.NewReader(body))
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

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, readErrorSnippet(resp.Body))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	var embResp EmbeddingsResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal response: %w", err)
	}

	return &embResp, resp.StatusCode, nil
}

// Images POSTs to /images/generations and returns the parsed body.
func (p *OpenAIProvider) Images(ctx context.Context, req *ImagesRequest, apiKey, baseURL string) (*ImagesResponse, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/images/generations", bytes.NewReader(body))
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
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, readErrorSnippet(resp.Body))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	var imgResp ImagesResponse
	if err := json.Unmarshal(respBody, &imgResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal response: %w", err)
	}
	return &imgResp, resp.StatusCode, nil
}

// Speech POSTs to /audio/speech and returns the raw audio bytes.
func (p *OpenAIProvider) Speech(ctx context.Context, req *AudioSpeechRequest, apiKey, baseURL string) ([]byte, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "audio/mpeg")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(audio))
	}
	return audio, resp.StatusCode, nil
}

// Transcription POSTs multipart/form-data with the audio file to
// /audio/transcriptions. The model/language/format fields are sent as
// form fields; the audio bytes are sent as the 'file' field.
func (p *OpenAIProvider) Transcription(ctx context.Context, req *AudioTranscriptionRequest, audioData []byte, audioMime, apiKey, baseURL string) (*AudioTranscriptionResponse, int, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", req.Model); err != nil {
		return nil, 0, err
	}
	if req.Language != "" {
		_ = mw.WriteField("language", req.Language)
	}
	if req.Prompt != "" {
		_ = mw.WriteField("prompt", req.Prompt)
	}
	if req.Format != "" {
		_ = mw.WriteField("response_format", req.Format)
	}
	fw, err := mw.CreateFormFile("file", "audio."+extFromMime(audioMime))
	if err != nil {
		return nil, 0, fmt.Errorf("create form file: %w", err)
	}
	if _, err := fw.Write(audioData); err != nil {
		return nil, 0, fmt.Errorf("write audio: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, 0, fmt.Errorf("close multipart: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, readErrorSnippet(resp.Body))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	var tr AudioTranscriptionResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal response: %w", err)
	}
	return &tr, resp.StatusCode, nil
}

// Rerank POSTs to /rerank and returns the parsed body.
func (p *OpenAIProvider) Rerank(ctx context.Context, req *RerankRequest, apiKey, baseURL string) (*RerankResponse, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/rerank", bytes.NewReader(body))
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
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, readErrorSnippet(resp.Body))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	var rr RerankResponse
	if err := json.Unmarshal(respBody, &rr); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal response: %w", err)
	}
	return &rr, resp.StatusCode, nil
}

func extFromMime(mime string) string {
	switch mime {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/x-wav":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/webm":
		return "webm"
	case "audio/flac":
		return "flac"
	case "audio/m4a", "audio/x-m4a", "audio/mp4":
		return "m4a"
	}
	return "bin"
}
