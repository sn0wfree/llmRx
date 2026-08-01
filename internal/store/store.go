package store

import (
	"context"
	"database/sql"
)

type Store interface {
	ChannelRepository
	KeyRepository
	TokenRepository
	PlanRepository
	UserRepository
	AlertRepository
	GuardrailRepository
	BYOKRepository
	ProviderDefRepository
	ComboModelRepository
	RuntimeRepository
	SecurityRepository

	Ping(ctx context.Context) error
	Close() error

	RawQueryRow(query string, args ...any) *sql.Row
	RawQuery(query string, args ...any) (*sql.Rows, error)
}

type DrainedChannel struct {
	ID   int64
	Name string
}