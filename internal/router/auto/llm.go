package auto

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Cause values reported by the LLM classifier.
const (
	// CauseLLM marks a decision produced by the LLM classifier.
	CauseLLM = "llm"
	// CauseHeuristicFallback marks a decision produced by the
	// heuristic scorer after the LLM classifier failed, timed out
	// or returned an invalid tier.
	CauseHeuristicFallback = "heuristic_fallback"
)

// LLMClassifierConfig describes an optional OpenAI-compatible
// chat endpoint used to upgrade tier decisions. It is an
// independent endpoint (a small, cheap model) — it never shares
// a business channel with user traffic.
type LLMClassifierConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	// Timeout bounds the classifier call; any latency beyond it
	// falls back to the heuristic scorer. Default 1.5s.
	Timeout time.Duration
}

// LLMClassifier classifies prompts through a small OpenAI-
// compatible chat endpoint asking for a JSON verdict. Any failure
// — network error, timeout, non-2xx, unparseable body, unknown
// tier — falls back to the heuristic scorer, so routing never
// depends on the classifier's availability.
type LLMClassifier struct {
	client   *http.Client
	baseURL  string
	apiKey   string
	model    string
	fallback ComplexityScorer
}

// NewLLMClassifier returns a classifier wired to cfg.BaseURL with
// the given heuristic fallback (nil means default thresholds).
func NewLLMClassifier(cfg LLMClassifierConfig, fallback ComplexityScorer) *LLMClassifier {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	if fallback == nil {
		fallback = NewHeuristicScorer(DefaultThresholds())
	}
	return &LLMClassifier{
		client:   &http.Client{Timeout: timeout},
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		fallback: fallback,
	}
}

const classifierSystemPrompt = `You are a routing classifier for an LLM gateway. Classify the user's prompt by complexity into exactly one of: simple, standard, complex, agentic. Respond ONLY with a JSON object: {"tier": "...", "score": 0.0-1.0} where score is your complexity confidence (0 = trivial chit-chat, 1 = large-scale engineering/research task). No prose, no markdown.`

// llmResponse is the shape of choices[0].message.content from the
// classifier endpoint.
type llmResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Classify asks the LLM endpoint for a tier verdict and falls back
// to the heuristic on any failure.
func (c *LLMClassifier) Classify(text string) Score {
	if strings.TrimSpace(text) == "" {
		return Score{Tier: TierSimple, Score: 0, Cause: CauseEmpty}
	}
	body, err := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": classifierSystemPrompt},
			{"role": "user", "content": text},
		},
		"temperature": 0,
		"max_tokens":  50,
	})
	if err != nil {
		return c.fallbackScore(text)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return c.fallbackScore(text)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return c.fallbackScore(text)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.fallbackScore(text)
	}
	var parsed llmResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return c.fallbackScore(text)
	}
	if len(parsed.Choices) == 0 {
		return c.fallbackScore(text)
	}
	var verdict struct {
		Tier  string  `json:"tier"`
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(parsed.Choices[0].Message.Content), &verdict); err != nil {
		return c.fallbackScore(text)
	}
	if !ValidTier(verdict.Tier) {
		return c.fallbackScore(text)
	}
	score := verdict.Score
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	if score == 0 {
		score = 0.5 // missing score field: default to mid-confidence
	}
	return Score{Tier: Tier(verdict.Tier), Score: score, Cause: CauseLLM}
}

// fallbackScore delegates to the heuristic scorer and marks the
// decision as a fallback so the audit log shows why.
func (c *LLMClassifier) fallbackScore(text string) Score {
	s := c.fallback.Classify(text)
	s.Cause = CauseHeuristicFallback
	return s
}
