package tokencache

import (
	"sync"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Cache is a thread-safe lookup table for active API tokens, backed
// by the store. Reads are O(1); reloads happen on demand.
type Cache struct {
	mu    sync.RWMutex
	items map[string]middleware.TokenInfo
	store store.Store
}

func New(st store.Store) *Cache {
	c := &Cache{items: make(map[string]middleware.TokenInfo), store: st}
	_ = c.Reload()
	return c
}

func (c *Cache) Reload() error {
	toks, err := c.store.GetTokens()
	if err != nil {
		return err
	}
	next := make(map[string]middleware.TokenInfo, len(toks))
	for _, t := range toks {
		if t.Status != 0 { // TokenActive == 0
			continue
		}
		next[t.Key] = middleware.TokenInfo{ID: t.ID, Key: t.Key, Name: t.Name}
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