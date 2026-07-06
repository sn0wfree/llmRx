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

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// StreamChunk is one server-sent event from an OpenAI-compatible
// streaming chat completion. The object is always "chat.completion.chunk".
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// StreamChoice is one delta inside a stream chunk.
type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
