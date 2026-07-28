package provider

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/observability"
)

// RetryConfig holds retry and timeout settings. Zero values use
// defaults: MaxRetries=0 (disabled), BaseDelay=500ms, Timeout=60s.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	Timeout    time.Duration
}

// RetryDefaults returns the default retry configuration.
func RetryDefaults() RetryConfig {
	return RetryConfig{
		MaxRetries: 0,
		BaseDelay:  500 * time.Millisecond,
		Timeout:    60 * time.Second,
	}
}

// RetryingProvider wraps any Provider and adds automatic retry with
// exponential backoff. Retries are attempted on 5xx, timeout,
// connection reset, and 429 (rate limit) responses. Non-retryable
// errors (4xx except 429) are returned immediately.
type RetryingProvider struct {
	inner  Provider
	config RetryConfig
}

// NewRetryingProvider wraps the given provider with retry logic.
func NewRetryingProvider(inner Provider, cfg RetryConfig) *RetryingProvider {
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 500 * time.Millisecond
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &RetryingProvider{inner: inner, config: cfg}
}

func (r *RetryingProvider) Name() string {
	return r.inner.Name()
}

// Chat implements Provider with automatic retry and timeout.
func (r *RetryingProvider) Chat(ctx context.Context, req *ChatRequest, apiKey string, baseURL string) (*ChatResponse, int, error) {
	if r.config.MaxRetries <= 0 {
		return r.chatWithTimeout(ctx, req, apiKey, baseURL)
	}

	var lastErr error
	var lastStatus int

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		resp, status, err := r.chatWithTimeout(ctx, req, apiKey, baseURL)

		// Success or non-retryable error: return immediately.
		if (err == nil && status < 500) || !isRetryable(status, err) {
			return resp, status, err
		}

		lastErr = err
		lastStatus = status

		if attempt < r.config.MaxRetries {
			delay := r.retryDelay(status, err, attempt)
			if delay > 0 {
				observability.RecordRetry(req.Model)
				log.Printf("[retry] attempt %d/%d failed (status=%d), retrying in %v: %v",
					attempt+1, r.config.MaxRetries, status, delay, err)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, lastStatus, ctx.Err()
				}
			}
		}
	}

	return nil, lastStatus, lastErr
}

// chatWithTimeout wraps the inner Chat call with a context timeout.
func (r *RetryingProvider) chatWithTimeout(ctx context.Context, req *ChatRequest, apiKey string, baseURL string) (*ChatResponse, int, error) {
	if r.config.Timeout <= 0 {
		return r.inner.Chat(ctx, req, apiKey, baseURL)
	}
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	return r.inner.Chat(ctx, req, apiKey, baseURL)
}

// StreamChat implements StreamingProvider. It delegates directly to
// the inner provider. Streaming does not support retry because
// once chunks start flowing, restarting would break the client's
// SSE stream. Timeout is applied via the context.
func (r *RetryingProvider) StreamChat(ctx context.Context, req *ChatRequest, apiKey, baseURL string) (<-chan StreamEvent, error) {
	sp, ok := r.inner.(StreamingProvider)
	if !ok {
		return nil, ErrStreamUnsupported
	}
	if r.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.config.Timeout)
		// Note: cancel is intentionally not deferred — the caller
		// reads from the channel until it closes, which signals
		// completion. The context will be garbage-collected when
		// the channel consumer returns.
		_ = cancel
	}
	return sp.StreamChat(ctx, req, apiKey, baseURL)
}

// Embeddings implements EmbeddingsProvider by delegating to the inner
// provider if it supports it. Retries are applied the same as Chat.
func (r *RetryingProvider) Embeddings(ctx context.Context, req *EmbeddingsRequest, apiKey, baseURL string) (*EmbeddingsResponse, int, error) {
	ep, ok := r.inner.(EmbeddingsProvider)
	if !ok {
		return nil, 0, fmt.Errorf("embeddings not supported by inner provider")
	}
	if r.config.MaxRetries <= 0 {
		return r.embeddingsWithTimeout(ctx, req, apiKey, baseURL)
	}
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		resp, status, err := r.embeddingsWithTimeout(ctx, req, apiKey, baseURL)
		if (err == nil && status < 500) || !isRetryable(status, err) {
			return resp, status, err
		}
		lastErr = err
		lastStatus = status
		if attempt < r.config.MaxRetries {
			delay := r.retryDelay(status, err, attempt)
			if delay > 0 {
				observability.RecordRetry(req.Model)
				log.Printf("[retry] embeddings attempt %d/%d failed (status=%d), retrying in %v: %v",
					attempt+1, r.config.MaxRetries, status, delay, err)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, lastStatus, ctx.Err()
				}
			}
		}
	}
	_ = ep // suppress unused variable
	return nil, lastStatus, lastErr
}

func (r *RetryingProvider) embeddingsWithTimeout(ctx context.Context, req *EmbeddingsRequest, apiKey, baseURL string) (*EmbeddingsResponse, int, error) {
	ep, ok := r.inner.(EmbeddingsProvider)
	if !ok {
		return nil, 0, fmt.Errorf("embeddings not supported")
	}
	if r.config.Timeout <= 0 {
		return ep.Embeddings(ctx, req, apiKey, baseURL)
	}
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	return ep.Embeddings(ctx, req, apiKey, baseURL)
}

// retryDelay calculates the delay before the next retry attempt.
// Returns 0 for non-retryable errors. For 429, attempts to read
// Retry-After header from the error message.
func (r *RetryingProvider) retryDelay(status int, err error, attempt int) time.Duration {
	if !isRetryable(status, err) {
		return 0
	}

	// For 429, try to extract Retry-After from the error message.
	if status == http.StatusTooManyRequests {
		if delay := parseRetryAfter(err); delay > 0 {
			return delay
		}
	}

	// Exponential backoff: base * 2^attempt
	delay := time.Duration(float64(r.config.BaseDelay) * math.Pow(2, float64(attempt)))
	// Cap at 30s
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

// isRetryable reports whether the error/status combination should
// trigger a retry. A non-retryable HTTP status (4xx except 429)
// always takes precedence over the error value — even if the
// upstream returned an error alongside a 400, we don't retry.
func isRetryable(status int, err error) bool {
	// Explicit non-retryable HTTP status takes precedence.
	if status > 0 && status != http.StatusTooManyRequests && status < 500 {
		return false
	}
	if err != nil {
		// Context cancellation or deadline exceeded — don't retry.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		return true // connection errors, etc. are retryable
	}
	// 5xx server errors are retryable; 429 rate limit is retryable.
	if status >= 500 || status == http.StatusTooManyRequests {
		return true
	}
	return false
}

// parseRetryAfter attempts to extract a Retry-After duration from
// the error message. The upstream provider adapters embed HTTP
// status codes and response bodies in their error strings.
func parseRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	msg := err.Error()
	// Look for "Retry-After: N" or "retry-after: N" in the message.
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, "retry-after:")
	if idx < 0 {
		return 0
	}
	rest := strings.TrimSpace(msg[idx+len("retry-after:"):])
	// The value might be "1234567890\n..." — take the first token.
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0
	}
	secs, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second // cap at 1 minute
	}
	return d
}
