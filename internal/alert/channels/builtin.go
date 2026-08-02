package channels

import (
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

// Builtin always succeeds; it logs the event so operators can see
// it via stdout in addition to the admin UI's alert_events table.
type Builtin struct{}

// NewBuiltin returns a Builtin channel that just logs to stdout.
func NewBuiltin() *Builtin { return &Builtin{} }

// Name returns "builtin".
func (b *Builtin) Name() string { return "builtin" }

// Deliver logs the event and returns nil. Persistence is handled
// by the manager after the channels all run.
func (b *Builtin) Deliver(ev *model.AlertEvent) error {
	logging.Warn("ALERT fire",
		logging.F("alert_id", ev.AlertID),
		logging.F("name", ev.AlertName),
		logging.F("type", ev.AlertType),
		logging.F("payload", ev.Payload),
	)
	return nil
}
