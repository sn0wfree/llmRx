package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

func init() {
	RegisterProtocol("gemini", NewGeminiProvider())
	RegisterProtocol("google-gemini", NewGeminiProvider())
}

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
	Contents          []geminiContent      `json:"contents"`
	SystemInstruction *geminiPart          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationCfg `json:"generationConfig,omitempty"`
	Tools             []geminiTool         `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig    `json:"toolConfig,omitempty"`
}

type geminiGenerationCfg struct {
	MaxOutputTokens  int            `json:"maxOutputTokens,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"topP,omitempty"`
	TopK             *int           `json:"topK,omitempty"`
	StopSequences    []string       `json:"stopSequences,omitempty"`
	CandidateCount   *int           `json:"candidateCount,omitempty"`
	ResponseSchema   map[string]any `json:"responseSchema,omitempty"`
	ResponseMimeType string         `json:"responseMimeType,omitempty"`
}

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
		Mode string `json:"mode,omitempty"`
	} `json:"functionCallingConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
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

func (p *GeminiProvider) Chat(ctx context.Context, req *ChatRequest, apiKey, baseURL string) (*ChatResponse, int, error) {
	body, err := json.Marshal(p.translateReq(req))
	if err != nil {
		return nil, 0, err
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s",
		strings.TrimRight(baseURL, "/"), req.Model, apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
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
	var gr geminiResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, resp.StatusCode, err
	}
	if len(gr.Candidates) == 0 {
		return nil, http.StatusBadGateway, fmt.Errorf("upstream returned no candidates (raw=%s)", truncate(string(raw), 200))
	}
	cand := gr.Candidates[0]
	var text string
	for _, p := range cand.Content.Parts {
		text += p.Text
	}
	return &ChatResponse{
		Object: "chat.completion",
		Model:  req.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: text},
			FinishReason: lowerFirst(cand.FinishReason),
		}},
		Usage: Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		},
	}, resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (p *GeminiProvider) translateReq(req *ChatRequest) geminiRequest {
	out := geminiRequest{}
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.ContentString())
			continue
		}
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
