package store

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/model"
)

// RedeemRepository covers invite-code registration and top-up code
// redemption. All consume paths are transactional: a code is marked
// used atomically with the effect it grants.
type RedeemRepository interface {
	// CreateRedeemCode inserts one redeem code (admin generated).
	CreateRedeemCode(c *model.RedeemCode) error
	// GetRedeemCodeByCode returns a code by its string form.
	GetRedeemCodeByCode(code string) (*model.RedeemCode, error)
	// ListRedeemCodes returns all codes, newest first.
	ListRedeemCodes() ([]model.RedeemCode, error)
	// UseInviteCode consumes an invite code and creates the user,
	// a dedicated bearer token and a dedicated plan with the code's
	// InitialQuota. The code is marked used in the same transaction;
	// a concurrent redemption fails via the used_at check.
	//
	// Returns the created user (with plaintext Key on the token
	// returned via the result), the token and the plan.
	UseInviteCode(ctx context.Context, code string, username, passwordHash string) (*RegistrationResult, error)
	// RedeemTopup adds amount_usd to the plan's budget and marks the
	// code used, atomically. Returns the new plan.
	RedeemTopup(ctx context.Context, code string, planID int64) (*model.Plan, error)
}

// RegistrationResult is the outcome of UseInviteCode: everything a
// newly registered user needs to start calling the API.
type RegistrationResult struct {
	User  *model.User  `json:"user"`
	Token *model.Token `json:"token"`
	Plan  *model.Plan  `json:"plan"`
}
