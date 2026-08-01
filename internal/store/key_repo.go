package store

import "github.com/sn0wfree/llmRx/internal/model"

type KeyRepository interface {
	GetKeys(channelID int64) ([]model.Key, error)
	CreateKey(k *model.Key) error
	DeleteKey(id int64) error
	WipeKeys() (int64, error)
}