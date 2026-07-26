package channels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestWebhook_Name(t *testing.T) {
	w := NewWebhook()
	if w.Name() != "webhook" {
		t.Fatalf("Name: got %q", w.Name())
	}
}

func TestWebhook_Deliver_NoURL(t *testing.T) {
	w := NewWebhook()
	ev := &model.AlertEvent{
		AlertName: "test",
		Payload:   `{"value": 42}`,
	}
	if err := w.Deliver(ev); err != nil {
		t.Fatalf("Deliver with no URL should be noop: %v", err)
	}
}

func TestWebhook_Deliver_Success(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := NewWebhook()
	ev := &model.AlertEvent{
		AlertName: "test-alert",
		AlertType: model.AlertErrorRate,
		Payload:   `{"_webhook_url":"` + srv.URL + `","error_rate":0.75}`,
	}
	if err := w.Deliver(ev); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if received == nil {
		t.Fatal("webhook not received")
	}
	if received["alert_name"] != "test-alert" {
		t.Fatalf("alert_name mismatch: %v", received["alert_name"])
	}
}

func TestWebhook_Deliver_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := NewWebhook()
	ev := &model.AlertEvent{
		AlertName: "fail",
		Payload:   `{"_webhook_url":"` + srv.URL + `"}`,
	}
	if err := w.Deliver(ev); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestExtractURL(t *testing.T) {
	tests := []struct {
		payload string
		want    string
	}{
		{`{"_webhook_url":"https://example.com"}`, "https://example.com"},
		{`{"other":"data"}`, ""},
		{`not json`, ""},
		{``, ""},
	}
	for _, tc := range tests {
		ev := &model.AlertEvent{Payload: tc.payload}
		got := extractURL(ev)
		if got != tc.want {
			t.Errorf("extractURL(%q): got %q, want %q", tc.payload, got, tc.want)
		}
	}
}
