package admin

import (
	"context"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// AdminStore is what the admin JSON API needs from the store: the shared
// store.Reader plus the calls only the API makes.
//
// It deliberately does not embed the console's interface: the HTML
// console consumes the API layer, so an API -> console dependency would
// invert that direction and make every store change break both.
type AdminStore interface {
	store.Reader

	// Redeem codes: admin issues them; the console does not.
	CreateRedeemCode(c *model.RedeemCode) error

	// Guardrails: the API lists events and edits rules; the console
	// only lists the rules.
	GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error)

	GetGuardrailRule(id int64) (*model.GuardrailRule, error)

	UpdateGuardrailRule(r *model.GuardrailRule) error

	// MCP servers: the API reads and updates a single server.
	GetMCPServer(ctx context.Context, id int64) (*store.MCPServer, error)

	UpdateMCPServer(ctx context.Context, s *store.MCPServer) error

	// The balance snapshot write is a monitor-owned operation the API
	// exposes so an operator can trigger a refresh.
	UpdateChannelBalance(channelID int64, balance, currency string, checkedAt time.Time, balanceErr string) error

	// Runtime settings and the master key are root-only operations.
	RotateMasterKey(newKeyHex string) (int, error)

	SetRuntimeSettings(payload []byte) error
}
