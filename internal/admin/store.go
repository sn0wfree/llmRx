package admin

import (
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/webui"
)

// AdminStore is the subset of store.Store methods consumed by admin handlers.
// Embeds webui.WebuiStore to reuse the shared methods.
// store.Store implicitly satisfies this interface via Go's
// structural typing, so no adapter code is needed.
type AdminStore interface {
	webui.WebuiStore

	// Alerts (write operations not in WebuiStore)
	CreateAlert(a *model.Alert) error
	UpdateAlert(a *model.Alert) error
	AckAlertEvent(id int64) error

	// Runtime config (write)
	SetRuntimeSettings(payload []byte) error

	// Secrets
	RotateMasterKey(newKeyHex string) (int, error)
}
