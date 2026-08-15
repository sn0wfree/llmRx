package store

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/model"
)

// ModelCostRepository is the rate-card for per-model pricing. The
// model_costs table is the sole source of truth for what a user is
// billed (channel-level prices no longer participate). Rows are
// seeded from modelmeta on first boot and editable via the admin UI.
type ModelCostRepository interface {
	// GetModelCost returns the rate card for a model, or
	// ErrNotFound when unpriced.
	GetModelCost(model string) (*model.ModelCost, error)
	// ListModelCosts returns all rate cards, ordered by model.
	ListModelCosts() ([]model.ModelCost, error)
	// UpsertModelCost inserts or replaces the row for a model.
	UpsertModelCost(c *model.ModelCost) error
	// DeleteModelCost removes the row for a model. Deleting a model
	// makes it unpriced (requests for it are rejected fail-closed).
	DeleteModelCost(model string) error
	// BatchUpsertModelCosts inserts or updates many rows in one
	// transaction (used by startup seeding / admin import).
	BatchUpsertModelCosts(costs []model.ModelCost) error
	// CountModelCosts returns the number of rows (seeding check).
	CountModelCosts() (int, error)
	// MarkTokenUserID is used by startup backfill to link tokens to
	// their owning user via token.name == users.username.
	BackfillTokenUserIDs(ctx context.Context) (int64, error)
}
