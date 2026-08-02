package store

import "github.com/sn0wfree/llmRx/internal/model"

type AlertRepository interface {
	GetAlerts() ([]model.Alert, error)
	GetAlert(id int64) (*model.Alert, error)
	CreateAlert(a *model.Alert) error
	UpdateAlert(a *model.Alert) error
	DeleteAlert(id int64) error
	// RecordAlertFired atomically claims the fire by advancing
	// last_fired_at to atUnix. It only wins if atUnix is newer than
	// the stored value (conditional UPDATE), so concurrent replicas
	// can't double-fire: exactly one caller gets changed=true.
	RecordAlertFired(id int64, atUnix int64) (bool, error)
	DisableAlert(id int64, reason string) error
	GetAlertEvents(limit int) ([]model.AlertEvent, error)
	CreateAlertEvent(e *model.AlertEvent) error
	AckAlertEvent(id int64) error
}
