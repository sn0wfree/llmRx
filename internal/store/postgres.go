package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

// Postgres is a skeleton Store implementation backed by PostgreSQL.
// Every method returns errNotImplemented; this exists to validate
// that the Store interface compiles against a non-SQLite backend
// and to serve as a starting point for a real implementation.
type Postgres struct {
	db *sql.DB
}

// OpenPostgres opens a PostgreSQL connection and returns a Store
// backed by it. The DSN is passed directly to database/sql.Open.
func OpenPostgres(dsn string) (*Postgres, error) {
	if dsn == "" {
		return nil, errors.New("empty dsn")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	return &Postgres{db: db}, nil
}

func (p *Postgres) Ping(ctx context.Context) error                  { return p.db.PingContext(ctx) }
func (p *Postgres) Close() error                                    { return p.db.Close() }
func (p *Postgres) GetChannels() ([]model.Channel, error)           { return nil, errNotImplemented }
func (p *Postgres) GetChannel(id int64) (*model.Channel, error)     { return nil, errNotImplemented }
func (p *Postgres) CreateChannel(ch *model.Channel) error           { return errNotImplemented }
func (p *Postgres) UpdateChannel(ch *model.Channel) error           { return errNotImplemented }
func (p *Postgres) DeleteChannel(id int64) error                    { return errNotImplemented }
func (p *Postgres) GetDrainedChannels() ([]DrainedChannel, error)   { return nil, errNotImplemented }
func (p *Postgres) GetKeys(channelID int64) ([]model.Key, error)    { return nil, errNotImplemented }
func (p *Postgres) CreateKey(k *model.Key) error                    { return errNotImplemented }
func (p *Postgres) DeleteKey(id int64) error                        { return errNotImplemented }
func (p *Postgres) WipeKeys() (int64, error)                        { return 0, errNotImplemented }
func (p *Postgres) GetToken(key string) (*model.Token, error)       { return nil, errNotImplemented }
func (p *Postgres) GetTokenByID(id int64) (*model.Token, error)     { return nil, errNotImplemented }
func (p *Postgres) GetTokens() ([]model.Token, error)               { return nil, errNotImplemented }
func (p *Postgres) CreateToken(t *model.Token) error                { return errNotImplemented }
func (p *Postgres) UpdateToken(t *model.Token) error                { return errNotImplemented }
func (p *Postgres) DeleteToken(id int64) error                      { return errNotImplemented }
func (p *Postgres) IncrementTokenSpend(tokenID int64, amount float64) error {
	return errNotImplemented
}
func (p *Postgres) IncrementPlanSpend(planID int64, amount float64) error {
	return errNotImplemented
}
func (p *Postgres) MarkTokenExpired(tokenID int64) error            { return errNotImplemented }
func (p *Postgres) RecordRequestSpend(tokenID, planID int64, amount float64) error {
	return errNotImplemented
}
func (p *Postgres) GetPlans() ([]model.Plan, error)                 { return nil, errNotImplemented }
func (p *Postgres) GetPlan(id int64) (*model.Plan, error)           { return nil, errNotImplemented }
func (p *Postgres) CreatePlan(plan *model.Plan) error                  { return errNotImplemented }
func (p *Postgres) UpdatePlan(plan *model.Plan) error                  { return errNotImplemented }
func (p *Postgres) DeletePlan(id int64) error                       { return errNotImplemented }
func (p *Postgres) GetUsers() ([]model.User, error)                 { return nil, errNotImplemented }
func (p *Postgres) GetUser(id int64) (*model.User, error)           { return nil, errNotImplemented }
func (p *Postgres) GetUserByUsername(username string) (*model.User, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetUserBySession(token string) (*model.User, error) {
	return nil, errNotImplemented
}
func (p *Postgres) CreateUser(user *model.User) error                  { return errNotImplemented }
func (p *Postgres) UpdateUser(user *model.User) error                  { return errNotImplemented }
func (p *Postgres) CleanupExpiredSessions() (int64, error)          { return 0, errNotImplemented }
func (p *Postgres) GetAlerts() ([]model.Alert, error)               { return nil, errNotImplemented }
func (p *Postgres) GetAlert(id int64) (*model.Alert, error)         { return nil, errNotImplemented }
func (p *Postgres) CreateAlert(alert *model.Alert) error                { return errNotImplemented }
func (p *Postgres) UpdateAlert(alert *model.Alert) error                { return errNotImplemented }
func (p *Postgres) DeleteAlert(id int64) error                      { return errNotImplemented }
func (p *Postgres) RecordAlertFired(id int64, atUnix int64) error   { return errNotImplemented }
func (p *Postgres) DisableAlert(id int64, reason string) error      { return errNotImplemented }
func (p *Postgres) GetAlertEvents(limit int) ([]model.AlertEvent, error) {
	return nil, errNotImplemented
}
func (p *Postgres) CreateAlertEvent(event *model.AlertEvent) error      { return errNotImplemented }
func (p *Postgres) AckAlertEvent(id int64) error                    { return errNotImplemented }
func (p *Postgres) RawQueryRow(query string, args ...any) *sql.Row  { return nil }
func (p *Postgres) RawQuery(query string, args ...any) (*sql.Rows, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetRuntimeSettings() ([]byte, error)             { return nil, errNotImplemented }
func (p *Postgres) SetRuntimeSettings(payload []byte) error         { return errNotImplemented }
func (p *Postgres) ReencryptAllKeys(oldMgr, newMgr *secrets.Manager) (int, error) {
	return 0, errNotImplemented
}
func (p *Postgres) SetSecrets(m *secrets.Manager)                   {}
func (p *Postgres) RotateMasterKey(newKeyHex string) (int, error)   { return 0, errNotImplemented }
func (p *Postgres) CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error) {
	return 0, errNotImplemented
}
func (p *Postgres) ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error) {
	return nil, errNotImplemented
}
func (p *Postgres) TouchBYOKChannel(ctx context.Context, id int64) error {
	return errNotImplemented
}
func (p *Postgres) DeleteBYOKChannel(ctx context.Context, id int64) error {
	return errNotImplemented
}
func (p *Postgres) GetProviderDefs() ([]model.ProviderDef, error)   { return nil, errNotImplemented }
func (p *Postgres) CreateProviderDef(def *model.ProviderDef) error   { return errNotImplemented }
func (p *Postgres) DeleteProviderDef(id int64) error                { return errNotImplemented }
func (p *Postgres) GetComboModels(tokenID int64) ([]model.TokenComboModel, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetComboModel(id int64) (*model.TokenComboModel, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetAllComboModels() ([]model.TokenComboModel, error) {
	return nil, errNotImplemented
}
func (p *Postgres) CreateComboModel(combo *model.TokenComboModel) error {
	return errNotImplemented
}
func (p *Postgres) UpdateComboModel(combo *model.TokenComboModel) error {
	return errNotImplemented
}
func (p *Postgres) DeleteComboModel(id int64) error                 { return errNotImplemented }
func (p *Postgres) GetEnabledGuardrailRules() ([]model.GuardrailRule, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetGuardrailRules() ([]model.GuardrailRule, error) {
	return nil, errNotImplemented
}
func (p *Postgres) GetGuardrailRule(id int64) (*model.GuardrailRule, error) {
	return nil, errNotImplemented
}
func (p *Postgres) CreateGuardrailRule(rule *model.GuardrailRule) error {
	return errNotImplemented
}
func (p *Postgres) UpdateGuardrailRule(rule *model.GuardrailRule) error {
	return errNotImplemented
}
func (p *Postgres) DeleteGuardrailRule(id int64) error              { return errNotImplemented }
func (p *Postgres) CreateGuardrailEvent(event *model.GuardrailEvent) error {
	return errNotImplemented
}
func (p *Postgres) GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error) {
	return nil, errNotImplemented
}

func init() {
	Register("postgres", func(dsn string) (Store, error) {
		return OpenPostgres(dsn)
	})
}
