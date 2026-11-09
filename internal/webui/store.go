package webui

import (
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// UserLookup is the narrow interface SessionMiddleware needs from the store.
// It extracts only the single method called during session resolution,
// decoupling the middleware from the full store surface.
type UserLookup interface {
	GetUserBySession(token string) (*model.User, error)
}

// WebuiStore is what the HTML console needs from the store: the shared
// store.Reader plus the calls only the console makes.
//
// The shared part lives in internal/store so this package and the admin
// JSON API both depend on the contract instead of on each other.
type WebuiStore interface {
	store.Reader

	// Combo models: the console lists every combo and sets the default
	// one for a token.
	GetAllComboModels() ([]model.TokenComboModel, error)

	ListAllComboModels() ([]model.TokenComboModel, error)

	SetDefaultModelSet(tokenID, comboID int64) error

	// Provider definitions back the console's provider picker.
	GetProviderDefs() ([]model.ProviderDef, error)

	// Tokens: the console resolves a token by name and lists a user's
	// tokens.
	GetTokenByName(name string) (*model.Token, error)

	GetTokensByUserID(userID int64) ([]model.Token, error)
}
