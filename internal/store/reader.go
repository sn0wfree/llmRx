package store

import (
	"context"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Reader is the store surface shared by the two control-plane
// consumers: the admin JSON API and the HTML console. It lives here so
// neither has to depend on the other — the console consumes the API
// layer, it is not its peer, and importing it from the API would invert
// that direction.
//
// Only methods both actually call belong here; a call that just one of
// them makes stays in that package's own interface.
type Reader interface {
	AckAlertEvent(id int64) error

	BalanceHistory(channelID int64, from time.Time, limit int) ([]BalancePoint, error)

	BatchUpsertModelCosts(costs []model.ModelCost) error

	CreateAlert(a *model.Alert) error

	CreateChannel(ch *model.Channel) error

	CreateComboModel(c *model.TokenComboModel) error

	CreateGuardrailRule(r *model.GuardrailRule) error

	CreateKey(k *model.Key) error

	CreateMCPServer(ctx context.Context, s *MCPServer) error

	CreatePlan(p *model.Plan) error

	CreateProviderDef(p *model.ProviderDef) error

	CreateToken(t *model.Token) error

	CreateUser(u *model.User) error

	DeleteAlert(id int64) error

	DeleteChannel(id int64) error

	DeleteComboModel(id int64) error

	DeleteGuardrailRule(id int64) error

	DeleteKey(id int64) error

	DeleteMCPServer(ctx context.Context, id int64) error

	DeleteModelCost(model string) error

	DeletePlan(id int64) error

	DeleteProviderDef(id int64) error

	DeleteToken(id int64) error

	GetAlert(id int64) (*model.Alert, error)

	GetAlertEvents(limit int) ([]model.AlertEvent, error)

	GetAlerts() ([]model.Alert, error)

	GetChannel(id int64) (*model.Channel, error)

	GetChannels() ([]model.Channel, error)

	GetComboModel(id int64) (*model.TokenComboModel, error)

	GetComboModels(tokenID int64) ([]model.TokenComboModel, error)

	GetGuardrailRules() ([]model.GuardrailRule, error)

	GetKeys(channelID int64) ([]model.Key, error)

	GetMCPServers(ctx context.Context) ([]MCPServer, error)

	GetMCPToolPricing(ctx context.Context, toolID int64) (*MCPToolPricing, error)

	GetMCPTools(ctx context.Context, serverID int64) ([]MCPTool, error)

	GetPlan(id int64) (*model.Plan, error)

	GetPlans() ([]model.Plan, error)

	GetRuntimeSettings() ([]byte, error)

	GetTokenByID(id int64) (*model.Token, error)

	GetTokens() ([]model.Token, error)

	GetUser(id int64) (*model.User, error)

	GetUserBySession(token string) (*model.User, error)

	GetUserByUsername(username string) (*model.User, error)

	GetUsers() ([]model.User, error)

	ListModelCosts() ([]model.ModelCost, error)

	ListRedeemCodes() ([]model.RedeemCode, error)

	SetMCPToolPricing(ctx context.Context, p *MCPToolPricing) error

	UpdateAlert(a *model.Alert) error

	UpdateChannel(ch *model.Channel) error

	UpdateComboModel(c *model.TokenComboModel) error

	UpdatePlan(p *model.Plan) error

	UpdateToken(t *model.Token) error

	UpdateUser(u *model.User) error

	UpsertModelCost(c *model.ModelCost) error

	UsagePoints(channelID int64, from time.Time, limit int) ([]UsagePoint, error)

}
