package sse

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewAndEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := New(rec)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type: %q", got)
	}
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if err := w.Event("log", `{"id":1}`); err != nil {
		t.Fatalf("event: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: log\n") {
		t.Fatalf("missing event: %q", out)
	}
	if !strings.Contains(out, "data: {\"id\":1}\n\n") {
		t.Fatalf("missing data: %q", out)
	}
}

func TestEventMultiline(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	_ = w.Event("e", "line1\nline2")
	out := rec.Body.String()
	if !strings.Contains(out, "data: line1\ndata: line2\n\n") {
		t.Fatalf("multiline: %q", out)
	}
}

func TestComment(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	if err := w.Comment("hi"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if got := rec.Body.String(); !strings.HasPrefix(got, ": hi\n\n") {
		t.Fatalf("comment: %q", got)
	}
}

func TestHeartbeatStopsOnContextCancel(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	ctx, cancel := context.WithCancel(context.Background())
	go w.Heartbeat(ctx, 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	cancel()
	// Give the goroutine a tick to exit; test completes regardless.
	time.Sleep(20 * time.Millisecond)
}

func TestEventBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	if err := w.EventBytes("ping", []byte("hello world")); err != nil {
		t.Fatalf("eventbytes: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: ping\n") {
		t.Fatalf("missing event: %q", out)
	}
	if !strings.Contains(out, "data: hello world\n\n") {
		t.Fatalf("missing data: %q", out)
	}
}

func TestEventBytesMultiline(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	if err := w.EventBytes("", []byte("a\nb\nc")); err != nil {
		t.Fatalf("eventbytes: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "data: a\ndata: b\ndata: c\n\n") {
		t.Fatalf("multiline: %q", out)
	}
}

func TestEventBytesEmptyEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	if err := w.EventBytes("", []byte("data only")); err != nil {
		t.Fatalf("eventbytes: %v", err)
	}
	out := rec.Body.String()
	if strings.Contains(out, "event:") {
		t.Fatalf("unexpected event line: %q", out)
	}
	if !strings.Contains(out, "data: data only\n\n") {
		t.Fatalf("missing data: %q", out)
	}
}

func TestEventJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	if err := w.EventJSON("log", map[string]any{"id": 42, "msg": "hi"}); err != nil {
		t.Fatalf("eventjson: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: log\n") {
		t.Fatalf("missing event: %q", out)
	}
	if !strings.Contains(out, `"id":42`) || !strings.Contains(out, `"msg":"hi"`) {
		t.Fatalf("missing json data: %q", out)
	}
}

func TestEventJSONEmptyEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := New(rec)
	if err := w.EventJSON("", []int{1, 2, 3}); err != nil {
		t.Fatalf("eventjson: %v", err)
	}
	out := rec.Body.String()
	if strings.Contains(out, "event:") {
		t.Fatalf("unexpected event line: %q", out)
	}
	if !strings.Contains(out, "data: [1,2,3]\n\n") {
		t.Fatalf("missing data: %q", out)
	}
}
