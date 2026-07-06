package model

import "time"

// AlertType enumerates the supported rule types. The DB column is a
// string for forward compatibility (we can add new types without a
// schema change).
type AlertType string

const (
	AlertErrorRate     AlertType = "error_rate"     // ratio of status>=400 to total in window
	AlertP95Latency    AlertType = "p95_latency"    // approximate p95 of duration_ms in window
	AlertCostSpike     AlertType = "cost_spike"     // cost in window vs prior window
	AlertKeyExhausted  AlertType = "key_exhausted"  // any channel has 0 active keys
)

// Alert is a user-configured rule. Threshold semantics depend on
// the Type; see alert.Evaluate.
type Alert struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Type          AlertType `json:"type"`
	Threshold     float64   `json:"threshold"`
	WindowSec     int64     `json:"window_sec"`
	CooldownSec   int64     `json:"cooldown_sec"`
	WebhookURL    string    `json:"webhook_url"`
	Enabled       bool      `json:"enabled"`
	LastFiredAt   int64     `json:"last_fired_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// AlertEvent records a fired alert. Payload is JSON-serialised
// metric values; the Webhook delivery status is also stored.
type AlertEvent struct {
	ID              int64     `json:"id"`
	AlertID         int64     `json:"alert_id"`
	AlertName       string    `json:"alert_name"`
	AlertType       AlertType `json:"alert_type"`
	FiredAt         time.Time `json:"fired_at"`
	Payload         string    `json:"payload"`
	DeliveredWebhook bool     `json:"delivered_webhook"`
	Acknowledged    bool      `json:"acknowledged"`
}
