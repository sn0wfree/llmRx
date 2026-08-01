package store

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

type GuardrailRepository interface {
	GetEnabledGuardrailRules() ([]model.GuardrailRule, error)
	GetGuardrailRules() ([]model.GuardrailRule, error)
	GetGuardrailRule(id int64) (*model.GuardrailRule, error)
	CreateGuardrailRule(r *model.GuardrailRule) error
	UpdateGuardrailRule(r *model.GuardrailRule) error
	DeleteGuardrailRule(id int64) error
	CreateGuardrailEvent(e *model.GuardrailEvent) error
	GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error)
}

type BYOKRepository interface {
	CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error)
	ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error)
	GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error)
	GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error)
	TouchBYOKChannel(ctx context.Context, id int64) error
	DeleteBYOKChannel(ctx context.Context, id int64) error
}

type ProviderDefRepository interface {
	GetProviderDefs() ([]model.ProviderDef, error)
	CreateProviderDef(p *model.ProviderDef) error
	DeleteProviderDef(id int64) error
}

type ComboModelRepository interface {
	GetComboModels(tokenID int64) ([]model.TokenComboModel, error)
	GetComboModel(id int64) (*model.TokenComboModel, error)
	GetAllComboModels() ([]model.TokenComboModel, error)
	ListAllComboModels() ([]model.TokenComboModel, error)
	CreateComboModel(c *model.TokenComboModel) error
	UpdateComboModel(c *model.TokenComboModel) error
	DeleteComboModel(id int64) error
	SetDefaultModelSet(tokenID, comboID int64) error
}

type RuntimeRepository interface {
	GetRuntimeSettings() ([]byte, error)
	SetRuntimeSettings(payload []byte) error
}

type SecurityRepository interface {
	SetSecrets(m *secrets.Manager)
	RotateMasterKey(newKeyHex string) (int, error)
	ReencryptAllKeys(oldMgr, newMgr *secrets.Manager) (int, error)
}