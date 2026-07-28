package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newOpenAIServer returns an httptest server that responds with the
// given JSON body on the expected path. It asserts that the request
// uses POST + application/json + Bearer authorization.
func newOpenAIServer(t *testing.T, path, jsonBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != path {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(jsonBody))
	}))
}

// newOpenAIErrorServer returns an httptest server that responds
// with the given HTTP status and body.
func newOpenAIErrorServer(t *testing.T, path string, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

// newOpenAIBinaryServer returns an httptest server that responds
// with raw bytes (for audio endpoints).
func newOpenAIBinaryServer(t *testing.T, path, contentType string, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
}

// ──────────────────────────────────────────────────────────
// OpenAIProvider 5 capability endpoints
// ──────────────────────────────────────────────────────────

func TestOpenAIProvider_Embeddings(t *testing.T) {
	srv := newOpenAIServer(t, "/embeddings", `{
		"object": "list",
		"data": [
			{"object": "embedding", "embedding": [0.1, 0.2, 0.3], "index": 0},
			{"object": "embedding", "embedding": [0.4, 0.5, 0.6], "index": 1}
		],
		"model": "text-embedding-3-small",
		"usage": {"prompt_tokens": 5, "completion_tokens": 0, "total_tokens": 5}
	}`)
	defer srv.Close()

	p := NewOpenAIProvider()
	resp, code, err := p.Embeddings(context.Background(),
		&EmbeddingsRequest{Model: "text-embedding-3-small", Input: "hello"},
		"sk-test", srv.URL)
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
	}
	if resp.Data[0].Embedding[0] != 0.1 || resp.Data[1].Index != 1 {
		t.Errorf("embedding data: %+v", resp.Data)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestOpenAIProvider_Embeddings_UpstreamError(t *testing.T) {
	srv := newOpenAIErrorServer(t, "/embeddings", http.StatusUnauthorized, `{"error":"invalid api key"}`)
	defer srv.Close()

	p := NewOpenAIProvider()
	_, code, err := p.Embeddings(context.Background(),
		&EmbeddingsRequest{Model: "m", Input: "x"},
		"bad", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestOpenAIProvider_Images(t *testing.T) {
	srv := newOpenAIServer(t, "/images/generations", `{
		"created": 1700000000,
		"data": [
			{"url": "https://example.com/img1.png"},
			{"b64_json": "iVBORw0KGgo="}
		]
	}`)
	defer srv.Close()

	p := NewOpenAIProvider()
	resp, code, err := p.Images(context.Background(),
		&ImagesRequest{Model: "dall-e-3", Prompt: "a cat", N: IntPtr(2)},
		"sk", srv.URL)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 images, got %d", len(resp.Data))
	}
	if resp.Data[0].URL != "https://example.com/img1.png" {
		t.Errorf("first image URL: %q", resp.Data[0].URL)
	}
	if resp.Data[1].B64JSON != "iVBORw0KGgo=" {
		t.Errorf("second image b64: %q", resp.Data[1].B64JSON)
	}
}

func TestOpenAIProvider_Images_BadRequest(t *testing.T) {
	srv := newOpenAIErrorServer(t, "/images/generations", http.StatusBadRequest, `{"error":"bad prompt"}`)
	defer srv.Close()

	p := NewOpenAIProvider()
	_, code, err := p.Images(context.Background(),
		&ImagesRequest{Model: "dall-e-3", Prompt: "bad"},
		"sk", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", code)
	}
}

func TestOpenAIProvider_Speech(t *testing.T) {
	audioBytes := []byte{0xFF, 0xFB, 0x90, 0x00, 0x00} // fake MP3 header
	srv := newOpenAIBinaryServer(t, "/audio/speech", "audio/mpeg", audioBytes)
	defer srv.Close()

	p := NewOpenAIProvider()
	resp, code, err := p.Speech(context.Background(),
		&AudioSpeechRequest{Model: "tts-1", Input: "Hello world", Voice: "alloy"},
		"sk", srv.URL)
	if err != nil {
		t.Fatalf("Speech: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if len(resp) != len(audioBytes) {
		t.Fatalf("expected %d bytes, got %d", len(audioBytes), len(resp))
	}
	for i, b := range resp {
		if b != audioBytes[i] {
			t.Fatalf("byte mismatch at %d: got %d, want %d", i, b, audioBytes[i])
		}
	}
}

func TestOpenAIProvider_Speech_UpstreamError(t *testing.T) {
	srv := newOpenAIErrorServer(t, "/audio/speech", http.StatusInternalServerError, "boom")
	defer srv.Close()

	p := NewOpenAIProvider()
	_, code, err := p.Speech(context.Background(),
		&AudioSpeechRequest{Model: "tts-1", Input: "x", Voice: "alloy"},
		"sk", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", code)
	}
}

func TestOpenAIProvider_Transcription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			http.Error(w, "bad path", 404)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart, got %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if r.FormValue("model") != "whisper-1" {
			t.Errorf("model field: %q", r.FormValue("model"))
		}
		if r.FormValue("language") != "en" {
			t.Errorf("language field: %q", r.FormValue("language"))
		}
		if r.FormValue("response_format") != "json" {
			t.Errorf("format field: %q", r.FormValue("response_format"))
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		if !strings.HasSuffix(header.Filename, ".wav") {
			t.Errorf("file extension: %q", header.Filename)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"hello transcribed","language":"en","duration":1.5}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider()
	audioData := []byte{0x52, 0x49, 0x46, 0x46} // WAV header bytes
	resp, code, err := p.Transcription(context.Background(),
		&AudioTranscriptionRequest{Model: "whisper-1", Language: "en", Format: "json"},
		audioData, "audio/wav", "sk", srv.URL)
	if err != nil {
		t.Fatalf("Transcription: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if resp.Text != "hello transcribed" {
		t.Errorf("text: %q", resp.Text)
	}
	if resp.Language != "en" || resp.Duration != 1.5 {
		t.Errorf("metadata: lang=%q duration=%f", resp.Language, resp.Duration)
	}
}

func TestOpenAIProvider_Transcription_MP3Extension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(10 << 20)
		_, header, _ := r.FormFile("file")
		if !strings.HasSuffix(header.Filename, ".mp3") {
			t.Errorf("expected .mp3 extension, got %q", header.Filename)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"x"}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider()
	_, _, err := p.Transcription(context.Background(),
		&AudioTranscriptionRequest{Model: "whisper-1"},
		[]byte{0xFF, 0xFB}, "audio/mpeg", "sk", srv.URL)
	if err != nil {
		t.Fatalf("Transcription: %v", err)
	}
}

func TestOpenAIProvider_Rerank(t *testing.T) {
	srv := newOpenAIServer(t, "/rerank", `{
		"model": "rerank-english-v2.0",
		"results": [
			{"index": 2, "relevance_score": 0.95, "document": "doc2 text"},
			{"index": 0, "relevance_score": 0.78, "document": "doc0 text"}
		],
		"usage": {"prompt_tokens": 10, "completion_tokens": 0, "total_tokens": 10}
	}`)
	defer srv.Close()

	p := NewOpenAIProvider()
	resp, code, err := p.Rerank(context.Background(),
		&RerankRequest{Model: "rerank-english-v2.0", Query: "what is X?", Documents: []string{"doc0", "doc1", "doc2"}},
		"sk", srv.URL)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 2 || resp.Results[0].RelevanceScore != 0.95 {
		t.Errorf("first result: %+v", resp.Results[0])
	}
	if resp.Results[0].Document == nil || *resp.Results[0].Document != "doc2 text" {
		t.Errorf("first document: %v", resp.Results[0].Document)
	}
}

func TestOpenAIProvider_Rerank_UpstreamError(t *testing.T) {
	srv := newOpenAIErrorServer(t, "/rerank", http.StatusInternalServerError, "boom")
	defer srv.Close()

	p := NewOpenAIProvider()
	_, code, err := p.Rerank(context.Background(),
		&RerankRequest{Model: "r", Query: "q", Documents: []string{"d"}},
		"sk", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", code)
	}
}

func TestExtFromMime(t *testing.T) {
	cases := map[string]string{
		"audio/mpeg":   "mp3",
		"audio/mp3":    "mp3",
		"audio/wav":    "wav",
		"audio/x-wav":  "wav",
		"audio/ogg":    "ogg",
		"audio/webm":   "webm",
		"audio/flac":   "flac",
		"audio/m4a":    "m4a",
		"audio/x-m4a":  "m4a",
		"audio/mp4":    "m4a",
		"unknown/type": "bin",
		"":             "bin",
	}
	for mime, want := range cases {
		if got := extFromMime(mime); got != want {
			t.Errorf("extFromMime(%q) = %q, want %q", mime, got, want)
		}
	}
}

// ──────────────────────────────────────────────────────────
// RetryingProvider delegation methods
// ──────────────────────────────────────────────────────────

// capabilityMock is a mock provider that implements multiple
// capability interfaces so we can exercise RetryingProvider
// delegation paths.
type capabilityMock struct {
	name string

	embeddingsResp   *EmbeddingsResponse
	embeddingsStatus int
	embeddingsErr    error
	embeddingsCalls  int

	imagesResp   *ImagesResponse
	imagesStatus int
	imagesErr    error
	imagesCalls  int

	speechResp   []byte
	speechStatus int
	speechErr    error
	speechCalls  int

	transcriptionResp   *AudioTranscriptionResponse
	transcriptionStatus int
	transcriptionErr    error
	transcriptionCalls  int

	rerankResp   *RerankResponse
	rerankStatus int
	rerankErr    error
	rerankCalls  int

	modelsList []string
	modelsErr  error
	modelsCalls int
}

func (m *capabilityMock) Name() string { return m.name }

func (m *capabilityMock) Chat(_ context.Context, _ *ChatRequest, _, _ string) (*ChatResponse, int, error) {
	return &ChatResponse{}, 200, nil
}

func (m *capabilityMock) Embeddings(_ context.Context, _ *EmbeddingsRequest, _, _ string) (*EmbeddingsResponse, int, error) {
	m.embeddingsCalls++
	return m.embeddingsResp, m.embeddingsStatus, m.embeddingsErr
}

func (m *capabilityMock) Images(_ context.Context, _ *ImagesRequest, _, _ string) (*ImagesResponse, int, error) {
	m.imagesCalls++
	return m.imagesResp, m.imagesStatus, m.imagesErr
}

func (m *capabilityMock) Speech(_ context.Context, _ *AudioSpeechRequest, _, _ string) ([]byte, int, error) {
	m.speechCalls++
	return m.speechResp, m.speechStatus, m.speechErr
}

func (m *capabilityMock) Transcription(_ context.Context, _ *AudioTranscriptionRequest, _ []byte, _, _, _ string) (*AudioTranscriptionResponse, int, error) {
	m.transcriptionCalls++
	return m.transcriptionResp, m.transcriptionStatus, m.transcriptionErr
}

func (m *capabilityMock) Rerank(_ context.Context, _ *RerankRequest, _, _ string) (*RerankResponse, int, error) {
	m.rerankCalls++
	return m.rerankResp, m.rerankStatus, m.rerankErr
}

func (m *capabilityMock) ListModels(_ context.Context, _, _ string) ([]string, error) {
	m.modelsCalls++
	return m.modelsList, m.modelsErr
}

func TestRetryDefaults(t *testing.T) {
	d := RetryDefaults()
	if d.MaxRetries != 0 {
		t.Errorf("MaxRetries: got %d, want 0", d.MaxRetries)
	}
	if d.BaseDelay != 500*time.Millisecond {
		t.Errorf("BaseDelay: got %v, want 500ms", d.BaseDelay)
	}
	if d.Timeout != 60*time.Second {
		t.Errorf("Timeout: got %v, want 60s", d.Timeout)
	}
}

func TestRetryingProvider_Name(t *testing.T) {
	mock := &capabilityMock{name: "custom-provider"}
	rp := NewRetryingProvider(mock, RetryConfig{})
	if got := rp.Name(); got != "custom-provider" {
		t.Errorf("Name: got %q, want %q", got, "custom-provider")
	}
}

func TestRetryingProvider_Embeddings_Success(t *testing.T) {
	mock := &capabilityMock{
		embeddingsResp:   &EmbeddingsResponse{Model: "m"},
		embeddingsStatus: 200,
	}
	rp := NewRetryingProvider(mock, RetryConfig{MaxRetries: 0})
	resp, code, err := rp.Embeddings(context.Background(),
		&EmbeddingsRequest{Model: "m"}, "key", "url")
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if code != 200 || resp.Model != "m" {
		t.Errorf("got code=%d model=%q", code, resp.Model)
	}
	if mock.embeddingsCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.embeddingsCalls)
	}
}

func TestRetryingProvider_Embeddings_RetryOn5xx(t *testing.T) {
	mock := &retryingEmbeddingsMock{
		responses: []mockEmbeddingsResponse{
			{status: 500, err: errors.New("server error")},
			{status: 200, resp: &EmbeddingsResponse{Model: "m"}},
		},
	}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
	})
	resp, code, err := rp.Embeddings(context.Background(),
		&EmbeddingsRequest{Model: "m"}, "key", "url")
	if err != nil || code != 200 {
		t.Fatalf("expected 200 after retry, got code=%d err=%v", code, err)
	}
	if resp.Model != "m" {
		t.Errorf("model: %q", resp.Model)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls, got %d", mock.callCount)
	}
}

// retryingEmbeddingsMock is a tiny mock that returns different
// responses on successive calls, for retry-loop tests.
type retryingEmbeddingsMock struct {
	responses []mockEmbeddingsResponse
	callCount int
}

type mockEmbeddingsResponse struct {
	resp   *EmbeddingsResponse
	status int
	err    error
}

func (r *retryingEmbeddingsMock) Name() string { return "retry-mock" }
func (r *retryingEmbeddingsMock) Chat(_ context.Context, _ *ChatRequest, _, _ string) (*ChatResponse, int, error) {
	return nil, 0, errors.New("not used")
}
func (r *retryingEmbeddingsMock) Embeddings(_ context.Context, _ *EmbeddingsRequest, _, _ string) (*EmbeddingsResponse, int, error) {
	if r.callCount >= len(r.responses) {
		return nil, 0, errors.New("no more responses")
	}
	resp := r.responses[r.callCount]
	r.callCount++
	return resp.resp, resp.status, resp.err
}

func TestRetryingProvider_Embeddings_NotSupported(t *testing.T) {
	// Inner provider doesn't implement EmbeddingsProvider.
	inner := &mockProvider{}
	rp := NewRetryingProvider(inner, RetryConfig{MaxRetries: 0})
	_, _, err := rp.Embeddings(context.Background(),
		&EmbeddingsRequest{Model: "m"}, "key", "url")
	if err == nil {
		t.Fatal("expected error for unsupported embeddings")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("err: %v", err)
	}
}

func TestRetryingProvider_Embeddings_NoRetryOn4xx(t *testing.T) {
	mock := &retryingEmbeddingsMock{
		responses: []mockEmbeddingsResponse{
			{status: 400, err: errors.New("bad request")},
		},
	}
	rp := NewRetryingProvider(mock, RetryConfig{
		MaxRetries: 3,
		BaseDelay:  5 * time.Millisecond,
	})
	_, _, err := rp.Embeddings(context.Background(),
		&EmbeddingsRequest{Model: "m"}, "key", "url")
	if err == nil {
		t.Fatal("expected error")
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", mock.callCount)
	}
}

func TestRetryingProvider_StreamChat_NoStreamSupport(t *testing.T) {
	// mockProvider doesn't implement StreamingProvider.
	inner := &mockProvider{}
	rp := NewRetryingProvider(inner, RetryConfig{})
	_, err := rp.StreamChat(context.Background(), &ChatRequest{Stream: true}, "key", "url")
	if err == nil {
		t.Fatal("expected ErrStreamUnsupported")
	}
	if !errors.Is(err, ErrStreamUnsupported) {
		t.Errorf("err: %v", err)
	}
}

func TestRetryingProvider_StreamChat_Delegates(t *testing.T) {
	mock := &streamingMockProvider{}
	rp := NewRetryingProvider(mock, RetryConfig{Timeout: 1 * time.Second})
	ch, err := rp.StreamChat(context.Background(),
		&ChatRequest{Model: "m", Stream: true}, "key", "url")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	// Drain a couple of events.
	for i := 0; i < 2; i++ {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
}

// streamingMockProvider implements StreamingProvider.
type streamingMockProvider struct {
	chatMock
}

func (s *streamingMockProvider) StreamChat(ctx context.Context, _ *ChatRequest, _, _ string) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 2)
	go func() {
		defer close(out)
		out <- StreamEvent{Chunk: StreamChunk{ID: "1", Model: "m"}}
		out <- StreamEvent{Chunk: StreamChunk{ID: "2", Model: "m"}}
	}()
	return out, nil
}

// chatMock provides Chat() for non-streaming tests.
type chatMock struct{}

func (chatMock) Name() string { return "chat" }
func (chatMock) Chat(_ context.Context, _ *ChatRequest, _, _ string) (*ChatResponse, int, error) {
	return &ChatResponse{}, 200, nil
}

func TestRetryingProvider_ListModels_Delegates(t *testing.T) {
	mock := &capabilityMock{
		modelsList: []string{"gpt-4o", "gpt-3.5-turbo"},
	}
	rp := NewRetryingProvider(mock, RetryConfig{})
	models, err := rp.ListModels(context.Background(), "key", "url")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0] != "gpt-4o" {
		t.Errorf("first model: %q", models[0])
	}
	if mock.modelsCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.modelsCalls)
	}
}

func TestRetryingProvider_ListModels_NotSupported(t *testing.T) {
	inner := &mockProvider{} // does not implement ModelLister
	rp := NewRetryingProvider(inner, RetryConfig{})
	_, err := rp.ListModels(context.Background(), "key", "url")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryingProvider_Images_Delegates(t *testing.T) {
	mock := &capabilityMock{
		imagesResp:   &ImagesResponse{Created: 123},
		imagesStatus: 200,
	}
	rp := NewRetryingProvider(mock, RetryConfig{})
	resp, code, err := rp.Images(context.Background(),
		&ImagesRequest{Model: "dalle"}, "key", "url")
	if err != nil || code != 200 {
		t.Fatalf("Images: code=%d err=%v", code, err)
	}
	if resp.Created != 123 {
		t.Errorf("created: %d", resp.Created)
	}
	if mock.imagesCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.imagesCalls)
	}
}

func TestRetryingProvider_Images_NotSupported(t *testing.T) {
	inner := &mockProvider{}
	rp := NewRetryingProvider(inner, RetryConfig{})
	_, _, err := rp.Images(context.Background(), &ImagesRequest{}, "key", "url")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryingProvider_Speech_Delegates(t *testing.T) {
	audio := []byte{0x01, 0x02, 0x03}
	mock := &capabilityMock{
		speechResp:   audio,
		speechStatus: 200,
	}
	rp := NewRetryingProvider(mock, RetryConfig{})
	resp, code, err := rp.Speech(context.Background(),
		&AudioSpeechRequest{Model: "tts-1"}, "key", "url")
	if err != nil || code != 200 {
		t.Fatalf("Speech: code=%d err=%v", code, err)
	}
	if len(resp) != 3 {
		t.Errorf("audio bytes: %d", len(resp))
	}
	if mock.speechCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.speechCalls)
	}
}

func TestRetryingProvider_Speech_NotSupported(t *testing.T) {
	inner := &mockProvider{}
	rp := NewRetryingProvider(inner, RetryConfig{})
	_, _, err := rp.Speech(context.Background(), &AudioSpeechRequest{}, "key", "url")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryingProvider_Transcription_Delegates(t *testing.T) {
	mock := &capabilityMock{
		transcriptionResp:   &AudioTranscriptionResponse{Text: "hello"},
		transcriptionStatus: 200,
	}
	rp := NewRetryingProvider(mock, RetryConfig{})
	resp, code, err := rp.Transcription(context.Background(),
		&AudioTranscriptionRequest{Model: "whisper-1"},
		[]byte{0x01}, "audio/wav", "key", "url")
	if err != nil || code != 200 {
		t.Fatalf("Transcription: code=%d err=%v", code, err)
	}
	if resp.Text != "hello" {
		t.Errorf("text: %q", resp.Text)
	}
	if mock.transcriptionCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.transcriptionCalls)
	}
}

func TestRetryingProvider_Transcription_NotSupported(t *testing.T) {
	inner := &mockProvider{}
	rp := NewRetryingProvider(inner, RetryConfig{})
	_, _, err := rp.Transcription(context.Background(),
		&AudioTranscriptionRequest{}, []byte{}, "audio/wav", "key", "url")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRetryingProvider_Rerank_Delegates(t *testing.T) {
	mock := &capabilityMock{
		rerankResp:   &RerankResponse{Model: "r"},
		rerankStatus: 200,
	}
	rp := NewRetryingProvider(mock, RetryConfig{})
	resp, code, err := rp.Rerank(context.Background(),
		&RerankRequest{Model: "r", Query: "q"}, "key", "url")
	if err != nil || code != 200 {
		t.Fatalf("Rerank: code=%d err=%v", code, err)
	}
	if resp.Model != "r" {
		t.Errorf("model: %q", resp.Model)
	}
	if mock.rerankCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.rerankCalls)
	}
}

func TestRetryingProvider_Rerank_NotSupported(t *testing.T) {
	inner := &mockProvider{}
	rp := NewRetryingProvider(inner, RetryConfig{})
	_, _, err := rp.Rerank(context.Background(),
		&RerankRequest{}, "key", "url")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewRetryingProvider_AppliesDefaults(t *testing.T) {
	mock := &capabilityMock{}
	rp := NewRetryingProvider(mock, RetryConfig{})
	if rp.config.BaseDelay != 500*time.Millisecond {
		t.Errorf("BaseDelay default: got %v", rp.config.BaseDelay)
	}
	if rp.config.Timeout != 60*time.Second {
		t.Errorf("Timeout default: got %v", rp.config.Timeout)
	}
}