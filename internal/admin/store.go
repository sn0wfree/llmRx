package admin

import (
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/webui"
)

// AdminStore is the subset of store.Store methods consumed by admin handlers.
// Embeds webui.WebuiStore to reuse the 33 methods shared between webui and admin.
// store.Store (63 methods) implicitly satisfies this interface via Go's
// structural typing, so no adapter code is needed.
type AdminStore interface {
	webui.WebuiStore

	// Alerts (write operations not in WebuiStore)
	CreateAlert(a *model.Alert) error
	UpdateAlert(a *model.Alert) error
	AckAlertEvent(id int64) error

	// Analytics (additional methods not in WebuiStore)
	TimeSeries(f store.LogFilter, bucketSec int64) ([]store.SeriesPoint, error)
	TopByToken(f store.LogFilter, limit int) ([]store.NamedMetric, error)

	// Runtime config (write)
	SetRuntimeSettings(payload []byte) error

	// Secrets
	RotateMasterKey(newKeyHex string) (int, error)
}
