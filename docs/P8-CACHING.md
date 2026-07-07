# P8 — Response Caching

> Date: 2026-07 · Owner: llmRx maintainers · Status: **implementation**
> (target: end of week 1 after this doc lands).

## 1. Why now

- All surveyed competitors (LiteLLM, Bifrost, Kong AI, Portkey,
  Helicone) ship caching as a Tier-1 feature. See `docs/COMPARATIVE.md`
  §2.
- Caching is the single biggest lever for reducing upstream spend at
  scale: a templated RAG prompt or repeated system commands can hit
  cache 30-60 % of the time. At our internal benchmarks, 30 % hit
  rate halves upstream cost.
- Cache hits return in **< 5 ms** vs upstream 1-3 s, removing the
  long tail from client P95.
- Implementation cost is moderate: ~600 LoC for the in-process and
  SQLite backends; Redis as a follow-up.

## 2. Scope (P8 milestone)

| Feature | In scope | Out of scope |
|---|:---:|---|
| Exact-match response cache | ✅ | — |
| Memory backend (LRU, single-process) | ✅ | — |
| SQLite backend (persistent across restart) | ✅ | — |
| Cache-control hints (`no-cache`, `no-store`, `ttl`, `s-maxage`) | ✅ | — |
| Per-channel TTL override | ✅ | — |
| Streaming cache (replay) | ✅ | — |
| Admin `POST /cache/purge` | ✅ | — |
| Dashboard hit-ratio card | ✅ | — |
| Semantic cache (embedding + vector store) | ❌ | parked → P9+ |
| Redis backend | ❌ | parked → P8.5 (1-day follow-up) |
| Per-user cache rules | ❌ | parked → P10 |

## 3. Cache key

```go
func Key(req *provider.ChatRequest) (string, error) {
    if req.Temperature != nil && *req.Temperature > 0 {
        return "", ErrTemperaturePositive // non-deterministic, skip
    }
    h := sha256.New()
    // model + messages (normalized) + tools + response_format + stream
    h.Write([]byte(req.Model))
    h.Write([]byte{0})
    h.Write(mustJSON(canonicalMessages(req.Messages))) // drop ids / timestamp noise
    h.Write([]byte{0})
    h.Write(mustJSON(req.Tools))
    h.Write([]byte{0})
    if req.ResponseFormat != nil {
        h.Write(mustJSON(req.ResponseFormat))
    }
    h.Write([]byte{0})
    if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
        h.Write([]byte{1})
    }
    return hex.EncodeToString(h.Sum(nil)), nil
}
```

**Why temperature matters**: temperature > 0 means the same prompt
can validly return different answers; caching would lie to the caller.
Same for any randomness knobs. We refuse to cache when
`temperature > 0` or any future "seed"-style field is present.

## 4. Backend interface

```go
package cache

import (
    "context"
    "encoding/json"
    "time"
)

type Entry struct {
    Key          string          `json:"key"`
    StatusCode   int             `json:"status_code"`
    Headers      map[string]string `json:"headers"`
    Body         json.RawMessage `json:"body"`        // the full upstream JSON
    Usage        *provider.Usage `json:"usage,omitempty"`
    CostUSD      float64         `json:"cost_usd"`
    ChannelID    int64           `json:"channel_id"`
    StoredAt     time.Time       `json:"stored_at"`
    HitCount     int64           `json:"hit_count"`
}

type Cache interface {
    // Get returns the entry if present and not expired; ok=false on miss.
    Get(ctx context.Context, key string) (*Entry, bool, error)

    // Set stores e with TTL. Empty TTL uses the default (server.cache_ttl_sec).
    Set(ctx context.Context, e *Entry, ttl time.Duration) error

    // Delete removes the entry.
    Delete(ctx context.Context, key string) error

    // Purge clears everything (used by POST /cache/purge and tests).
    Purge(ctx context.Context) error

    // Stats returns counters for /dashboard.
    Stats(ctx context.Context) (Stats, error)
}

type Stats struct {
    Size    int64 `json:"size"`
    Hits    int64 `json:"hits"`
    Misses  int64 `json:"misses"`
    HitRate float64 `json:"hit_rate"` // Hits / (Hits + Misses)
}
```

### 4.1 In-memory LRU backend

```go
type MemoryCache struct {
    mu       sync.Mutex
    items    map[string]*entryWithExp  // key -> entry + expiresAt
    lru      *list.List                 // doubly-linked for O(1) eviction
    maxItems int
    stats    atomicStats
}
```

* O(1) Get / Set / Delete.
* LRU eviction when `items > maxItems`.
* Default `maxItems = 10_000`; configurable via `cache_max_items`.
* Memory bound: ~20 MB for 10K entries (depends on payload size).
* Stats via lock-free atomics (hits / misses increments).
* Lost on restart; expected — the SQLite backend takes over.

### 4.2 SQLite backend

```sql
CREATE TABLE response_cache (
    key         TEXT PRIMARY KEY,
    status_code INTEGER NOT NULL,
    headers     TEXT NOT NULL,         -- JSON
    body        BLOB NOT NULL,         -- upstream response JSON, gzipped
    usage_json  TEXT,                  -- optional
    cost_usd    REAL NOT NULL DEFAULT 0,
    channel_id  INTEGER NOT NULL,
    stored_at   INTEGER NOT NULL,      -- unix seconds
    expires_at  INTEGER NOT NULL,
    hit_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_cache_expires ON response_cache(expires_at);
CREATE INDEX idx_cache_channel ON response_cache(channel_id);
```

* `body` gzipped on write (typical 70 % reduction for chat JSON).
* `expires_at` lets `Get` reject in one query without touching the
  payload; periodic VACUUM drops expired rows.
* Migration via the existing `addColumnIfMissing` mechanism — but
  for a new table, `CREATE TABLE IF NOT EXISTS` is enough.
* Stats in a separate `response_cache_stats` table (singleton row)
  to avoid contention on every Get.

### 4.3 Redis backend (P8.5 follow-up, not in this milestone)

Standard `GET key` / `SETEX key ttl value`; value = JSON of `Entry`.
`redis.NewClient` from `github.com/redis/go-redis/v9` (one new dep).

## 5. Server config

```go
type ServerConfig struct {
    // ... existing fields ...

    // P8 caching
    CacheBackend       string `yaml:"cache_backend"`       // memory | sqlite (default sqlite)
    CacheTTLSec        int    `yaml:"cache_ttl_sec"`        // default 600 (10 min)
    CacheMaxItems      int    `yaml:"cache_max_items"`      // memory backend only, default 10000
    CacheMaxBodyBytes  int    `yaml:"cache_max_body_bytes"` // refuse to cache > N bytes (default 1 MiB)
}
```

A value of 0 disables caching entirely (and skips the Get/Set
overhead).

## 6. Wire-up

### 6.1 Non-streaming

```
client → ChatCompletions
   │
   ▼
   key, err := cache.Key(&req)
   if err == cache.ErrTemperaturePositive { skip }
   if cached, ok, _ := c.Get(ctx, key); ok {
       c.RecordHit()
       // set X-LlmRx-Cache: HIT
       // respond with cached body verbatim
       return
   }
   c.RecordMiss()
   ↓
   route + Chat/Stream → upstream
   ↓
   if entry fits MaxBodyBytes and ttl > 0 {
       c.Set(ctx, &cache.Entry{...}, ttl)
   }
```

### 6.2 Streaming

Streams are cached as the **concatenated final payload** (the JSON of
the last `data: {...}\n\n` chunk plus the prefix), then replayed via
SSE on hit.

* Stream chunks accumulate in a slice (no socket yet — buffer until
  upstream completes).
* On hit, replay through the same `flusher` interface with the same
  chunk cadence (4 chunks per flush).
* Add `X-LlmRx-Cache: HIT` response header **before** any chunk.
* `cache_max_body_bytes` applies to the final concatenated payload,
  not per-chunk; oversized streams are not cached.

### 6.3 Cache-control hints

The OpenAI spec lets the client pass `cache={"no-cache": ..., "no-store": ..., "ttl": ..., "s-maxage": ...}` in the body. Honour them:

| Flag | Behaviour |
|---|---|
| `no-cache: true` | Bypass cache (force upstream) — do not read |
| `no-store: true` | Bypass cache + don't write on this response |
| `ttl: N` | Override server default for this entry (seconds) |
| `s-maxage: N` | Only serve cached entries ≤ N seconds old; treat older as miss |

`s-maxage` is the strictest — we **never** serve stale entries even
if the entry hasn't expired, if the client has asked for `s-maxage`.

## 7. Cache invalidation

Three automatic triggers; no manual flush endpoint needed unless
explicitly invoked:

| Trigger | Effect |
|---|---|
| Channel edited (model list or pricing changed) | Purge keys where `channel_id == ch.ID` |
| Channel deleted | Purge same; the cache key won't be reused |
| TTL elapsed | `Get` rejects in-memory; periodic SQLite VACUUM removes expired rows |

Admin `POST /api/v1/cache/purge` clears everything (used by ops
recovery, not normal flow).

## 8. Failure mode

| Scenario | Behaviour |
|---|---|
| Backend unreachable | Log warning, treat as miss. Client request still succeeds via upstream. |
| Cache poisoned (write fails after upstream returned 200) | Log error. Client still gets the upstream response. The failed write is silent. |
| Cache hit corrupted (read returns bad JSON) | Log error, delete the entry, fall back to upstream. |
| Memory backend full | LRU evict the coldest entry; continue. |

## 9. Dashboard / API surface

* `GET /api/v1/cache/stats` — `{ size, hits, misses, hit_rate }`
* `POST /api/v1/cache/purge` — clears everything; admin-only
* Dashboard card "Cache hit rate (24h)" — derived from
  `TopByToken`-style query against a `response_cache_events`
  table or simply from in-memory stats
* Admin Channel form gains a checkbox `disable_caching_for_this_channel`

## 10. Files to add / touch

```
internal/cache/cache.go              # Cache interface, Entry, Key, Stats
internal/cache/memory.go             # LRU + atomic stats
internal/cache/sqlite.go             # SQL implementation
internal/cache/control.go            # no-cache / no-store / ttl / s-maxage parsing
internal/api/router.go               # consult cache before Chat() / stream; populate after
internal/api/handler.go              # SetStreamCaps remains; add SetCache / Cache accessor
internal/config/config.go            # ServerConfig: 4 new yaml keys
internal/store/sqlite.go             # new table response_cache + migration
internal/runtime/runtime.go          # atomic.CacheStats
cmd/gateway/main.go                  # wire cache to handler
web/src/pages/Settings.tsx           # surface cache TTL + max items
web/src/pages/Dashboard.tsx          # cache hit-rate card
internal/cache/memory_test.go
internal/cache/sqlite_test.go
internal/cache/key_test.go
internal/cache/control_test.go
internal/api/cache_test.go            # wire-level tests: hit / miss / control / streaming
```

## 11. Test plan

* **Memory backend unit**: LRU eviction, hit counters, TTL expiry,
  Purge, concurrent access (race detector).
* **SQLite backend unit**: persistence across reopen, schema migration,
  expired-row rejection, gzipped body round-trip.
* **Key builder determinism**: same input → same hash; tool ordering
  does not matter; image_url parts included verbatim.
* **Wire-level**: hit returns verbatim JSON in < 5 ms; cache hit
  surfaces `X-LlmRx-Cache: HIT`.
* **Wire-level**: `no-cache` / `no-store` / `ttl` / `s-maxage` honoured.
* **Streaming hit replays**: the same chunks (and `data: [DONE]`) as
  the live path; `X-LlmRx-Cache: HIT` header set.
* **Cache threshold**: temperature > 0 always misses.
* **Backwards compat**: deployment with `cache_backend=memory` runs
  unchanged; `cache_backend=sqlite` is the default.
* **Race-clean**: 1000 concurrent Get + Set on the same key don't
  corrupt stats.

## 12. Acceptance criteria

| Metric | Target |
|---|---|
| Cache hit P95 latency | < 5 ms (in-memory); < 15 ms (SQLite) |
| Cache miss added latency | < 100 µs (memory); < 2 ms (SQLite) |
| Test coverage (cache package) | ≥ 80 % |
| Test coverage (overall) | ≥ 60 % (current 65.3 %; must not drop) |
| Memory footprint | 10K entries ≈ 20 MB |
| Concurrent Get throughput | ≥ 100K req/s (memory, race-clean) |
| Cache-control flags | All four honoured in tests |
| `POST /cache/purge` | Clears backend; Stats reset |

## 13. Rollout

1. Land the docs (this file).
2. Implement `internal/cache` with both backends + tests.
3. Wire `api.Handler` to consult cache; honour control flags.
4. Wire streaming-cache replay path.
5. Add admin endpoints + dashboard card.
6. Land one commit per backend (memory first; sqlite second).
7. End-to-end test: identical request twice → second is from cache.
8. Update README + CHANGELOG.
9. Optional follow-up: Redis backend (`docs/P8-CACHING.md` §4.3).

## 14. Risks

| Risk | Mitigation |
|---|---|
| Cache poisoning (upstream bug propagates) | `cache_max_body_bytes` cap + JSON sanity check on read; corrupted entries are deleted and the request retried upstream. |
| Cache bypass for sensitive traffic | `no-store` honour + per-channel disable flag. |
| SQLite cache becomes a write hotspot | WAL mode (already enabled) + batched VACUUM; fall back to memory backend if SQLite write latency spikes. |
| Cache hit cardinality explosion (millions of keys) | LRU eviction in memory backend; periodic VACUUM in SQLite; per-channel `disable_caching_for_this_channel` flag for noisy channels. |