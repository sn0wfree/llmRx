package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mockProvider struct {
	responses []mockResponse
	callCount int
}

type mockResponse struct {
	resp   *ChatResponse
	status int
	err    error
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Chat(_ context.Context, _ *ChatRequest, _, _ string) (*ChatResponse, int, error) {
	if m.callCount >= len(m.responses) {
		return nil, 0, errors.New("no more responses")
	}
	r := m.responses[m.callCount]
	m.callCount++
	return r.resp, r.status, r.err
}

func TestRetryingProvider_NoRetry(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{resp: &ChatResponse{}, status: 200},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{MaxRetries: 0, Timeout: 5 * time.Second})
	resp, status, err := rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	if err != nil || status != 200 || resp == nil {
		t.Errorf("expected 200 OK, got status=%d err=%v", status, err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}
}

func TestRetryingProvider_RetryOn5xx(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 500, err: errors.New("server error")},
		{status: 200, resp: &ChatResponse{}},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
		Timeout:    5 * time.Second,
	})
	resp, status, err := rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	if err != nil || status != 200 || resp == nil {
		t.Errorf("expected 200 OK after retry, got status=%d err=%v", status, err)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls, got %d", mock.callCount)
	}
}

func TestRetryingProvider_NoRetryOn4xx(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 400, err: errors.New("bad request")},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		Timeout:    5 * time.Second,
	})
	_, status, err := rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	if err == nil {
		t.Error("expected error for 4xx")
	}
	if status != 400 {
		t.Errorf("expected 400, got %d", status)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", mock.callCount)
	}
}

func TestRetryingProvider_RetryOn429(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 429, err: errors.New("rate limited")},
		{status: 200, resp: &ChatResponse{}},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
		Timeout:    5 * time.Second,
	})
	_, status, err := rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	if err != nil || status != 200 {
		t.Errorf("expected 200 after 429 retry, got status=%d err=%v", status, err)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls, got %d", mock.callCount)
	}
}

func TestRetryingProvider_ExhaustsRetries(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 500, err: errors.New("error 1")},
		{status: 500, err: errors.New("error 2")},
		{status: 500, err: errors.New("error 3")},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
		Timeout:    5 * time.Second,
	})
	_, status, err := rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	if err == nil {
		t.Error("expected error after exhausting retries")
	}
	if status != 500 {
		t.Errorf("expected 500, got %d", status)
	}
	if mock.callCount != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", mock.callCount)
	}
}

func TestRetryingProvider_ContextCanceled(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 500, err: errors.New("error")},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		Timeout:    5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, _, err := rp.Chat(ctx, &ChatRequest{}, "key", "url")
	if err == nil {
		t.Error("expected error for canceled context")
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call (no retry on cancel), got %d", mock.callCount)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		status int
		err    error
		want   bool
	}{
		{200, nil, false},
		{400, nil, false},
		{401, nil, false},
		{429, nil, true},
		{500, nil, true},
		{503, nil, true},
		{0, errors.New("connection reset"), true},
		{0, context.Canceled, false},
		{0, context.DeadlineExceeded, false},
	}
	for _, tt := range tests {
		got := isRetryable(tt.status, tt.err)
		if got != tt.want {
			t.Errorf("isRetryable(%d, %v) = %v, want %v", tt.status, tt.err, got, tt.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		err  error
		want time.Duration
	}{
		{errors.New("Retry-After: 5"), 5 * time.Second},
		{errors.New("retry-after: 120"), 60 * time.Second}, // capped
		{errors.New("no retry header"), 0},
		{nil, 0},
	}
	for _, tt := range tests {
		got := parseRetryAfter(tt.err)
		if got != tt.want {
			t.Errorf("parseRetryAfter(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestRetryingProvider_ExponentialBackoff(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 500, err: errors.New("error")},
		{status: 500, err: errors.New("error")},
		{status: 200, resp: &ChatResponse{}},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 3,
		BaseDelay:  50 * time.Millisecond,
		Timeout:    5 * time.Second,
	})
	start := time.Now()
	_, _, _ = rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	elapsed := time.Since(start)
	// 2 retries: 50ms + 100ms = 150ms minimum
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected at least 100ms backoff, got %v", elapsed)
	}
	if mock.callCount != 3 {
		t.Errorf("expected 3 calls, got %d", mock.callCount)
	}
}

func TestRetryingProvider_Timeout(t *testing.T) {
	mock := &mockProvider{responses: []mockResponse{
		{status: 200, resp: &ChatResponse{}},
	}}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 0,
		BaseDelay:  100 * time.Millisecond,
		Timeout:    1 * time.Millisecond,
	})
	// Even with 1ms timeout, a mock that returns immediately should work
	_, status, err := rp.Chat(context.Background(), &ChatRequest{}, "key", "url")
	if err != nil || status != 200 {
		t.Errorf("expected immediate 200, got status=%d err=%v", status, err)
	}
}

func TestRetryDelay_CapsAt30s(t *testing.T) {
	rp := NewRetryingProvider(&mockProvider{}, RetryConfig{
		MaxRetries: 10,
		BaseDelay:  1 * time.Second,
	})
	// Attempt 5: 1s * 2^5 = 32s, should be capped at 30s
	delay := rp.retryDelay(500, nil, 5)
	if delay != 30*time.Second {
		t.Errorf("expected 30s cap, got %v", delay)
	}
}

func TestRetryDelay_NonRetryableReturns0(t *testing.T) {
	rp := NewRetryingProvider(&mockProvider{}, RetryConfig{})
	delay := rp.retryDelay(400, nil, 0)
	if delay != 0 {
		t.Errorf("expected 0 for non-retryable, got %v", delay)
	}
}
