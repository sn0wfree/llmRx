package store

import "github.com/sn0wfree/llmRx/internal/model"

type ChannelRepository interface {
	GetChannels() ([]model.Channel, error)
	GetChannel(id int64) (*model.Channel, error)
	CreateChannel(ch *model.Channel) error
	UpdateChannel(ch *model.Channel) error
	DeleteChannel(id int64) error
	GetDrainedChannels() ([]DrainedChannel, error)
}
