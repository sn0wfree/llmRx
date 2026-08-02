package store

import "github.com/sn0wfree/llmRx/internal/model"

type TokenRepository interface {
	GetToken(key string) (*model.Token, error)
	GetTokenByID(id int64) (*model.Token, error)
	GetTokens() ([]model.Token, error)
	CreateToken(t *model.Token) error
	UpdateToken(t *model.Token) error
	DeleteToken(id int64) error
	IncrementTokenSpend(tokenID int64, amount float64) error
	MarkTokenExpired(tokenID int64) error
}

type PlanRepository interface {
	GetPlans() ([]model.Plan, error)
	GetPlan(id int64) (*model.Plan, error)
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error
	DeletePlan(id int64) error
	IncrementPlanSpend(planID int64, amount float64) error
	RecordRequestSpend(tokenID, planID int64, amount float64) error
}
