package webui

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestNewRenderer_Ok(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	if r == nil {
		t.Fatal("nil renderer")
	}
}

func TestRenderer_RenderEmpty(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	var buf bytes.Buffer
	if err := r.Render(&buf, "nonexistent", nil); err == nil {
		t.Error("Render of nonexistent template should error")
	}
}

func TestRenderer_RenderPartialEmpty(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	var buf bytes.Buffer
	if err := r.RenderPartial(&buf, "missing.html", nil); err == nil {
		t.Error("RenderPartial of missing should error")
	}
}

func TestRenderer_Flash_Success(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	r.Flash(w, "success", "ok msg")
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	if !strings.Contains(w.Body.String(), "green-100") {
		t.Errorf("body = %q, want green-100 color", w.Body.String())
	}
}

func TestRenderer_Flash_Error(t *testing.T) {
	r, _ := NewRenderer()
	w := httptest.NewRecorder()
	r.Flash(w, "error", "boom")
	if !strings.Contains(w.Body.String(), "red-100") {
		t.Errorf("body = %q, want red-100 color", w.Body.String())
	}
}

func TestRenderer_Flash_Warning(t *testing.T) {
	r, _ := NewRenderer()
	w := httptest.NewRecorder()
	r.Flash(w, "warning", "watch")
	if !strings.Contains(w.Body.String(), "yellow-100") {
		t.Errorf("body = %q, want yellow-100 color", w.Body.String())
	}
}

func TestRenderer_Flash_Info(t *testing.T) {
	r, _ := NewRenderer()
	w := httptest.NewRecorder()
	r.Flash(w, "info", "fyi")
	if !strings.Contains(w.Body.String(), "blue-100") {
		t.Errorf("body = %q, want blue-100 color", w.Body.String())
	}
}

func TestRenderer_Flash_UnknownLevelDefaultsToBlue(t *testing.T) {
	r, _ := NewRenderer()
	w := httptest.NewRecorder()
	r.Flash(w, "mystery", "msg")
	if !strings.Contains(w.Body.String(), "blue-100") {
		t.Errorf("body = %q, want blue-100 default", w.Body.String())
	}
}

func TestRenderer_Flash_EscapesHTML(t *testing.T) {
	r, _ := NewRenderer()
	w := httptest.NewRecorder()
	r.Flash(w, "info", "<script>alert(1)</script>")
	if strings.Contains(w.Body.String(), "<script>") {
		t.Error("Flash did not HTML-escape user input")
	}
	if !strings.Contains(w.Body.String(), "&lt;script&gt;") {
		t.Errorf("body = %q, want escaped", w.Body.String())
	}
}

func TestPageDataStruct(t *testing.T) {
	pd := PageData{Title: "t", Active: "a", Data: 42}
	if pd.Title != "t" || pd.Active != "a" {
		t.Error("PageData struct fields wrong")
	}
}

func TestUserStruct(t *testing.T) {
	u := User{ID: 7, Username: "alice", Role: 1}
	if u.ID != 7 || u.Username != "alice" || u.Role != 1 {
		t.Error("User struct fields wrong")
	}
	_ = time.Time{}
	_ = model.ChannelEnabled // touch model import
}
