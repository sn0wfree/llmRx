package webui

import (
	"bytes"
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

func TestUserStruct(t *testing.T) {
	u := User{ID: 7, Username: "alice", Role: 1}
	if u.ID != 7 || u.Username != "alice" || u.Role != 1 {
		t.Error("User struct fields wrong")
	}
	_ = time.Time{}
	_ = model.ChannelEnabled // touch model import
}
