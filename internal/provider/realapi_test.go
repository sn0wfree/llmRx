//go:build realapi
// +build realapi

package provider

// ============================================================================
// Real-LLM integration tests
// ----------------------------------------------------------------------------
// These tests hit the ACTUAL OpenAI and Anthropic APIs. They are skipped by
// default to keep `go test ./...` hermetic and to avoid:
//
//   1. Cost: every request is billed to the operator's account.
//   2. Account risk: even at low rate, repeated runs from CI IPs can trip
//      rate limits or anti-abuse heuristics → account suspension.
//   3. Network flakiness: upstream outages or DNS issues cause CI red.
//
// To enable, set BOTH:
//
//   LLMRX_RUN_REAL_API_TESTS=1        # master switch
//   OPENAI_API_KEY=sk-...             # OpenAI test key
//   ANTHROPIC_API_KEY=sk-ant-...      # Anthropic test key (optional)
//
// Then run with:
//
//   go test -tags=realapi ./internal/provider -v
//
// The `-tags=realapi` build tag is REQUIRED in addition to the env var, so
// even an operator with the env set can't accidentally trip these on a
// blanket `go test ./...` run.
//
// Frequency guard: every test in this file funnels through rateGate, which
// enforces a hard minimum spacing between calls (default 5s, configurable
// via LLMRX_REAL_API_TEST_INTERVAL_MS). With 4 tests × 3 calls each, the
// suite spends ~60s of wall time but only fires ~12 requests.
//
// Cost guard: each test uses the cheapest available model and the smallest
// reasonable max_tokens. Default models: gpt-4o-mini, claude-3-haiku.
// Override via LLMRX_REAL_OPENAI_MODEL / LLMRX_REAL_ANTHROPIC_MODEL.
// ============================================================================

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- guards ---------------------------------------------------------------

// realAPIMu / realAPILastAt form a package-level rate limiter. Each
// call to `gate()` blocks for at least `interval` since the previous
// release, so the slowest tests pace the whole file.
var (
	realAPIMu     sync.Mutex
	realAPILastAt time.Time
)

// interval is configurable via env to support suites that want a
// tighter or looser pacing. Default is 5s.
func realAPIInterval() time.Duration {
	ms := os.Getenv("LLMRX_REAL_API_TEST_INTERVAL_MS")
	if ms == "" {
		return 5 * time.Second
	}
	d, err := time.ParseDuration(ms + "ms")
	if err != nil {
		return 5 * time.Second
	}
	return d
}

// gate blocks until `interval` has passed since the previous release.
// Skips if real-API tests aren't enabled via env+build tag.
func gate(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-API test under -short")
	}
	if os.Getenv("LLMRX_RUN_REAL_API_TESTS") != "1" {
		t.Skip("set LLMRX_RUN_REAL_API_TESTS=1 to enable real-API tests")
	}

	realAPIMu.Lock()
	defer realAPIMu.Unlock()

	now := time.Now()
	if !realAPILastAt.IsZero() {
		next := realAPILastAt.Add(realAPIInterval())
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
	}
	realAPILastAt = now
}

// mustEnv returns the value of `name` or skips the test. Centralizes
// the "missing key → skip" check so individual tests stay focused.
func mustEnv(t *testing.T, name string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		t.Skipf("missing required env var %s", name)
	}
	return v
}

// modelFor returns the model to use for a provider. Falls back to the
// cheapest default if LLMRX_REAL_*_MODEL isn't set.
func modelFor(providerName string) string {
	switch providerName {
	case "openai":
		if v := os.Getenv("LLMRX_REAL_OPENAI_MODEL"); v != "" {
			return v
		}
		return "gpt-4o-mini"
	case "minimax":
		if v := os.Getenv("LLMRX_REAL_MINIMAX_MODEL"); v != "" {
			return v
		}
		return "MiniMax-Text-01"
	case "anthropic":
		if v := os.Getenv("LLMRX_REAL_ANTHROPIC_MODEL"); v != "" {
			return v
		}
		return "claude-3-haiku-20240307"
	}
	return ""
}

// baseURLFor returns the upstream base URL. Each provider has a
// stable, well-known endpoint. Kept as a func so tests can override
// via env if a proxy is in play.
func baseURLFor(providerName string) string {
	switch providerName {
	case "openai":
		if v := os.Getenv("LLMRX_REAL_OPENAI_BASE_URL"); v != "" {
			return v
		}
		return "https://api.openai.com/v1"
	case "minimax":
		if v := os.Getenv("LLMRX_REAL_MINIMAX_BASE_URL"); v != "" {
			return v
		}
		return "https://api.minimax.io/v1"
	case "anthropic":
		if v := os.Getenv("LLMRX_REAL_ANTHROPIC_BASE_URL"); v != "" {
			return v
		}
		return "https://api.anthropic.com/v1"
	}
	return ""
}

// contentAsString extracts the string content from a Message.Content
// that may be either a plain string or an OpenAI multi-part array.
// Real-API responses always use plain strings, but the type system
// can't prove that, so we centralize the type assertion here.
func contentAsString(t *testing.T, c any) string {
	t.Helper()
	if s, ok := c.(string); ok {
		return s
	}
	t.Fatalf("expected string content, got %T: %v", c, c)
	return ""
}

// preview truncates a string to n bytes for log output.
func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- OpenAI  tests -------------------------------------------------------

// TestRealAPI_OpenAI_Chat fires one minimal chat completion against
// the real OpenAI API. Asserts:
//   - HTTP 200
//   - Non-empty assistant content
//   - usage.prompt_tokens > 0
//   - usage.completion_tokens > 0
//   - First-byte latency is logged (operator triage)
func TestRealAPI_OpenAI_Chat(t *testing.T) {
	gate(t)
	key := mustEnv(t, "OPENAI_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("openai")
	base := baseURLFor("openai")

	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: "Reply with one short sentence."},
			{Role: "user", Content: "Say hello."},
		},
		MaxTokens:   16,
		Temperature: &temp,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, code, err := p.Chat(ctx, req, key, base)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("OpenAI Chat error: %v", err)
	}
	if code != 200 {
		t.Fatalf("OpenAI Chat status=%d, want 200", code)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("OpenAI Chat returned no choices")
	}
	content := contentAsString(t, resp.Choices[0].Message.Content)
	if content == "" {
		t.Fatal("OpenAI Chat returned empty content")
	}
	if resp.Usage.PromptTokens == 0 {
		t.Errorf("OpenAI Usage.PromptTokens = 0, want > 0")
	}
	if resp.Usage.CompletionTokens == 0 {
		t.Errorf("OpenAI Usage.CompletionTokens = 0, want > 0")
	}

	t.Logf("OpenAI Chat %s: %.2fs, prompt=%d, completion=%d, model=%s, content=%q",
		key[len(key)-6:], took.Seconds(),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Model, preview(content, 60))
}

// TestRealAPI_OpenAI_Chat_LongPrompt checks that a 200-token prompt
// succeeds and the reported prompt_tokens roughly matches the input.
// This catches a class of bug where the gateway mis-encodes long
// inputs (e.g. JSON-escape errors in the message array).
func TestRealAPI_OpenAI_Chat_LongPrompt(t *testing.T) {
	gate(t)
	key := mustEnv(t, "OPENAI_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("openai")
	base := baseURLFor("openai")

	temp := 0.0
	// 200 tokens ≈ 800 chars of English.
	longUser := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 16)
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: "Echo the user's last word."},
			{Role: "user", Content: longUser},
		},
		MaxTokens:   8,
		Temperature: &temp,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, code, err := p.Chat(ctx, req, key, base)
	if err != nil || code != 200 {
		t.Fatalf("OpenAI Chat long-prompt error: %v code=%d", err, code)
	}
	// Sanity: prompt_tokens should be in the 200-300 range. Loose
	// bounds because tokenizers vary, but anything below 100 means
	// the prompt wasn't actually sent.
	if resp.Usage.PromptTokens < 100 || resp.Usage.PromptTokens > 400 {
		t.Errorf("OpenAI prompt_tokens = %d, want 200-300", resp.Usage.PromptTokens)
	}
	t.Logf("OpenAI Chat long: prompt=%d, completion=%d",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

// --- Anthropic tests -----------------------------------------------------

// TestRealAPI_Anthropic_Chat fires a minimal Anthropic Messages call.
// Anthropic differs from OpenAI in auth header (x-api-key), endpoint
// shape, and the `system` field being top-level rather than a message.
// This test catches regressions in that translation layer.
func TestRealAPI_Anthropic_Chat(t *testing.T) {
	gate(t)
	key := mustEnv(t, "ANTHROPIC_API_KEY")

	p := NewAnthropicProvider()
	model := modelFor("anthropic")
	base := baseURLFor("anthropic")

	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "user", Content: "Say hello in one short sentence."},
		},
		MaxTokens:   32,
		Temperature: &temp,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, code, err := p.Chat(ctx, req, key, base)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("Anthropic Chat error: %v", err)
	}
	if code != 200 {
		t.Fatalf("Anthropic Chat status=%d, want 200", code)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("Anthropic Chat returned no choices")
	}
	content := contentAsString(t, resp.Choices[0].Message.Content)
	if content == "" {
		t.Fatal("Anthropic Chat returned empty content")
	}
	if resp.Usage.PromptTokens == 0 {
		t.Errorf("Anthropic Usage.PromptTokens = 0")
	}
	if resp.Usage.CompletionTokens == 0 {
		t.Errorf("Anthropic Usage.CompletionTokens = 0")
	}

	t.Logf("Anthropic Chat: %.2fs, prompt=%d, completion=%d, model=%s, content=%q",
		took.Seconds(),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Model, preview(content, 60))
}

// TestRealAPI_Anthropic_Streaming exercises the streaming path
// against real Anthropic. SSE protocol handling has historically been
// the most fragile part of the provider adapter; this catches
// regressions in event parsing (content_block_delta, message_stop, etc.)
// that a httptest mock can't reliably reproduce.
func TestRealAPI_Anthropic_Streaming(t *testing.T) {
	gate(t)
	key := mustEnv(t, "ANTHROPIC_API_KEY")

	p := NewAnthropicProvider()

	model := modelFor("anthropic")
	base := baseURLFor("anthropic")

	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "user", Content: "Count from 1 to 5."},
		},
		MaxTokens:   32,
		Temperature: &temp,
		Stream:      true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Cast to the concrete *AnthropicProvider to access StreamChat;
	// the protocol file declares it directly.
	ch, err := p.StreamChat(ctx, req, key, base)
	if err != nil {
		t.Fatalf("Anthropic StreamChat error: %v", err)
	}

	var chunks int
	var content strings.Builder
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("Anthropic stream event error: %v", ev.Err)
		}
		chunks++
		if len(ev.Chunk.Choices) > 0 {
			if s, ok := ev.Chunk.Choices[0].Delta.Content.(string); ok {
				content.WriteString(s)
			}
		}
	}

	if chunks == 0 {
		t.Fatal("Anthropic streaming produced no chunks")
	}
	if content.Len() == 0 {
		t.Errorf("Anthropic streaming produced no content across %d chunks", chunks)
	}
	t.Logf("Anthropic stream: %d chunks, content=%q", chunks, preview(content.String(), 60))
}

// --- MiniMax tests -------------------------------------------------------

// TestRealAPI_Minimax_Chat fires a minimal chat completion against
// the real MiniMax API. MiniMax speaks the OpenAI-compatible protocol,
// so this validates that the gateway's OpenAI adapter works with
// non-OpenAI providers that claim compatibility.
func TestRealAPI_Minimax_Chat(t *testing.T) {
	gate(t)
	key := mustEnv(t, "LLMRX_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("minimax")
	base := baseURLFor("minimax")

	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: "Reply with one short sentence."},
			{Role: "user", Content: "Say hello."},
		},
		MaxTokens:   16,
		Temperature: &temp,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, code, err := p.Chat(ctx, req, key, base)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("Minimax Chat error: %v", err)
	}
	if code != 200 {
		t.Fatalf("Minimax Chat status=%d, want 200", code)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("Minimax Chat returned no choices")
	}
	content := contentAsString(t, resp.Choices[0].Message.Content)
	// Some models (like minimax-m3) return reasoning_content instead of
	// content for thinking models. Fall back to reasoning_content if empty.
	if content == "" && resp.Choices[0].Message.ReasoningContent != "" {
		content = "[reasoning] " + resp.Choices[0].Message.ReasoningContent
	}
	if content == "" {
		t.Fatal("Minimax Chat returned empty content")
	}
	if resp.Usage.PromptTokens == 0 {
		t.Errorf("Minimax Usage.PromptTokens = 0, want > 0")
	}
	if resp.Usage.CompletionTokens == 0 {
		t.Errorf("Minimax Usage.CompletionTokens = 0, want > 0")
	}

	t.Logf("Minimax Chat %s: %.2fs, prompt=%d, completion=%d, model=%s, content=%q",
		key[len(key)-6:], took.Seconds(),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Model, preview(content, 60))
}

// TestRealAPI_Minimax_Chat_LongPrompt checks that a 200-token prompt
// succeeds and the reported prompt_tokens roughly matches the input.
func TestRealAPI_Minimax_Chat_LongPrompt(t *testing.T) {
	gate(t)
	key := mustEnv(t, "LLMRX_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("minimax")
	base := baseURLFor("minimax")

	temp := 0.0
	longUser := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 16)
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: "Echo the user's last word."},
			{Role: "user", Content: longUser},
		},
		MaxTokens:   8,
		Temperature: &temp,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, code, err := p.Chat(ctx, req, key, base)
	if err != nil || code != 200 {
		t.Fatalf("Minimax Chat long-prompt error: %v code=%d", err, code)
	}
	if resp.Usage.PromptTokens < 100 || resp.Usage.PromptTokens > 400 {
		t.Errorf("Minimax prompt_tokens = %d, want 200-300", resp.Usage.PromptTokens)
	}
	t.Logf("Minimax Chat long: prompt=%d, completion=%d",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
}

// TestRealAPI_Minimax_Concurrent fires 3 concurrent requests to test
// parallel throughput and connection pooling. This simulates real-world
// usage where multiple users hit the gateway simultaneously.
func TestRealAPI_Minimax_Concurrent(t *testing.T) {
	gate(t)
	key := mustEnv(t, "LLMRX_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("minimax")
	base := baseURLFor("minimax")

	const concurrency = 3
	var wg sync.WaitGroup
	results := make([]struct {
		id      int
		took    time.Duration
		prompt  int
		complet int
		content string
		err     error
	}, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			temp := 0.0
			req := &ChatRequest{
				Model: model,
				Messages: []Message{
					{Role: "user", Content: fmt.Sprintf("Say hello and count to %d", id+1)},
				},
				MaxTokens:   32,
				Temperature: &temp,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			start := time.Now()
			resp, code, err := p.Chat(ctx, req, key, base)
			took := time.Since(start)

			if err != nil {
				results[id] = struct {
					id      int
					took    time.Duration
					prompt  int
					complet int
					content string
					err     error
				}{id: id, took: took, err: err}
				return
			}
			if code != 200 {
				results[id] = struct {
					id      int
					took    time.Duration
					prompt  int
					complet int
					content string
					err     error
				}{id: id, took: took, err: fmt.Errorf("status %d", code)}
				return
			}
			content := contentAsString(t, resp.Choices[0].Message.Content)
			if content == "" && resp.Choices[0].Message.ReasoningContent != "" {
				content = "[reasoning] " + resp.Choices[0].Message.ReasoningContent
			}
			results[id] = struct {
				id      int
				took    time.Duration
				prompt  int
				complet int
				content string
				err     error
			}{id: id, took: took, prompt: resp.Usage.PromptTokens, complet: resp.Usage.CompletionTokens, content: preview(content, 60)}
		}(i)
	}
	wg.Wait()

	t.Logf("== Concurrent test: %d requests ==", concurrency)
	var totalTook time.Duration
	for _, r := range results {
		if r.err != nil {
			t.Errorf("  Request %d: ERROR %v", r.id, r.err)
		} else {
			t.Logf("  Request %d: %.2fs, prompt=%d, completion=%d, content=%q",
				r.id, r.took.Seconds(), r.prompt, r.complet, r.content)
			totalTook += r.took
		}
	}
	avg := totalTook / time.Duration(concurrency)
	t.Logf("  Average latency: %.2fs", avg.Seconds())
}

// TestRealAPI_Minimax_LongContext fires a single request with a long
// system prompt (~500 tokens) to test context window handling and
// instruction following under load.
func TestRealAPI_Minimax_LongContext(t *testing.T) {
	gate(t)
	key := mustEnv(t, "LLMRX_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("minimax")
	base := baseURLFor("minimax")

	// Long system prompt: ~500 tokens of instructions
	longSystem := `You are a helpful assistant that specializes in Chinese-English translation.
When translating, follow these rules:
1. Preserve the original tone and style
2. Use appropriate idioms when available
3. Keep technical terms accurate
4. Maintain paragraph structure
5. For code comments, keep them in English unless specifically asked to translate
6. For proper nouns, keep the original form with Chinese translation in parentheses
7. For numbers and dates, follow the target language convention
8. For quotes, use the appropriate quotation marks for each language
9. For lists, maintain the same formatting style
10. For links, keep them unchanged

Please translate the following text from Chinese to English:`

	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: longSystem},
			{Role: "user", Content: "请翻译以下内容：今天天气很好，我们去公园散步吧。公园里有很多人在锻炼身体，有的在跑步，有的在打太极拳，还有的在跳广场舞。"},
		},
		MaxTokens:   128,
		Temperature: &temp,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	resp, code, err := p.Chat(ctx, req, key, base)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("Long context error: %v", err)
	}
	if code != 200 {
		t.Fatalf("Long context status=%d, want 200", code)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("Long context returned no choices")
	}

	content := contentAsString(t, resp.Choices[0].Message.Content)
	if content == "" && resp.Choices[0].Message.ReasoningContent != "" {
		content = "[reasoning] " + resp.Choices[0].Message.ReasoningContent
	}
	if content == "" {
		t.Fatal("Long context returned empty content")
	}

	t.Logf("== Long Context Test: %s ==", model)
	t.Logf("  Latency: %.2fs", took.Seconds())
	t.Logf("  Prompt tokens: %d", resp.Usage.PromptTokens)
	t.Logf("  Completion tokens: %d", resp.Usage.CompletionTokens)
	t.Logf("  Model: %s", resp.Model)
	t.Logf("  Content: %s", preview(content, 200))
}

// TestRealAPI_Minimax_100kContext fires a single request with ~100k tokens
// of context to test the model's ability to handle very long inputs.
// This tests both the context window size and instruction following
// under extreme token loads.
func TestRealAPI_Minimax_100kContext(t *testing.T) {
	gate(t)
	key := mustEnv(t, "LLMRX_API_KEY")

	p := NewOpenAIProvider()
	model := modelFor("minimax")
	base := baseURLFor("minimax")

	// Generate ~100k tokens of context. Each paragraph is ~200 tokens,
	// so we need ~500 paragraphs to reach 100k tokens.
	paragraphs := make([]string, 500)
	for i := range paragraphs {
		paragraphs[i] = fmt.Sprintf(
			"Paragraph %d: This is a test paragraph to fill the context window. "+
				"The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs. "+
				"How vexingly quick daft zebras jump! The five boxing wizards jump quickly. "+
				"Jackdaws love my big sphinx of quartz. Crazy Frederick bought many very exquisite opal jewels. "+
				"We promptly judged antique ivory buckles for the next prize. "+
				"A mad boxer shot a quick, gloved jab to the jaw of his dizzy opponent. "+
				"Sixty zippers were quickly picked from the woven jute bag.",
			i+1)
	}

	// System prompt explaining what to do with the long context
	systemPrompt := `You are a helpful assistant. You will receive a very long document 
with 500 numbered paragraphs. After reading the entire document, please:
1. Count the total number of paragraphs
2. Identify the paragraph with the highest number
3. List any patterns you notice in the content
4. Provide a brief summary of the document's purpose

Be concise in your response.`

	// Combine all paragraphs into one massive user message
	userMessage := "Here is the document:\n\n" + strings.Join(paragraphs, "\n\n") +
		"\n\nPlease analyze this document as instructed above."

	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		MaxTokens:   256,
		Temperature: &temp,
	}

	t.Logf("== 100k Context Test: %s ==", model)
	t.Logf("  Input length: ~%d characters", len(userMessage))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	start := time.Now()
	resp, code, err := p.Chat(ctx, req, key, base)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("100k context error: %v", err)
	}
	if code != 200 {
		t.Fatalf("100k context status=%d, want 200", code)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("100k context returned no choices")
	}

	content := contentAsString(t, resp.Choices[0].Message.Content)
	if content == "" && resp.Choices[0].Message.ReasoningContent != "" {
		content = "[reasoning] " + resp.Choices[0].Message.ReasoningContent
	}
	if content == "" {
		t.Fatal("100k context returned empty content")
	}

	t.Logf("  Latency: %.2fs", took.Seconds())
	t.Logf("  Prompt tokens: %d", resp.Usage.PromptTokens)
	t.Logf("  Completion tokens: %d", resp.Usage.CompletionTokens)
	t.Logf("  Model: %s", resp.Model)
	t.Logf("  Content: %s", preview(content, 300))
}