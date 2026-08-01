package webui

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// UserLookup is the narrow interface SessionMiddleware needs from the store.
// It extracts only the single method called during session resolution,
// decoupling the middleware from the full 63-method store.Store.
type UserLookup interface {
	GetUserBySession(token string) (*model.User, error)
}

// WebuiStore is the subset of store.Store methods consumed by webui handlers.
// Defined here (consumer side) per Go best practice: "accept interfaces,
// return structs". store.Store (63 methods) implicitly satisfies this
// interface via Go's structural typing, so no adapter code is needed.
type WebuiStore interface {
	UserLookup

	// Channels
	GetChannels() ([]model.Channel, error)
	GetChannel(id int64) (*model.Channel, error)
	CreateChannel(ch *model.Channel) error
	UpdateChannel(ch *model.Channel) error
	DeleteChannel(id int64) error

	// Keys
	GetKeys(channelID int64) ([]model.Key, error)
	CreateKey(k *model.Key) error
	DeleteKey(id int64) error

	// Tokens
	GetTokens() ([]model.Token, error)
	GetTokenByID(id int64) (*model.Token, error)
	CreateToken(t *model.Token) error
	UpdateToken(t *model.Token) error
	DeleteToken(id int64) error

	// Plans
	GetPlans() ([]model.Plan, error)
	GetPlan(id int64) (*model.Plan, error)
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error
	DeletePlan(id int64) error

	// Users
	GetUser(id int64) (*model.User, error)
	GetUserByUsername(username string) (*model.User, error)
	GetUsers() ([]model.User, error)
	CreateUser(u *model.User) error
	UpdateUser(u *model.User) error

	// Alerts
	GetAlerts() ([]model.Alert, error)
	GetAlert(id int64) (*model.Alert, error)
	DeleteAlert(id int64) error
	GetAlertEvents(limit int) ([]model.AlertEvent, error)

	// Runtime
	GetRuntimeSettings() ([]byte, error)

	// ProviderDefs
	GetProviderDefs() ([]model.ProviderDef, error)
	CreateProviderDef(p *model.ProviderDef) error
	DeleteProviderDef(id int64) error

	// ComboModels
	GetComboModels(tokenID int64) ([]model.TokenComboModel, error)
	GetComboModel(id int64) (*model.TokenComboModel, error)
	GetAllComboModels() ([]model.TokenComboModel, error)
	ListAllComboModels() ([]model.TokenComboModel, error)
	CreateComboModel(c *model.TokenComboModel) error
	UpdateComboModel(c *model.TokenComboModel) error
	DeleteComboModel(id int64) error
	SetDefaultModelSet(tokenID, comboID int64) error

	// MCP Servers
	GetMCPServers(ctx context.Context) ([]store.MCPServer, error)
	GetMCPServer(ctx context.Context, id int64) (*store.MCPServer, error)
	CreateMCPServer(ctx context.Context, s *store.MCPServer) error
	DeleteMCPServer(ctx context.Context, id int64) error
	GetMCPTools(ctx context.Context, serverID int64) ([]store.MCPTool, error)
	GetMCPToolPricing(ctx context.Context, toolID int64) (*store.MCPToolPricing, error)
	SetMCPToolPricing(ctx context.Context, p *store.MCPToolPricing) error

	// Guardrails
	GetGuardrailRules() ([]model.GuardrailRule, error)
	CreateGuardrailRule(r *model.GuardrailRule) error
	DeleteGuardrailRule(id int64) error
}
