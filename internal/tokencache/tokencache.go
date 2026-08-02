package tokencache

import (
	"fmt"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
)

// Cache is a thread-safe lookup table for active API tokens, backed
// by the store. Reads are O(1); reloads happen on demand.
type Cache struct {
	mu    sync.RWMutex
	items map[string]middleware.TokenInfo
	store TokenSource
	// expirer, when non-nil, is called with the token ID for any
	// token observed past its ExpiresAt. This lets the cache
	// opportunistically flip Status=TokenExpired in the store so
	// later reloads do not re-process the same row.
	expirer func(tokenID int64) error
}

// TokenSource is the narrow contract the cache depends on; the
// production store satisfies it via its GetTokens and GetPlan
// methods. GetPlan is used to join the plan's budget/used snapshot
// onto each TokenInfo so the middleware can enforce plan budgets
// without touching the database on the hot path. GetAllComboModels
// returns all enabled combos in one query (batch-loaded) so the
// cache avoids N+1 SQL round-trips during Reload.
type TokenSource interface {
	GetTokens() ([]model.Token, error)
	GetPlan(id int64) (*model.Plan, error)
	GetAllComboModels() ([]model.TokenComboModel, error)
}

func New(st TokenSource) *Cache {
	c := &Cache{items: make(map[string]middleware.TokenInfo), store: st}
	// Initial Reload errors are swallowed so callers that don't care
	// about plan-join failures still get a working cache. Callers that
	// DO care (e.g. the gateway bootstrap) should call Reload()
	// explicitly and handle the error themselves.
	_ = c.Reload()
	return c
}

// SetExpirer installs a callback invoked when Reload observes a
// token whose ExpiresAt is in the past. The store typically uses
// this to flip Status to TokenExpired so future reloads skip the
// row. Safe to set after construction; only consulted on the next
// Reload.
func (c *Cache) SetExpirer(fn func(tokenID int64) error) { c.expirer = fn }

func (c *Cache) Reload() error {
	toks, err := c.store.GetTokens()
	if err != nil {
		return err
	}

	// Batch-load all enabled combos in one query to avoid N+1.
	combos, _ := c.store.GetAllComboModels() // non-fatal: DB may not have the table yet
	comboMap := make(map[int64][]model.TokenComboModel, len(combos))
	for _, cb := range combos {
		comboMap[cb.TokenID] = append(comboMap[cb.TokenID], cb)
	}

	now := time.Now()
	next := make(map[string]middleware.TokenInfo, len(toks))
	for _, t := range toks {
		if t.Status != 0 { // TokenActive == 0
			continue
		}
		if !t.ExpiresAt.IsZero() && now.After(t.ExpiresAt) {
			if c.expirer != nil {
				_ = c.expirer(t.ID)
			}
			continue
		}
		info := middleware.TokenInfo{
			ID:              t.ID,
			Key:             t.Key,
			Name:            t.Name,
			PlanID:          t.PlanID,
			RPM:             t.RPM,
			TPM:             t.TPM,
			ModelsWhitelist: t.ModelsWhitelist,
			IPWhitelist:     t.IPWhitelist,
			ExpiresAt:       t.ExpiresAt,
		}
		if cmbs, ok := comboMap[t.ID]; ok {
			info.ComboModels = make(map[string]model.TokenComboModel, len(cmbs))
			for _, cb := range cmbs {
				info.ComboModels[cb.Name] = cb
			}
		}
		next[t.Key] = info
	}
	// Plan budgets are joined in a second pass so a token whose
	// plan has been updated between cache reloads sees the fresh
	// used_usd / budget_usd snapshot.
	//
	// Fail-closed: if GetPlan errors or returns nil for a plan
	// referenced by an active token, Reload returns an error and
	// the previous cache is preserved. Otherwise a transient DB
	// blip would silently downgrade bound tokens to "unlimited"
	// and let un-budgeted spend through.
	budgets := map[int64][3]float64{}
	referenced := map[int64]struct{}{}
	for _, info := range next {
		if info.PlanID == 0 {
			continue
		}
		referenced[info.PlanID] = struct{}{}
	}
	for pid := range referenced {
		if _, ok := budgets[pid]; ok {
			continue
		}
		p, perr := c.store.GetPlan(pid)
		if perr != nil {
			return fmt.Errorf("load plan %d: %w", pid, perr)
		}
		if p == nil {
			return fmt.Errorf("plan %d not found (referenced by an active token)", pid)
		}
		// Cache [budget, used, markup] so the chat handler can compute
		// billedCost without a per-request store.GetPlan() roundtrip.
		budgets[pid] = [3]float64{p.BudgetUSD, p.UsedUSD, p.MarkupRatio}
	}
	for k, info := range next {
		if b, ok := budgets[info.PlanID]; ok {
			info.PlanBudgetUSD = b[0]
			info.PlanUsedUSD = b[1]
			info.PlanMarkupRatio = b[2]
			next[k] = info
		}
	}
	c.mu.Lock()
	c.items = next
	c.mu.Unlock()
	return nil
}

func (c *Cache) Lookup(key string) (middleware.TokenInfo, bool) {
	c.mu.RLock()
	info, ok := c.items[key]
	c.mu.RUnlock()
	return info, ok
}

func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}
