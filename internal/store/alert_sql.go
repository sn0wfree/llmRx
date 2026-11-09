package store

import (
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) GetAlerts() ([]model.Alert, error) {
	rows, err := s.query(`SELECT id, name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, disabled_reason, created_at FROM alerts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Alert
	for rows.Next() {
		var a model.Alert
		var enabled any
		var created int64
		if err := rows.Scan(&a.ID, &a.Name, &a.Type, &a.Threshold, &a.WindowSec, &a.CooldownSec, &a.WebhookURL, &enabled, &a.LastFiredAt, &a.DisabledReason, &created); err != nil {
			return nil, err
		}
		a.Enabled = s.d.ParseBool(enabled)
		a.CreatedAt = fromUnix(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *dbStore) GetAlert(id int64) (*model.Alert, error) {
	row := s.queryRow(`SELECT id, name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, disabled_reason, created_at FROM alerts WHERE id=?`, id)
	var a model.Alert
	var enabled any
	var created int64
	if err := row.Scan(&a.ID, &a.Name, &a.Type, &a.Threshold, &a.WindowSec, &a.CooldownSec, &a.WebhookURL, &enabled, &a.LastFiredAt, &a.DisabledReason, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.Enabled = s.d.ParseBool(enabled)
	a.CreatedAt = fromUnix(created)
	return &a, nil
}

func (s *dbStore) CreateAlert(a *model.Alert) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	id, err := dialect.InsertOne(s.d, s.db, `INSERT INTO alerts(name, type, threshold, window_sec, cooldown_sec, webhook_url, enabled, last_fired_at, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		a.Name, string(a.Type), a.Threshold, a.WindowSec, a.CooldownSec, a.WebhookURL, s.d.Bool(a.Enabled), a.LastFiredAt, a.CreatedAt.Unix())
	if err != nil {
		return err
	}
	a.ID = id
	return nil
}

func (s *dbStore) UpdateAlert(a *model.Alert) error {
	_, err := s.exec(`UPDATE alerts SET name=?, type=?, threshold=?, window_sec=?, cooldown_sec=?, webhook_url=?, enabled=?, last_fired_at=? WHERE id=?`,
		a.Name, string(a.Type), a.Threshold, a.WindowSec, a.CooldownSec, a.WebhookURL, s.d.Bool(a.Enabled), a.LastFiredAt, a.ID)
	return err
}

func (s *dbStore) DeleteAlert(id int64) error {
	_, err := s.exec(`DELETE FROM alerts WHERE id=?`, id)
	return err
}

func (s *dbStore) RecordAlertFired(id int64, atUnix int64) (bool, error) {
	// Conditional UPDATE: only the first replica to push
	// last_fired_at to atUnix wins the fire, so multi-replica
	// (Postgres) deployments can't double-send webhooks.
	// changed=false means another replica already fired with a
	// newer timestamp.
	//
	// <= (not <): with second-granularity timestamps, a rule whose
	// cooldown is shorter than one second legitimately re-fires
	// within the same second (e.g. cooldown=0, tick every second);
	// strictly-newer would silently swallow those fires. The
	// trade-off is that two replicas firing in the exact same
	// second can both claim the fire — a much narrower window than
	// the unconditional UPDATE this replaced.
	res, err := s.exec(`UPDATE alerts SET last_fired_at=? WHERE id=? AND last_fired_at <= ?`, atUnix, id, atUnix)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DisableAlert flips the rule's enabled flag to 0 and records
// the reason so the admin UI / /alerts listing can surface why
// the rule was auto-disabled. Idempotent.
func (s *dbStore) DisableAlert(id int64, reason string) error {
	res, err := s.exec(
		`UPDATE alerts SET enabled=?, disabled_reason=? WHERE id=?`,
		s.d.Bool(false), reason, id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *dbStore) CreateAlertEvent(e *model.AlertEvent) error {
	if e.FiredAt.IsZero() {
		e.FiredAt = time.Now()
	}
	id, err := dialect.InsertOne(s.d, s.db, `INSERT INTO alert_events(alert_id, alert_name, alert_type, fired_at, payload, delivered_webhook, acknowledged) VALUES(?,?,?,?,?,?,?)`,
		e.AlertID, e.AlertName, string(e.AlertType), e.FiredAt.Unix(), e.Payload, s.d.Bool(e.DeliveredWebhook), s.d.Bool(e.Acknowledged))
	if err != nil {
		return err
	}
	e.ID = id
	return nil
}

func (s *dbStore) GetAlertEvents(limit int) ([]model.AlertEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(`SELECT id, alert_id, alert_name, alert_type, fired_at, payload, delivered_webhook, acknowledged FROM alert_events ORDER BY fired_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AlertEvent
	for rows.Next() {
		var e model.AlertEvent
		var del, ack any
		var fired int64
		if err := rows.Scan(&e.ID, &e.AlertID, &e.AlertName, &e.AlertType, &fired, &e.Payload, &del, &ack); err != nil {
			return nil, err
		}
		e.FiredAt = time.Unix(fired, 0)
		e.DeliveredWebhook = s.d.ParseBool(del)
		e.Acknowledged = s.d.ParseBool(ack)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *dbStore) AckAlertEvent(id int64) error {
	_, err := s.exec(`UPDATE alert_events SET acknowledged=? WHERE id=?`, s.d.Bool(true), id)
	return err
}

// ---------------- raw access ----------------
