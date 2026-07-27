package observability

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Initialize global once using Init() (idempotent)
	Init()
	os.Exit(m.Run())
}

// --- httpStatusClass ---

func TestHttpStatusClass(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{100, "other"},
		{199, "other"},
		{200, "2xx"},
		{201, "2xx"},
		{299, "2xx"},
		{300, "3xx"},
		{301, "3xx"},
		{399, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{499, "4xx"},
		{500, "5xx"},
		{503, "5xx"},
		{599, "5xx"},
		{999, "5xx"},
		{0, "other"},
	}
	for _, tc := range tests {
		got := httpStatusClass(tc.code)
		if got != tc.want {
			t.Errorf("httpStatusClass(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// --- Nil global safety (all instrument functions should not panic) ---

func TestRecordRequest_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	RecordRequest("test", 100, false, 0.01, 10, 20, false)
}

func TestRecordUpstreamError_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	RecordUpstreamError("test", 500)
}

func TestRecordRateLimitBlock_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	RecordRateLimitBlock("rpm")
}

func TestRecordRetry_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	RecordRetry("test")
}

func TestStreamStart_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	StreamStart()
}

func TestStreamEnd_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	StreamEnd()
}

func TestSetChannelsEnabled_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	SetChannelsEnabled(5)
}

func TestSetTokensActive_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	SetTokensActive(10)
}

// --- Handler ---

func TestHandler_NilGlobal(t *testing.T) {
	saved := global
	global = nil
	defer func() { global = saved }()
	h := Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not initialized") {
		t.Fatalf("body: %q", w.Body.String())
	}
}

func TestHandler_Initialized(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "not initialized") {
		t.Fatal("should not contain 'not initialized' when global is set")
	}
	if len(body) == 0 {
		t.Fatal("body should not be empty")
	}
}

// --- Init ---

func TestInit_NoOpWhenSet(t *testing.T) {
	// Init() is already called by TestMain, so global is set.
	// Verify it's non-nil and calling Init again doesn't panic.
	if global == nil {
		t.Fatal("global should be set by TestMain")
	}
	m := global
	Init() // second call should be a no-op
	if global != m {
		t.Fatal("Init should not overwrite existing global")
	}
}

// --- RecordRequest with initialized global ---

func TestRecordRequest_WithGlobal(t *testing.T) {
	RecordRequest("gpt-4o", 1500, false, 0.05, 100, 50, true)
	RecordRequest("gpt-4o", 200, true, 0.0, 10, 0, false)
}

func TestRecordUpstreamError_WithGlobal(t *testing.T) {
	RecordUpstreamError("claude-3", 429)
	RecordUpstreamError("claude-3", 500)
	RecordUpstreamError("claude-3", 503)
}

func TestRecordRateLimitBlock_WithGlobal(t *testing.T) {
	RecordRateLimitBlock("rpm")
	RecordRateLimitBlock("tpm")
	RecordRateLimitBlock("budget")
}

func TestRecordRetry_WithGlobal(t *testing.T) {
	RecordRetry("gpt-4o")
	RecordRetry("gpt-4o")
}

func TestStreamLifecycle(t *testing.T) {
	StreamStart()
	StreamStart()
	StreamStart()
	StreamEnd()
}

func TestSetGauges(t *testing.T) {
	SetChannelsEnabled(15)
	SetTokensActive(42)
}

// --- Benchmark ---

func BenchmarkRecordRequest(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RecordRequest("bench", 100, false, 0.001, 10, 5, false)
	}
}

func BenchmarkHttpStatusClass(b *testing.B) {
	codes := []int{100, 200, 301, 404, 500, 503}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		httpStatusClass(codes[i%len(codes)])
	}
}

func BenchmarkStreamStartEnd(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StreamStart()
		StreamEnd()
	}
}
