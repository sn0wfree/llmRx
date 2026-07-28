package api_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAudioTranscription_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"whisper-1"}, "sk-key")
	app.AddToken("sk-t", "t")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("FAKE-AUDIO-DATA"))
	mw.WriteField("model", "whisper-1")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp provider.AudioTranscriptionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("expected non-empty text")
	}
}

func TestAudioTranscription_DefaultModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"whisper-1"}, "sk-key")
	app.AddToken("sk-t", "t")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "audio.mp3")
	fw.Write([]byte("x"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAudioTranscription_InvalidMultipart(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"whisper-1"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("not multipart"))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAudioTranscription_MissingFile(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"whisper-1"}, "sk-key")
	app.AddToken("sk-t", "t")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "whisper-1")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing_file") {
		t.Fatalf("expected missing_file, got %s", rec.Body.String())
	}
}

func TestAudioTranscription_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"whisper-1"}, "sk-key")
	app.AddToken("sk-t", "t")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "audio.mp3")
	fw.Write([]byte("x"))
	mw.WriteField("model", "unknown-model")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAudioTranscription_ModelWhitelist(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"allowed"}, "sk-key")
	tok := app.AddToken("sk-t", "t")
	tok.ModelsWhitelist = []string{"allowed"}
	app.Store.UpdateToken(tok)
	app.Cache.Reload()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "audio.mp3")
	fw.Write([]byte("x"))
	mw.WriteField("model", "forbidden")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
