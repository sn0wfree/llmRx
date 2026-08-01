package testhelper

import (
	"context"
	"database/sql"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
)

// ScriptedStore wraps a real store.Store and allows overriding individual
// methods with custom functions. By default, all methods delegate to the
// underlying store. Set any override field to intercept a specific call.
//
// Usage:
//
//	realStore := openTempStore(t)
//	ss := NewScriptedStore(realStore)
//	ss.GetChannelsFunc = func() ([]model.Channel, error) {
//	    return nil, errors.New("database locked")
//	}
//	h, _ := webui.New(ss, nil, "")
type ScriptedStore struct {
	underlying store.Store

	// Channels
	GetChannelsFunc  func() ([]model.Channel, error)
	GetChannelFunc   func(id int64) (*model.Channel, error)
	CreateChannelFunc func(ch *model.Channel) error
	UpdateChannelFunc func(ch *model.Channel) error
	DeleteChannelFunc func(id int64) error
	GetDrainedChannelsFunc func() ([]store.DrainedChannel, error)

	// Keys
	GetKeysFunc  func(channelID int64) ([]model.Key, error)
	CreateKeyFunc func(k *model.Key) error
	DeleteKeyFunc func(id int64) error
	WipeKeysFunc func() (int64, error)

	// Tokens
	GetTokenFunc         func(key string) (*model.Token, error)
	GetTokenByIDFunc     func(id int64) (*model.Token, error)
	GetTokensFunc        func() ([]model.Token, error)
	CreateTokenFunc      func(t *model.Token) error
	UpdateTokenFunc      func(t *model.Token) error
	DeleteTokenFunc      func(id int64) error
	IncrementTokenSpendFunc func(tokenID int64, amount float64) error
	IncrementPlanSpendFunc  func(planID int64, amount float64) error
	RecordRequestSpendFunc  func(tokenID, planID int64, amount float64) error
	MarkTokenExpiredFunc    func(tokenID int64) error

	// Plans
	GetPlansFunc  func() ([]model.Plan, error)
	GetPlanFunc   func(id int64) (*model.Plan, error)
	CreatePlanFunc func(p *model.Plan) error
	UpdatePlanFunc func(p *model.Plan) error
	DeletePlanFunc func(id int64) error

	// Users
	GetUsersFunc          func() ([]model.User, error)
	GetUserFunc           func(id int64) (*model.User, error)
	GetUserByUsernameFunc func(username string) (*model.User, error)
	GetUserBySessionFunc  func(token string) (*model.User, error)
	CreateUserFunc        func(u *model.User) error
	UpdateUserFunc        func(u *model.User) error
	CleanupExpiredSessionsFunc func() (int64, error)

	// Alerts
	GetAlertsFunc       func() ([]model.Alert, error)
	GetAlertFunc        func(id int64) (*model.Alert, error)
	CreateAlertFunc     func(a *model.Alert) error
	UpdateAlertFunc     func(a *model.Alert) error
	DeleteAlertFunc     func(id int64) error
	RecordAlertFiredFunc func(id int64, atUnix int64) error
	DisableAlertFunc    func(id int64, reason string) error
	GetAlertEventsFunc  func(limit int) ([]model.AlertEvent, error)
	CreateAlertEventFunc func(e *model.AlertEvent) error
	AckAlertEventFunc   func(id int64) error

	// Raw
	RawQueryRowFunc func(query string, args ...any) *sql.Row
	RawQueryFunc    func(query string, args ...any) (*sql.Rows, error)
	RawDBFunc       func() *sql.DB

	// Runtime
	GetRuntimeSettingsFunc  func() ([]byte, error)
	SetRuntimeSettingsFunc  func(payload []byte) error

	// Secrets
	ReencryptAllKeysFunc func(oldMgr, newMgr *secrets.Manager) (int, error)
	SetSecretsFunc        func(m *secrets.Manager)
	RotateMasterKeyFunc   func(newKeyHex string) (int, error)

	// Lifecycle
	PingFunc  func(ctx context.Context) error
	CloseFunc func() error

	// BYOK
	CreateBYOKChannelFunc    func(ctx context.Context, ch *model.BYOKChannel) (int64, error)
	ListBYOKChannelsFunc     func(ctx context.Context) ([]*model.BYOKChannel, error)
	GetBYOKChannelFunc       func(ctx context.Context, id int64) (*model.BYOKChannel, error)
	GetBYOKChannelByIPFunc   func(ctx context.Context, ownerIP string) (*model.BYOKChannel, error)
	TouchBYOKChannelFunc     func(ctx context.Context, id int64) error
	DeleteBYOKChannelFunc    func(ctx context.Context, id int64) error

	// ProviderDefs
	GetProviderDefsFunc  func() ([]model.ProviderDef, error)
	CreateProviderDefFunc func(p *model.ProviderDef) error
	DeleteProviderDefFunc func(id int64) error

	// ComboModels
	GetComboModelsFunc    func(tokenID int64) ([]model.TokenComboModel, error)
	GetComboModelFunc     func(id int64) (*model.TokenComboModel, error)
	GetAllComboModelsFunc func() ([]model.TokenComboModel, error)
	ListAllComboModelsFunc func() ([]model.TokenComboModel, error)
	CreateComboModelFunc  func(c *model.TokenComboModel) error
	UpdateComboModelFunc  func(c *model.TokenComboModel) error
	DeleteComboModelFunc  func(id int64) error
	SetDefaultModelSetFunc func(tokenID, comboID int64) error

	// Guardrails
	GetEnabledGuardrailRulesFunc func() ([]model.GuardrailRule, error)
	GetGuardrailRulesFunc        func() ([]model.GuardrailRule, error)
	GetGuardrailRuleFunc         func(id int64) (*model.GuardrailRule, error)
	CreateGuardrailRuleFunc      func(r *model.GuardrailRule) error
	UpdateGuardrailRuleFunc      func(r *model.GuardrailRule) error
	DeleteGuardrailRuleFunc      func(id int64) error
	CreateGuardrailEventFunc     func(e *model.GuardrailEvent) error
	GetGuardrailEventsFunc       func(tokenID int64, limit int) ([]model.GuardrailEvent, error)
}

// NewScriptedStore wraps the given store. All methods delegate to the
// underlying store unless the corresponding Func field is set.
func NewScriptedStore(underlying store.Store) *ScriptedStore {
	return &ScriptedStore{underlying: underlying}
}

// --- Channels ---

func (s *ScriptedStore) GetChannels() ([]model.Channel, error) {
	if s.GetChannelsFunc != nil {
		return s.GetChannelsFunc()
	}
	return s.underlying.GetChannels()
}
func (s *ScriptedStore) GetChannel(id int64) (*model.Channel, error) {
	if s.GetChannelFunc != nil {
		return s.GetChannelFunc(id)
	}
	return s.underlying.GetChannel(id)
}
func (s *ScriptedStore) CreateChannel(ch *model.Channel) error {
	if s.CreateChannelFunc != nil {
		return s.CreateChannelFunc(ch)
	}
	return s.underlying.CreateChannel(ch)
}
func (s *ScriptedStore) UpdateChannel(ch *model.Channel) error {
	if s.UpdateChannelFunc != nil {
		return s.UpdateChannelFunc(ch)
	}
	return s.underlying.UpdateChannel(ch)
}
func (s *ScriptedStore) DeleteChannel(id int64) error {
	if s.DeleteChannelFunc != nil {
		return s.DeleteChannelFunc(id)
	}
	return s.underlying.DeleteChannel(id)
}

func (s *ScriptedStore) GetDrainedChannels() ([]store.DrainedChannel, error) {
	if s.GetDrainedChannelsFunc != nil {
		return s.GetDrainedChannelsFunc()
	}
	return s.underlying.GetDrainedChannels()
}

// --- Keys ---

func (s *ScriptedStore) GetKeys(channelID int64) ([]model.Key, error) {
	if s.GetKeysFunc != nil {
		return s.GetKeysFunc(channelID)
	}
	return s.underlying.GetKeys(channelID)
}
func (s *ScriptedStore) CreateKey(k *model.Key) error {
	if s.CreateKeyFunc != nil {
		return s.CreateKeyFunc(k)
	}
	return s.underlying.CreateKey(k)
}
func (s *ScriptedStore) DeleteKey(id int64) error {
	if s.DeleteKeyFunc != nil {
		return s.DeleteKeyFunc(id)
	}
	return s.underlying.DeleteKey(id)
}
func (s *ScriptedStore) WipeKeys() (int64, error) {
	if s.WipeKeysFunc != nil {
		return s.WipeKeysFunc()
	}
	return s.underlying.WipeKeys()
}

// --- Tokens ---

func (s *ScriptedStore) GetToken(key string) (*model.Token, error) {
	if s.GetTokenFunc != nil {
		return s.GetTokenFunc(key)
	}
	return s.underlying.GetToken(key)
}
func (s *ScriptedStore) GetTokenByID(id int64) (*model.Token, error) {
	if s.GetTokenByIDFunc != nil {
		return s.GetTokenByIDFunc(id)
	}
	return s.underlying.GetTokenByID(id)
}
func (s *ScriptedStore) GetTokens() ([]model.Token, error) {
	if s.GetTokensFunc != nil {
		return s.GetTokensFunc()
	}
	return s.underlying.GetTokens()
}
func (s *ScriptedStore) CreateToken(t *model.Token) error {
	if s.CreateTokenFunc != nil {
		return s.CreateTokenFunc(t)
	}
	return s.underlying.CreateToken(t)
}
func (s *ScriptedStore) UpdateToken(t *model.Token) error {
	if s.UpdateTokenFunc != nil {
		return s.UpdateTokenFunc(t)
	}
	return s.underlying.UpdateToken(t)
}
func (s *ScriptedStore) DeleteToken(id int64) error {
	if s.DeleteTokenFunc != nil {
		return s.DeleteTokenFunc(id)
	}
	return s.underlying.DeleteToken(id)
}
func (s *ScriptedStore) IncrementTokenSpend(tokenID int64, amount float64) error {
	if s.IncrementTokenSpendFunc != nil {
		return s.IncrementTokenSpendFunc(tokenID, amount)
	}
	return s.underlying.IncrementTokenSpend(tokenID, amount)
}
func (s *ScriptedStore) IncrementPlanSpend(planID int64, amount float64) error {
	if s.IncrementPlanSpendFunc != nil {
		return s.IncrementPlanSpendFunc(planID, amount)
	}
	return s.underlying.IncrementPlanSpend(planID, amount)
}
func (s *ScriptedStore) RecordRequestSpend(tokenID, planID int64, amount float64) error {
	if s.RecordRequestSpendFunc != nil {
		return s.RecordRequestSpendFunc(tokenID, planID, amount)
	}
	return s.underlying.RecordRequestSpend(tokenID, planID, amount)
}
func (s *ScriptedStore) MarkTokenExpired(tokenID int64) error {
	if s.MarkTokenExpiredFunc != nil {
		return s.MarkTokenExpiredFunc(tokenID)
	}
	return s.underlying.MarkTokenExpired(tokenID)
}

// --- Plans ---

func (s *ScriptedStore) GetPlans() ([]model.Plan, error) {
	if s.GetPlansFunc != nil {
		return s.GetPlansFunc()
	}
	return s.underlying.GetPlans()
}
func (s *ScriptedStore) GetPlan(id int64) (*model.Plan, error) {
	if s.GetPlanFunc != nil {
		return s.GetPlanFunc(id)
	}
	return s.underlying.GetPlan(id)
}
func (s *ScriptedStore) CreatePlan(p *model.Plan) error {
	if s.CreatePlanFunc != nil {
		return s.CreatePlanFunc(p)
	}
	return s.underlying.CreatePlan(p)
}
func (s *ScriptedStore) UpdatePlan(p *model.Plan) error {
	if s.UpdatePlanFunc != nil {
		return s.UpdatePlanFunc(p)
	}
	return s.underlying.UpdatePlan(p)
}
func (s *ScriptedStore) DeletePlan(id int64) error {
	if s.DeletePlanFunc != nil {
		return s.DeletePlanFunc(id)
	}
	return s.underlying.DeletePlan(id)
}

// --- Users ---

func (s *ScriptedStore) GetUsers() ([]model.User, error) {
	if s.GetUsersFunc != nil {
		return s.GetUsersFunc()
	}
	return s.underlying.GetUsers()
}
func (s *ScriptedStore) GetUser(id int64) (*model.User, error) {
	if s.GetUserFunc != nil {
		return s.GetUserFunc(id)
	}
	return s.underlying.GetUser(id)
}
func (s *ScriptedStore) GetUserByUsername(username string) (*model.User, error) {
	if s.GetUserByUsernameFunc != nil {
		return s.GetUserByUsernameFunc(username)
	}
	return s.underlying.GetUserByUsername(username)
}
func (s *ScriptedStore) GetUserBySession(token string) (*model.User, error) {
	if s.GetUserBySessionFunc != nil {
		return s.GetUserBySessionFunc(token)
	}
	return s.underlying.GetUserBySession(token)
}
func (s *ScriptedStore) CreateUser(u *model.User) error {
	if s.CreateUserFunc != nil {
		return s.CreateUserFunc(u)
	}
	return s.underlying.CreateUser(u)
}
func (s *ScriptedStore) UpdateUser(u *model.User) error {
	if s.UpdateUserFunc != nil {
		return s.UpdateUserFunc(u)
	}
	return s.underlying.UpdateUser(u)
}
func (s *ScriptedStore) CleanupExpiredSessions() (int64, error) {
	if s.CleanupExpiredSessionsFunc != nil {
		return s.CleanupExpiredSessionsFunc()
	}
	return s.underlying.CleanupExpiredSessions()
}

// --- Alerts ---

func (s *ScriptedStore) GetAlerts() ([]model.Alert, error) {
	if s.GetAlertsFunc != nil {
		return s.GetAlertsFunc()
	}
	return s.underlying.GetAlerts()
}
func (s *ScriptedStore) GetAlert(id int64) (*model.Alert, error) {
	if s.GetAlertFunc != nil {
		return s.GetAlertFunc(id)
	}
	return s.underlying.GetAlert(id)
}
func (s *ScriptedStore) CreateAlert(a *model.Alert) error {
	if s.CreateAlertFunc != nil {
		return s.CreateAlertFunc(a)
	}
	return s.underlying.CreateAlert(a)
}
func (s *ScriptedStore) UpdateAlert(a *model.Alert) error {
	if s.UpdateAlertFunc != nil {
		return s.UpdateAlertFunc(a)
	}
	return s.underlying.UpdateAlert(a)
}
func (s *ScriptedStore) DeleteAlert(id int64) error {
	if s.DeleteAlertFunc != nil {
		return s.DeleteAlertFunc(id)
	}
	return s.underlying.DeleteAlert(id)
}
func (s *ScriptedStore) RecordAlertFired(id int64, atUnix int64) error {
	if s.RecordAlertFiredFunc != nil {
		return s.RecordAlertFiredFunc(id, atUnix)
	}
	return s.underlying.RecordAlertFired(id, atUnix)
}
func (s *ScriptedStore) DisableAlert(id int64, reason string) error {
	if s.DisableAlertFunc != nil {
		return s.DisableAlertFunc(id, reason)
	}
	return s.underlying.DisableAlert(id, reason)
}
func (s *ScriptedStore) GetAlertEvents(limit int) ([]model.AlertEvent, error) {
	if s.GetAlertEventsFunc != nil {
		return s.GetAlertEventsFunc(limit)
	}
	return s.underlying.GetAlertEvents(limit)
}
func (s *ScriptedStore) CreateAlertEvent(e *model.AlertEvent) error {
	if s.CreateAlertEventFunc != nil {
		return s.CreateAlertEventFunc(e)
	}
	return s.underlying.CreateAlertEvent(e)
}
func (s *ScriptedStore) AckAlertEvent(id int64) error {
	if s.AckAlertEventFunc != nil {
		return s.AckAlertEventFunc(id)
	}
	return s.underlying.AckAlertEvent(id)
}

// --- Raw ---

func (s *ScriptedStore) RawQueryRow(query string, args ...any) *sql.Row {
	if s.RawQueryRowFunc != nil {
		return s.RawQueryRowFunc(query, args...)
	}
	return s.underlying.RawQueryRow(query, args...)
}
func (s *ScriptedStore) RawQuery(query string, args ...any) (*sql.Rows, error) {
	if s.RawQueryFunc != nil {
		return s.RawQueryFunc(query, args...)
	}
	return s.underlying.RawQuery(query, args...)
}
func (s *ScriptedStore) RawDB() *sql.DB {
	if s.RawDBFunc != nil {
		return s.RawDBFunc()
	}
	return s.underlying.RawDB()
}

// --- Runtime ---

func (s *ScriptedStore) GetRuntimeSettings() ([]byte, error) {
	if s.GetRuntimeSettingsFunc != nil {
		return s.GetRuntimeSettingsFunc()
	}
	return s.underlying.GetRuntimeSettings()
}
func (s *ScriptedStore) SetRuntimeSettings(payload []byte) error {
	if s.SetRuntimeSettingsFunc != nil {
		return s.SetRuntimeSettingsFunc(payload)
	}
	return s.underlying.SetRuntimeSettings(payload)
}

// --- Secrets ---

func (s *ScriptedStore) ReencryptAllKeys(oldMgr, newMgr *secrets.Manager) (int, error) {
	if s.ReencryptAllKeysFunc != nil {
		return s.ReencryptAllKeysFunc(oldMgr, newMgr)
	}
	return s.underlying.ReencryptAllKeys(oldMgr, newMgr)
}
func (s *ScriptedStore) SetSecrets(m *secrets.Manager) {
	if s.SetSecretsFunc != nil {
		s.SetSecretsFunc(m)
		return
	}
	s.underlying.SetSecrets(m)
}
func (s *ScriptedStore) RotateMasterKey(newKeyHex string) (int, error) {
	if s.RotateMasterKeyFunc != nil {
		return s.RotateMasterKeyFunc(newKeyHex)
	}
	return s.underlying.RotateMasterKey(newKeyHex)
}

// --- Lifecycle ---

func (s *ScriptedStore) Ping(ctx context.Context) error {
	if s.PingFunc != nil {
		return s.PingFunc(ctx)
	}
	return s.underlying.Ping(ctx)
}
func (s *ScriptedStore) Close() error {
	if s.CloseFunc != nil {
		return s.CloseFunc()
	}
	return s.underlying.Close()
}

// --- BYOK ---

func (s *ScriptedStore) CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error) {
	if s.CreateBYOKChannelFunc != nil {
		return s.CreateBYOKChannelFunc(ctx, ch)
	}
	return s.underlying.CreateBYOKChannel(ctx, ch)
}
func (s *ScriptedStore) ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error) {
	if s.ListBYOKChannelsFunc != nil {
		return s.ListBYOKChannelsFunc(ctx)
	}
	return s.underlying.ListBYOKChannels(ctx)
}
func (s *ScriptedStore) GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error) {
	if s.GetBYOKChannelFunc != nil {
		return s.GetBYOKChannelFunc(ctx, id)
	}
	return s.underlying.GetBYOKChannel(ctx, id)
}

func (s *ScriptedStore) GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error) {
	if s.GetBYOKChannelByIPFunc != nil {
		return s.GetBYOKChannelByIPFunc(ctx, ownerIP)
	}
	return s.underlying.GetBYOKChannelByIP(ctx, ownerIP)
}

func (s *ScriptedStore) TouchBYOKChannel(ctx context.Context, id int64) error {
	if s.TouchBYOKChannelFunc != nil {
		return s.TouchBYOKChannelFunc(ctx, id)
	}
	return s.underlying.TouchBYOKChannel(ctx, id)
}
func (s *ScriptedStore) DeleteBYOKChannel(ctx context.Context, id int64) error {
	if s.DeleteBYOKChannelFunc != nil {
		return s.DeleteBYOKChannelFunc(ctx, id)
	}
	return s.underlying.DeleteBYOKChannel(ctx, id)
}

func (s *ScriptedStore) GetProviderDefs() ([]model.ProviderDef, error) {
	if s.GetProviderDefsFunc != nil {
		return s.GetProviderDefsFunc()
	}
	return s.underlying.GetProviderDefs()
}
func (s *ScriptedStore) CreateProviderDef(p *model.ProviderDef) error {
	if s.CreateProviderDefFunc != nil {
		return s.CreateProviderDefFunc(p)
	}
	return s.underlying.CreateProviderDef(p)
}
func (s *ScriptedStore) DeleteProviderDef(id int64) error {
	if s.DeleteProviderDefFunc != nil {
		return s.DeleteProviderDefFunc(id)
	}
	return s.underlying.DeleteProviderDef(id)
}
func (s *ScriptedStore) GetComboModels(tokenID int64) ([]model.TokenComboModel, error) {
	if s.GetComboModelsFunc != nil {
		return s.GetComboModelsFunc(tokenID)
	}
	return s.underlying.GetComboModels(tokenID)
}
func (s *ScriptedStore) GetComboModel(id int64) (*model.TokenComboModel, error) {
	if s.GetComboModelFunc != nil {
		return s.GetComboModelFunc(id)
	}
	return s.underlying.GetComboModel(id)
}
func (s *ScriptedStore) GetAllComboModels() ([]model.TokenComboModel, error) {
	if s.GetAllComboModelsFunc != nil {
		return s.GetAllComboModelsFunc()
	}
	return s.underlying.GetAllComboModels()
}

func (s *ScriptedStore) ListAllComboModels() ([]model.TokenComboModel, error) {
	if s.ListAllComboModelsFunc != nil {
		return s.ListAllComboModelsFunc()
	}
	return s.underlying.ListAllComboModels()
}
func (s *ScriptedStore) CreateComboModel(c *model.TokenComboModel) error {
	if s.CreateComboModelFunc != nil {
		return s.CreateComboModelFunc(c)
	}
	return s.underlying.CreateComboModel(c)
}
func (s *ScriptedStore) UpdateComboModel(c *model.TokenComboModel) error {
	if s.UpdateComboModelFunc != nil {
		return s.UpdateComboModelFunc(c)
	}
	return s.underlying.UpdateComboModel(c)
}
func (s *ScriptedStore) DeleteComboModel(id int64) error {
	if s.DeleteComboModelFunc != nil {
		return s.DeleteComboModelFunc(id)
	}
	return s.underlying.DeleteComboModel(id)
}

func (s *ScriptedStore) SetDefaultModelSet(tokenID, comboID int64) error {
	if s.SetDefaultModelSetFunc != nil {
		return s.SetDefaultModelSetFunc(tokenID, comboID)
	}
	return s.underlying.SetDefaultModelSet(tokenID, comboID)
}
func (s *ScriptedStore) GetEnabledGuardrailRules() ([]model.GuardrailRule, error) {
	if s.GetEnabledGuardrailRulesFunc != nil {
		return s.GetEnabledGuardrailRulesFunc()
	}
	return s.underlying.GetEnabledGuardrailRules()
}
func (s *ScriptedStore) GetGuardrailRules() ([]model.GuardrailRule, error) {
	if s.GetGuardrailRulesFunc != nil {
		return s.GetGuardrailRulesFunc()
	}
	return s.underlying.GetGuardrailRules()
}
func (s *ScriptedStore) GetGuardrailRule(id int64) (*model.GuardrailRule, error) {
	if s.GetGuardrailRuleFunc != nil {
		return s.GetGuardrailRuleFunc(id)
	}
	return s.underlying.GetGuardrailRule(id)
}
func (s *ScriptedStore) CreateGuardrailRule(r *model.GuardrailRule) error {
	if s.CreateGuardrailRuleFunc != nil {
		return s.CreateGuardrailRuleFunc(r)
	}
	return s.underlying.CreateGuardrailRule(r)
}
func (s *ScriptedStore) UpdateGuardrailRule(r *model.GuardrailRule) error {
	if s.UpdateGuardrailRuleFunc != nil {
		return s.UpdateGuardrailRuleFunc(r)
	}
	return s.underlying.UpdateGuardrailRule(r)
}
func (s *ScriptedStore) DeleteGuardrailRule(id int64) error {
	if s.DeleteGuardrailRuleFunc != nil {
		return s.DeleteGuardrailRuleFunc(id)
	}
	return s.underlying.DeleteGuardrailRule(id)
}
func (s *ScriptedStore) CreateGuardrailEvent(e *model.GuardrailEvent) error {
	if s.CreateGuardrailEventFunc != nil {
		return s.CreateGuardrailEventFunc(e)
	}
	return s.underlying.CreateGuardrailEvent(e)
}
func (s *ScriptedStore) GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error) {
	if s.GetGuardrailEventsFunc != nil {
		return s.GetGuardrailEventsFunc(tokenID, limit)
	}
	return s.underlying.GetGuardrailEvents(tokenID, limit)
}
