package scenarios

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestScenario_ImageGenerations(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"dall-e-3"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	body, _ := json.Marshal(map[string]interface{}{
		"model":  "dall-e-3",
		"prompt": "a cat on a chair",
		"n":      1,
		"size":   "1024x1024",
	})
	req := httptest.NewRequest("POST", "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(data))
	}
}

func TestScenario_AudioSpeech(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"tts-1"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	body, _ := json.Marshal(map[string]interface{}{
		"model": "tts-1",
		"input": "hello world",
		"voice": "alloy",
	})
	req := httptest.NewRequest("POST", "/v1/audio/speech", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("FAKE-MP3-DATA-hello world")) {
		t.Fatal("audio payload missing")
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/mp3" {
		t.Errorf("Content-Type: got %s, want audio/mp3", ct)
	}
}

func TestScenario_AudioTranscription(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"whisper-1"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	// Build multipart body manually.
	var body bytes.Buffer
	body.WriteString("--BOUNDARY\r\n")
	body.WriteString("Content-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n")
	body.WriteString("--BOUNDARY\r\n")
	body.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"audio.mp3\"\r\n")
	body.WriteString("Content-Type: audio/mpeg\r\n\r\n")
	body.WriteString("FAKE-AUDIO")
	body.WriteString("\r\n--BOUNDARY--\r\n")

	req := httptest.NewRequest("POST", "/v1/audio/transcriptions",
		bytes.NewReader(body.Bytes()))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=BOUNDARY")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if text, _ := resp["text"].(string); text != "transcribed: FAKE-AUDIO" {
		t.Errorf("text: got %q, want transcribed: FAKE-AUDIO", text)
	}
}

func TestScenario_Rerank(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"rerank-english-v3.0"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	body, _ := json.Marshal(map[string]interface{}{
		"model":     "rerank-english-v3.0",
		"query":     "what is the capital of france",
		"documents": []string{"Paris is the capital of France", "London is the capital of England", "Berlin is the capital of Germany"},
	})
	req := httptest.NewRequest("POST", "/v1/rerank", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-tok-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	results, _ := resp["results"].([]interface{})
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestScenario_ImageGenerations_NoAuth(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"dall-e-3"}, "sk-key-1")

	body := `{"model":"dall-e-3","prompt":"x"}`
	req := httptest.NewRequest("POST", "/v1/images/generations", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Fatalf("status=%d, want 401", w.Code)
	}
}