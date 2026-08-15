// Package modelcost is a thread-safe in-memory lookup table for the
// model rate card (the model_costs table). Reads are O(1); reloads
// happen on demand after admin edits so billing always uses fresh
// prices without hitting the DB on the hot path.
package modelcost

import (
	"fmt"
	"sync"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Source is the narrow store contract the cache needs.
type Source interface {
	ListModelCosts() ([]model.ModelCost, error)
}

// Cache holds the rate card keyed by model name.
type Cache struct {
	mu    sync.RWMutex
	items map[string]model.ModelCost
	store Source
}

func New(st Source) *Cache {
	c := &Cache{items: make(map[string]model.ModelCost), store: st}
	if st == nil {
		// Tests / early construction: no store yet, empty rate card.
		return c
	}
	// Initial load errors are swallowed; the gateway bootstrap calls
	// Reload explicitly and treats failure as fatal.
	_ = c.Reload()
	return c
}

// Reload re-reads the full rate card and swaps the table. A read
// failure preserves the previous snapshot.
func (c *Cache) Reload() error {
	costs, err := c.store.ListModelCosts()
	if err != nil {
		return fmt.Errorf("modelcost reload: %w", err)
	}
	next := make(map[string]model.ModelCost, len(costs))
	for _, mc := range costs {
		next[mc.Model] = mc
	}
	c.mu.Lock()
	c.items = next
	c.mu.Unlock()
	return nil
}

// Lookup returns the rate card for a model. ok=false means the model
// is unpriced and requests for it must be rejected fail-closed.
func (c *Cache) Lookup(model string) (model.ModelCost, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	mc, ok := c.items[model]
	return mc, ok
}

// Size returns the number of priced models.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}
