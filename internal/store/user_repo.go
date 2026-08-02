package store

import "github.com/sn0wfree/llmRx/internal/model"

type UserRepository interface {
	GetUsers() ([]model.User, error)
	GetUser(id int64) (*model.User, error)
	GetUserByUsername(username string) (*model.User, error)
	GetUserBySession(token string) (*model.User, error)
	CreateUser(u *model.User) error
	UpdateUser(u *model.User) error
	CleanupExpiredSessions() (int64, error)
}
