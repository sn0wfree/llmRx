// Package provider includes multi-protocol adapters. The default
// OpenAIProvider remains the workhorse; AnthropicProvider and
// GeminiProvider speak their respective wire protocols. The
// Factory function picks the right adapter based on the channel's
// Protocol field.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Factory returns a provider suitable for the given protocol tag.
// Unknown values fall back to OpenAIProvider.
func Factory(protocol string) Provider {
	switch strings.ToLower(protocol) {
	case "", "openai", "openai-compatible":
		return NewOpenAIProvider()
	case "anthropic", "anthropic-messages":
		return NewAnthropicProvider()
	case "gemini", "google-gemini":
		return NewGeminiProvider()
	default:
		return NewOpenAIProvider()
	}
}

// ---------------- Anthropic ----------------

// AnthropicProvider speaks the Anthropic Messages API. Differences
// from OpenAI worth noting:
//
//   - Endpoint:  POST {base}/v1/messages
//   - Auth:      x-api-key header (NOT Authorization: Bearer)
//   - Version:   anthropic-version: 2023-06-01 header
//   - Body:      {model, messages:[{role,content}], max_tokens}
//     (system prompt is a top-level field, not a message)
//   - Response:  {content:[{type:"text", text:"..."}], usage:{...}}
//   - Streaming: SSE with event types (message_start, content_block_delta,
//     message_delta, message_stop). Translates to OpenAI-style chunks
//     for the StreamingProvider interface.
type AnthropicProvider struct {
	client *http.Client
}

func NewAnthropicProvider() *AnthropicProvider {
	return &AnthropicProvider{client: &http.Client{Timeout: 120 * time.Second}}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	TopK        *int               `json:"top_k,omitempty"`
	StopSeq     any                `json:"stop_sequences,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Metadata    map[string]any     `json:"metadata,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
}

// anthropicTool mirrors Anthropic's tool block.
type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Content []anthropicContent  `json:"content"`
	Usage   anthropicUsage      `json:"usage"`
	StopReason string           `json:"stop_reason"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *AnthropicProvider) Chat(req *ChatRequest, apiKey, baseURL string) (*ChatResponse, int, error) {
	body, err := json.Marshal(p.translateReq(req))
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequest("POST", strings.TrimRight(baseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(raw))
	}
	var ar anthropicResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, resp.StatusCode, err
	}
	text := ""
	for _, c := range ar.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	return &ChatResponse{
		ID:    ar.ID,
		Object: "chat.completion",
		Model: ar.Model,
		Choices: []Choice{{
			Index: 0,
			Message: Message{Role: "assistant", Content: text},
			FinishReason: ar.StopReason,
		}},
		Usage: Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}, resp.StatusCode, nil
}

func (p *AnthropicProvider) translateReq(req *ChatRequest) anthropicRequest {
	var systemParts []string
	var msgs []anthropicMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.ContentString())
			continue
		}
		msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.ContentString()})
	}
	maxTokens := req.MaxTokens
	if req.MaxCompletionTokens > 0 {
		maxTokens = req.MaxCompletionTokens
	}
	if maxTokens == 0 {
		maxTokens = 1024 // Anthropic requires max_tokens
	}
	out := anthropicRequest{
		Model:       req.Model,
		Messages:    msgs,
		System:      strings.Join(systemParts, "\n"),
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeq:     req.Stop,
		Stream:      req.Stream,
		Metadata:    req.Metadata,
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]anthropicTool, len(req.Tools))
		for i, t := range req.Tools {
			out.Tools[i] = anthropicTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			}
		}
		if req.ToolChoice != nil {
			out.ToolChoice = req.ToolChoice
		} else {
			out.ToolChoice = map[string]any{"type": "auto"}
		}
	}
	return out
}

// StreamChat implements StreamingProvider.
func (p *AnthropicProvider) StreamChat(ctx context.Context, req *ChatRequest, apiKey, baseURL string) (<-chan StreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(p.translateReq(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(baseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
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
		// Anthropic SSE: lines starting with "event:" tell us the
		// event type; the next line "data:" carries the JSON.
		// The OpenAI chunk we emit reuses the same JSON shape so
		// downstream code is protocol-agnostic.
		var eventType string
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		acc := strings.Builder{}
		for {
			n, err := resp.Body.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				for {
					idx := bytes.IndexByte(buf, '\n')
					if idx < 0 {
						break
					}
					line := strings.TrimRight(string(buf[:idx]), "\r")
					buf = buf[idx+1:]
					if line == "" {
						eventType = ""
						continue
					}
					switch {
					case strings.HasPrefix(line, "event: "):
						eventType = strings.TrimPrefix(line, "event: ")
					case strings.HasPrefix(line, "data: "):
						payload := strings.TrimPrefix(line, "data: ")
						if eventType == "content_block_delta" {
							var d struct {
								Delta struct {
									Type string `json:"type"`
									Text string `json:"text"`
								} `json:"delta"`
							}
							if json.Unmarshal([]byte(payload), &d) == nil {
								acc.WriteString(d.Delta.Text)
							}
						}
						if eventType == "message_start" {
							var m struct {
								Message struct {
									ID    string `json:"id"`
									Model string `json:"model"`
								} `json:"message"`
							}
							if json.Unmarshal([]byte(payload), &m) == nil {
								chunk := StreamChunk{
									ID: m.Message.ID, Object: "chat.completion.chunk",
									Model: m.Message.Model,
									Choices: []StreamChoice{{Index: 0, Delta: Message{Role: "assistant"}}},
								}
								select {
								case <-ctx.Done():
									return
								case out <- StreamEvent{Chunk: chunk}:
								}
							}
						}
						if eventType == "message_delta" {
							var d struct {
								Usage anthropicUsage `json:"usage"`
							}
							if json.Unmarshal([]byte(payload), &d) == nil {
								chunk := StreamChunk{
									Object:  "chat.completion.chunk",
									Choices: []StreamChoice{{Index: 0, Delta: Message{}, FinishReason: "stop"}},
									Usage: &Usage{
										PromptTokens:     d.Usage.InputTokens,
										CompletionTokens: d.Usage.OutputTokens,
									},
								}
								select {
								case <-ctx.Done():
									return
								case out <- StreamEvent{Chunk: chunk}:
								}
							}
						}
						if eventType == "message_stop" {
							return
						}
					}
				}
			}
			if err != nil {
				if err != io.EOF {
					out <- StreamEvent{Err: err}
				}
				return
			}
		}
	}()
	return out, nil
}

// ---------------- Gemini ----------------

// GeminiProvider speaks the Google Generative Language API. Differences
// from OpenAI:
//
//   - Endpoint:  POST {base}/v1beta/models/{model}:generateContent
//   - Auth:      ?key=... query param (NOT Authorization header)
//   - Body:      {contents:[{role, parts:[{text}]}]}
//   - Response:  {candidates:[{content:{parts:[{text}]}, finishReason}], usageMetadata}
type GeminiProvider struct {
	client *http.Client
}

func NewGeminiProvider() *GeminiProvider {
	return &GeminiProvider{client: &http.Client{Timeout: 120 * time.Second}}
}

func (p *GeminiProvider) Name() string { return "gemini" }

type geminiRequest struct {
	Contents          []geminiContent       `json:"contents"`
	SystemInstruction *geminiPart           `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationCfg  `json:"generationConfig,omitempty"`
	Tools             []geminiTool          `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig     `json:"toolConfig,omitempty"`
}

// geminiGenerationCfg mirrors the Gemini generationConfig block.
// Only fields with non-zero values survive JSON marshalling.
type geminiGenerationCfg struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
	CandidateCount  *int     `json:"candidateCount,omitempty"`
	ResponseSchema  map[string]any `json:"responseSchema,omitempty"`
	ResponseMimeType string  `json:"responseMimeType,omitempty"`
}

// geminiTool is Gemini's tool declaration. FunctionDeclarations
// arrays match the OpenAI tools shape.
type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *struct {
		Mode string `json:"mode,omitempty"` // AUTO | ANY | NONE
	} `json:"functionCallingConfig,omitempty"`
}

type geminiContent struct {
	Role  string      `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (p *GeminiProvider) Chat(req *ChatRequest, apiKey, baseURL string) (*ChatResponse, int, error) {
	body, err := json.Marshal(p.translateReq(req))
	if err != nil {
		return nil, 0, err
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s",
		strings.TrimRight(baseURL, "/"), req.Model, apiKey)
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(raw))
	}
	var gr geminiResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, resp.StatusCode, err
	}
	var text string
	if len(gr.Candidates) > 0 {
		for _, p := range gr.Candidates[0].Content.Parts {
			text += p.Text
		}
	}
	return &ChatResponse{
		Object: "chat.completion",
		Model:  req.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: text},
			FinishReason: lowerFirst(gr.Candidates[0].FinishReason),
		}},
		Usage: Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		},
	}, resp.StatusCode, nil
}

func (p *GeminiProvider) translateReq(req *ChatRequest) geminiRequest {
	out := geminiRequest{}
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.ContentString())
			continue
		}
		// Gemini uses "user" / "model" roles.
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		out.Contents = append(out.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.ContentString()}},
		})
	}
	if len(systemParts) > 0 {
		out.SystemInstruction = &geminiPart{Text: strings.Join(systemParts, "\n")}
	}
	// GenerationConfig — set whenever the client specified any knob
	// that maps onto Gemini's knobs.
	maxTokens := req.MaxTokens
	if req.MaxCompletionTokens > 0 {
		maxTokens = req.MaxCompletionTokens
	}
	if maxTokens > 0 || req.Temperature != nil || req.TopP != nil || req.N != nil || req.Stop != nil || req.ResponseFormat != nil {
		gc := &geminiGenerationCfg{
			MaxOutputTokens: maxTokens,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			CandidateCount:  req.N,
		}
		if s, ok := req.Stop.(string); ok {
			gc.StopSequences = []string{s}
		} else if ss, ok := req.Stop.([]string); ok {
			gc.StopSequences = ss
		}
		if req.ResponseFormat != nil {
			switch req.ResponseFormat.Type {
			case "json_object":
				gc.ResponseMimeType = "application/json"
			case "json_schema":
				if req.ResponseFormat.JSONSchema != nil {
					gc.ResponseMimeType = "application/json"
					gc.ResponseSchema = req.ResponseFormat.JSONSchema.Schema
				}
			}
		}
		out.GenerationConfig = gc
	}
	// Tool declarations → Gemini's functionDeclarations wrapper.
	if len(req.Tools) > 0 {
		decls := make([]geminiFunctionDecl, len(req.Tools))
		for i, t := range req.Tools {
			decls[i] = geminiFunctionDecl{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			}
		}
		out.Tools = []geminiTool{{FunctionDeclarations: decls}}
		// Translate OpenAI tool_choice enum to Gemini's mode.
		out.ToolConfig = &geminiToolConfig{}
		mode := "AUTO"
		switch v := req.ToolChoice.(type) {
		case string:
			switch v {
			case "auto", "":
				mode = "AUTO"
			case "none":
				mode = "NONE"
			case "required":
				mode = "ANY"
			}
		case map[string]any:
			if t, _ := v["type"].(string); t == "function" {
				mode = "ANY"
			}
		}
		out.ToolConfig.FunctionCallingConfig = &struct {
			Mode string `json:"mode,omitempty"`
		}{Mode: mode}
	}
	return out
}

func lowerFirst(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// All returns the canonical map of protocol name → provider. Used
// by the api.Handler to resolve the right adapter for each
// channel based on its Protocol field.
func All() map[string]Provider {
	return map[string]Provider{
		"openai":              NewOpenAIProvider(),
		"openai-compatible":   NewOpenAIProvider(),
		"anthropic":           NewAnthropicProvider(),
		"anthropic-messages":  NewAnthropicProvider(),
		"gemini":              NewGeminiProvider(),
		"google-gemini":       NewGeminiProvider(),
	}
}
