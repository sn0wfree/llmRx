package store

import (
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

type ChannelRepository interface {
	GetChannels() ([]model.Channel, error)
	GetChannel(id int64) (*model.Channel, error)
	CreateChannel(ch *model.Channel) error
	UpdateChannel(ch *model.Channel) error
	DeleteChannel(id int64) error
	GetDrainedChannels() ([]DrainedChannel, error)
	RecordChannelSpend(channelID int64, cost float64) error
	// UpdateChannelBalance stores the latest upstream balance snapshot
	// (P14). A non-empty balanceErr records a failed query and leaves
	// the previous balance in place; an empty one clears the error.
	// Deliberately does not touch updated_at so an hourly refresh does
	// not make every channel look freshly edited in the admin UI.
	UpdateChannelBalance(channelID int64, balance, currency string, checkedAt time.Time, balanceErr string) error
}
