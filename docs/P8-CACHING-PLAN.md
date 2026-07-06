# P8 — Response Caching: Plan

> Date: 2026-07 · Owner: llmRx maintainers · Goal: ship two
> caches that map to the LiteLLM tiers:
>
> 1. **Exact-match response cache** (high hit rate for trivial
>    queries / templated prompts) — Redis or in-memory first; disk
>    later.
> 2. **Semantic cache** (similarity-based hit) — vector store with
>    cos-sim threshold; portable embedding function.
>
> This plan covers **exact-match cache only** as the P8 milestone.
> Semantic cache is documented but parked for P9 (it requires a
> vector store which is a non-trivial dependency).

## 1. Why now

* `LiteLLM`, `Bifrost`, `Kong AI`, `Portkey`, `Helicone` all ship
  caching as a tier-1 feature (`docs/COMPARATIVE.md:§4`).
* Caching is the single biggest lever to reduce upstream spend at
  scale: a templated RAG prompt or repeated system commands can hit
  cache 30-60% of the time.
* Implementation cost is moderate: ~600 LoC for exact-match + a small
  optional Redis backend.

## 2. Non-goals (deferred to later phases)

* ❌ Semantic cache (P9)
* ❌ Per-token billing impact (P10)
* ❌ OpenAI `prompt_cache_key` integration (P11)
* ❌ Cross-instance consistency (P12+)

## 3. Design

### 3.1 Cache key

```
key = sha256(
    model                          // exact model name
  + JSON(messages_no_ids)         // drop any message-id / trace-id noise
  + JSON(stream_options)           // stream=true changes response shape
  + temperature_or_default         // float, 4 decimal places
  + top_p_or_default
  + tool_signature                 // empty if no tools; else sorted
  + response_format_or_null        // 'text' vs 'json_object' vs 'schema'
).hex()
```

`temperature=0` matters for repeatability; `temperature > 0` means
the same prompt can validly return different answers, so we **don't**
cache by default when temperature > 0.

### 3.2 Backend interface

```go
type Cache interface {
    Get(ctx context.Context, key string) (*Entry, bool, error)
    Set(ctx context.Context, key string, e *Entry, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Size(ctx context.Context) (int64, error)  // for /metrics
}

type Entry struct {
    Response    json.RawMessage  // the full upstream JSON
    Usage       provider.Usage
    CostUSD     float64
    ChannelID   int64
    StoredAt    time.Time
    HitCount    int
}
```

Three backends:

| Backend | Use case | LoC |
|---|---|---|
| `Memory` (LRU 10K entries) | single-binary dev, < 100 RPS | ~150 |
| `Redis` | production multi-instance, 1000+ RPS | ~150 |
| `SQLite` | single-binary with persistence across restart | ~100 |

All three implement the same interface; production wires the SQLite
implementation today, Redis as a second backend, no extra process.

### 3.3 Server config

```go
type ServerConfig struct {
    // ...
    CacheBackend string         `yaml:"cache_backend"` // memory | redis | sqlite (default sqlite)
    CacheTTLSec  int            `yaml:"cache_ttl_sec"` // default 600 (10 min)
    CacheMaxRPS  int            `yaml:"cache_max_rps"` // 0 = unlimited
    CacheRedisAddr string       `yaml:"cache_redis_addr"` // host:port
    CacheRedisPassword string   `yaml:"cache_redis_password"`
    CacheRedisDB int           `yaml:"cache_redis_db"` // default 0
}
```

### 3.4 Wire-up

```
   client ──▶ ChatCompletions
                │
                ▼
        cache.Get(key) ?
                │
        ┌───────┴────────┐
        │ hit            │ miss
        ▼                ▼
   write cached     route + Chat/Stream
   JSON back            │
        ▲               ▼
        │           cache.Set(key, resp)
        └───────────────┘
```

### 3.5 Cache-control headers (OpenAI spec)

Forward three cache-control hints from the client request body:

| Header / Field | Effect |
|---|---|
| `cache={"no-cache": true}` | bypass cache; do not store |
| `cache={"no-store": true}` | bypass AND don't store on hit |
| `cache={"ttl": N}` | override TTL for this entry |
| `cache={"s-maxage": N}` | only serve cached responses ≤ N old |

(Honour `s-maxage` strictly; the other three are simple switches.)

### 3.6 Streaming semantics

* Streaming responses are cached as the **concatenated final
  payload** (the JSON of the last `data: {…}\n\n` chunk, plus
  the prefix).
* On hit, replay the cached chunks via SSE with the same flush
  cadence as the live path. First event still acknowledges with
  200 OK.
* Add an `X-LlmRx-Cache: HIT` response header before any chunk.

### 3.7 Tenant + global counters

* Each `Entry.HitCount` increments atomically; expose via `/metrics`
  or the admin Dashboard.
* Per-token cache stats (hit / miss / size) feed into `L3 cost` so
  the gateway can show cost-saved numbers.

## 4. Cache invalidation

Three triggers, all automatic — no manual flush endpoint:

1. **Channel edit** (model list changed) → purge keys that contain
   those model names.
2. **Channel delete** → purge all keys for that channel (match
   via stored `ChannelID`).
3. **TTL** — every entry has `StoredAt`; reads older than `ttl`
   are misses.

Optionally: expose `POST /api/v1/cache/purge` so an admin can
manually invalidate (the equivalent of a `FLUSHDB` on Redis).

## 5. Backward compatibility

* `cache_backend=sqlite` is the default; one extra table
  `response_cache (key PRIMARY KEY, value BLOB, ttl INT,
  channel_id INT, stored_at INT, hit_count INT)`.
* `cache_backend=memory` introduces no on-disk artefacts.
* Existing deployments without any cache config run unchanged
  (cache enabled but empty, no perf impact beyond microseconds).

## 6. Failure mode

* Cache backend unreachable → log warning; treat as miss.
* Cache poisoned (write fails after upstream returned 200) →
  upstream response is still served to the client; the failed
  cache write is only logged.
* Cache hit corrupted (read fails to JSON-decode) → log error,
  fall back to upstream.

## 7. Files to touch

```
internal/cache/cache.go              // backend interface
internal/cache/memory.go             // LRU + RWMutex
internal/cache/sqlite.go             // SQL implementation
internal/cache/key.go                // sha256 key builder
internal/cache/control.go            // cache_control flags
internal/api/router.go               // consult cache before Chat(), populate after
internal/api/router.go               // Honor "X-LlmRx-Cache: HIT" header on stream
internal/api/router.go               // Pass cache-control hints from request body
internal/api/stream_caps_test.go     // add cache test (hit bypass, miss populate, hdr)
internal/cache/memory_test.go
internal/cache/sqlite_test.go
internal/cache/key_test.go
internal/config/config.go            // ServerConfig: 5 new yaml keys
internal/store/sqlite.go             // new table response_cache
internal/runtime/runtime.go          // atomic defaults for live tunables
cmd/gateway/main.go                  // wire cache to handler
web/src/pages/Logs.tsx               // surface cache-hit ratio
web/src/pages/Dashboard.tsx          // add a card "cache hits / cost saved"
```

## 8. Test plan

* Memory backend unit tests (LRU eviction, hit counters, ttl)
* SQLite backend unit tests (persistence, schema migration)
* Key builder determinism (same input → same hash; tool ordering
  does not matter)
* Wire-level: hit returns same body the upstream did
* Wire-level: `no-cache` and `no-store` honoured
* Streaming hit replays correctly
* Cache threshold disabled when `temperature > 0`

## 9. Out-of-scope for P8 (parked)

* Semantic cache (embedding + vector store) — P9
* Cache rules per user / per group — P10
* Distributed cache invalidation across instances — P12

## 10. Acceptance criteria

* ✅ Cache hit returns verbatim JSON in < 5 ms
* ✅ Cache miss adds < 100 µs to request latency
* ✅ No regressions on existing 60% coverage gate
* ✅ New unit tests keep coverage above 70% for `internal/cache`
* ✅ Memory footprint: 10K entries ≈ 20 MB (varies by payload size)
* ✅ `post /api/v1/cache/purge` clears everything
* ✅ Admin dashboard shows cache hit ratio + cost saved
