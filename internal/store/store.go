package store

import (
	"github.com/sn0wfree/llmRx/internal/model"
)

type Store interface {
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
	GetToken(key string) (*model.Token, error)
	GetTokens() ([]model.Token, error)
	CreateToken(t *model.Token) error
	DeleteToken(id int64) error

	// Plans
	GetPlans() ([]model.Plan, error)
	GetPlan(id int64) (*model.Plan, error)
	CreatePlan(p *model.Plan) error
	UpdatePlan(p *model.Plan) error

	// Users
	GetUsers() ([]model.User, error)
	GetUser(id int64) (*model.User, error)
	GetUserByUsername(username string) (*model.User, error)
	CreateUser(u *model.User) error
	UpdateUser(u *model.User) error

	// Logs
	CreateLog(l *model.Log) error
	GetLogs(limit, offset int) ([]model.Log, error)
}
