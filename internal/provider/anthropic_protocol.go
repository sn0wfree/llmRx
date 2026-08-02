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

func init() {
	RegisterProtocol("anthropic", NewAnthropicProvider())
	RegisterProtocol("anthropic-messages", NewAnthropicProvider())
}

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
	System      any                `json:"system,omitempty"`
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

type anthropicSystemBlock struct {
	Type         string             `json:"type"`
	Text         string             `json:"text"`
	CacheControl *anthropicCacheCtl `json:"cache_control,omitempty"`
}

type anthropicCacheCtl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContentBlock struct {
	Type         string             `json:"type"`
	Text         string             `json:"text,omitempty"`
	CacheControl *anthropicCacheCtl `json:"cache_control,omitempty"`
}

type anthropicResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	Content    []anthropicContent `json:"content"`
	Usage      anthropicUsage     `json:"usage"`
	StopReason string             `json:"stop_reason"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *AnthropicProvider) Chat(ctx context.Context, req *ChatRequest, apiKey, baseURL string) (*ChatResponse, int, error) {
	body, err := json.Marshal(p.translateReq(req))
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(baseURL, "/")+"/v1/messages", bytes.NewReader(body))
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
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, readErrorSnippet(resp.Body))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
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
		ID:     ar.ID,
		Object: "chat.completion",
		Model:  ar.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: text},
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
	var systemStrings []string
	var systemBlocks []anthropicSystemBlock
	var msgs []anthropicMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			txt := m.ContentString()
			if m.CacheControl != nil {
				systemBlocks = append(systemBlocks, anthropicSystemBlock{
					Type:         "text",
					Text:         txt,
					CacheControl: &anthropicCacheCtl{Type: m.CacheControl.Type},
				})
			} else {
				systemStrings = append(systemStrings, txt)
			}
			continue
		}
		var content any = m.ContentString()
		if m.CacheControl != nil {
			content = []anthropicContentBlock{{
				Type:         "text",
				Text:         m.ContentString(),
				CacheControl: &anthropicCacheCtl{Type: m.CacheControl.Type},
			}}
		}
		msgs = append(msgs, anthropicMessage{Role: m.Role, Content: content})
	}
	maxTokens := req.MaxTokens
	if req.MaxCompletionTokens > 0 {
		maxTokens = req.MaxCompletionTokens
	}
	if maxTokens == 0 {
		maxTokens = 1024
	}
	var systemField any = strings.Join(systemStrings, "\n")
	if len(systemBlocks) > 0 {
		systemField = systemBlocks
	}
	out := anthropicRequest{
		Model:       req.Model,
		Messages:    msgs,
		System:      systemField,
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
		var eventType string
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
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
								chunk := StreamChunk{
									Object:  "chat.completion.chunk",
									Choices: []StreamChoice{{Index: 0, Delta: Message{Content: d.Delta.Text}}},
								}
								select {
								case <-ctx.Done():
									return
								case out <- StreamEvent{Chunk: chunk}:
								}
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
									Model:   m.Message.Model,
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
