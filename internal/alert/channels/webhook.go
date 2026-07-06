// Package channels provides delivery implementations for fired
// alert events. Each Channel is invoked once per fired event.
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Webhook delivers events via HTTP POST. The URL is taken from the
// Alert itself, not the channel constructor, because each rule may
// have a different endpoint.
type Webhook struct {
	Client *http.Client
}

// NewWebhook returns a Webhook with sensible timeouts and a small
// connection pool.
func NewWebhook() *Webhook {
	return &Webhook{
		Client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Name returns "webhook".
func (w *Webhook) Name() string { return "webhook" }

// Deliver POSTs the event as JSON to ev.AlertName's configured URL.
// We have to look up the URL on the event; the manager passes the
// alert in the model.AlertEvent.AlertName; the URL is fetched via
// a callback because the channel shouldn't reach into the Store.
//
// For simplicity we accept the URL via the event's payload's
// "_webhook_url" key set by the manager; if missing, Deliver is a
// no-op success.
func (w *Webhook) Deliver(ev *model.AlertEvent) error {
	url := extractURL(ev)
	if url == "" {
		return nil
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llmRx-alert/1")
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	return nil
}

func extractURL(ev *model.AlertEvent) string {
	// Convention: when the manager dispatches, it stashes the URL
	// inside the JSON payload as "_webhook_url" so this channel
	// doesn't need a back-reference. Look for it.
	var p map[string]any
	if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
		return ""
	}
	if v, ok := p["_webhook_url"].(string); ok {
		return v
	}
	return ""
}
