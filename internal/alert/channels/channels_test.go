package channels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestBuiltinDelivers(t *testing.T) {
	b := NewBuiltin()
	if b.Name() != "builtin" {
		t.Fatalf("name: %s", b.Name())
	}
	ev := &model.AlertEvent{AlertID: 1, AlertName: "x"}
	if err := b.Deliver(ev); err != nil {
		t.Fatalf("deliver: %v", err)
	}
}

func TestWebhookSkipsWhenNoURL(t *testing.T) {
	w := NewWebhook()
	ev := &model.AlertEvent{Payload: `{}`}
	if err := w.Deliver(ev); err != nil {
		t.Fatalf("deliver with no url: %v", err)
	}
}

func TestWebhookPostsToURL(t *testing.T) {
	var hits int32
	var gotPayload string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		var ev model.AlertEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		gotPayload = ev.Payload
		rw.WriteHeader(200)
	}))
	defer srv.Close()

	w := NewWebhook()
	ev := &model.AlertEvent{AlertID: 7, AlertName: "x", Payload: `{"_webhook_url":"` + srv.URL + `","k":"v"}`}
	if err := w.Deliver(ev); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits: %d", hits)
	}
	if gotPayload == "" {
		t.Fatal("server received no payload")
	}
}

func TestWebhookHandlesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(500)
	}))
	defer srv.Close()
	w := NewWebhook()
	ev := &model.AlertEvent{Payload: `{"_webhook_url":"` + srv.URL + `"}`}
	if err := w.Deliver(ev); err == nil {
		t.Fatal("expected error on 500")
	}
}
