package cache

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type entryWithExp struct {
	entry     *Entry
	expiresAt time.Time
	link      *list.Element
}

type MemoryCache struct {
	mu       sync.Mutex
	items    map[string]*entryWithExp
	lru      *list.List
	maxItems int
	hits     int64
	misses   int64
}

func NewMemoryCache(maxItems int) *MemoryCache {
	if maxItems <= 0 {
		maxItems = 10000
	}
	return &MemoryCache{
		items:    make(map[string]*entryWithExp),
		lru:      list.New(),
		maxItems: maxItems,
	}
}

func (m *MemoryCache) Get(_ context.Context, key string) (*Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.items[key]
	if !ok {
		atomic.AddInt64(&m.misses, 1)
		return nil, false, nil
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		m.lru.Remove(e.link)
		delete(m.items, key)
		atomic.AddInt64(&m.misses, 1)
		return nil, false, nil
	}
	m.lru.MoveToFront(e.link)
	e.entry.HitCount++
	atomic.AddInt64(&m.hits, 1)
	cp := *e.entry
	return &cp, true, nil
}

func (m *MemoryCache) Set(_ context.Context, e *Entry, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.items[e.Key]; ok {
		m.lru.Remove(existing.link)
		delete(m.items, e.Key)
	}

	for m.lru.Len() >= m.maxItems {
		back := m.lru.Back()
		if back == nil {
			break
		}
		delete(m.items, back.Value.(string))
		m.lru.Remove(back)
	}

	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	cp := *e
	cp.StoredAt = time.Now()
	entry := &entryWithExp{
		entry:     &cp,
		expiresAt: exp,
		link:      m.lru.PushFront(e.Key),
	}
	m.items[e.Key] = entry
	return nil
}

func (m *MemoryCache) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.items[key]
	if !ok {
		return nil
	}
	m.lru.Remove(e.link)
	delete(m.items, key)
	return nil
}

func (m *MemoryCache) Purge(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.items = make(map[string]*entryWithExp)
	m.lru = list.New()
	return nil
}

func (m *MemoryCache) Stats(_ context.Context) (Stats, error) {
	hits := atomic.LoadInt64(&m.hits)
	misses := atomic.LoadInt64(&m.misses)
	rate := 0.0
	if hits+misses > 0 {
		rate = float64(hits) / float64(hits+misses)
	}
	m.mu.Lock()
	size := int64(len(m.items))
	m.mu.Unlock()

	return Stats{
		Size:    size,
		Hits:    hits,
		Misses:  misses,
		HitRate: rate,
	}, nil
}

func (m *MemoryCache) Close() error {
	return nil
}
