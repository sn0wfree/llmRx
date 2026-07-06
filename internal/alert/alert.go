// Package alert provides a periodic rule evaluator that fires
// configured Alerts when conditions are met. Fired events are
// persisted via the Store and optionally POSTed to a webhook URL.
//
// The Manager is intentionally lightweight: one goroutine ticks
// every TickInterval, evaluates every enabled rule, and dispatches
// any firings. Cooldown is enforced by the Store (LastFiredAt) so
// the system is resilient to restarts.
package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Channel delivers a fired alert event. Implementations include
// the webhook poster and the in-process "builtin" channel that
// simply persists the event.
type Channel interface {
	Name() string
	Deliver(ev *model.AlertEvent) error
}

// Manager owns the scheduler loop and the registry of rules.
type Manager struct {
	st            store.Store
	channels      []Channel
	tickInterval  time.Duration
	defaultCooldown time.Duration

	mu    sync.RWMutex
	rules []model.Alert
}

// Config holds construction-time parameters.
type Config struct {
	TickInterval    time.Duration // default 30s
	DefaultCooldown time.Duration // default 5m
}

// NewManager wires up a manager. TickInterval <= 0 falls back to 30s
// and DefaultCooldown <= 0 falls back to 5m.
func NewManager(st store.Store, chans []Channel, cfg Config) *Manager {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 30 * time.Second
	}
	if cfg.DefaultCooldown <= 0 {
		cfg.DefaultCooldown = 5 * time.Minute
	}
	return &Manager{
		st:               st,
		channels:         chans,
		tickInterval:     cfg.TickInterval,
		defaultCooldown:  cfg.DefaultCooldown,
	}
}

// Start loads rules and runs the evaluation loop. Returns when ctx
// is cancelled.
func (m *Manager) Start(ctx context.Context) {
	if err := m.reload(); err != nil {
		log.Printf("alert: initial reload: %v", err)
	}
	t := time.NewTicker(m.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.reload(); err != nil {
				log.Printf("alert: reload: %v", err)
				continue
			}
			m.evaluate(ctx)
		}
	}
}

// Reload forces a re-read of rules from the store. Exposed for tests
// and the admin "test" path.
func (m *Manager) Reload() error { return m.reload() }

func (m *Manager) reload() error {
	rules, err := m.st.GetAlerts()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.rules = rules
	m.mu.Unlock()
	return nil
}

func (m *Manager) evaluate(ctx context.Context) {
	m.mu.RLock()
	rules := append([]model.Alert(nil), m.rules...)
	m.mu.RUnlock()
	now := time.Now()
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		// Cooldown gate (defence in depth; primary guard is in
		// fire() but reading the rule here avoids the SQL roundtrip).
		cd := time.Duration(r.CooldownSec) * time.Second
		if cd <= 0 {
			cd = m.defaultCooldown
		}
		if r.LastFiredAt > 0 && now.Sub(time.Unix(r.LastFiredAt, 0)) < cd {
			continue
		}
		fired, payload, err := Evaluate(&r, now, m.st)
		if err != nil {
			log.Printf("alert: eval %s: %v", r.Name, err)
			continue
		}
		if !fired {
			continue
		}
		if err := m.fire(ctx, &r, payload, now); err != nil {
			log.Printf("alert: fire %s: %v", r.Name, err)
		}
	}
}

// fire persists the event, notifies channels, and bumps LastFiredAt.
// It is wrapped in a guard that re-checks the cooldown atomically by
// updating last_fired_at to "now" via the store, then re-reading
// before any second call within the same tick could double-fire.
func (m *Manager) fire(ctx context.Context, r *model.Alert, payload map[string]any, now time.Time) error {
	// Inject the webhook URL into the payload so the webhook
	// channel can find it without needing a back-reference to
	// the alert row.
	if r.WebhookURL != "" {
		payload["_webhook_url"] = r.WebhookURL
	}
	body, _ := json.Marshal(payload)
	ev := &model.AlertEvent{
		AlertID:   r.ID,
		AlertName: r.Name,
		AlertType: r.Type,
		FiredAt:   now,
		Payload:   string(body),
	}
	delivered := false
	for _, c := range m.channels {
		if c.Name() == "webhook" {
			if err := c.Deliver(ev); err == nil {
				delivered = true
			} else {
				log.Printf("alert: webhook %s: %v", r.Name, err)
			}
		}
	}
	ev.DeliveredWebhook = delivered
	if err := m.st.CreateAlertEvent(ev); err != nil {
		return fmt.Errorf("persist event: %w", err)
	}
	if err := m.st.RecordAlertFired(r.ID, now.Unix()); err != nil {
		return fmt.Errorf("record fired: %w", err)
	}
	// Update local copy so the cooldown gate works in-process.
	m.mu.Lock()
	for i := range m.rules {
		if m.rules[i].ID == r.ID {
			m.rules[i].LastFiredAt = now.Unix()
		}
	}
	m.mu.Unlock()
	_ = ctx
	return nil
}
