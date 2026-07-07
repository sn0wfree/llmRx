# P9 — Multimodal Endpoints (Image / Rerank / Audio)

> Date: 2026-07 · Owner: llmRx maintainers · Status: design.
> Target: after P8 (caching) lands.

## 1. Why now

OpenAI-compatible SDK clients (Cursor / Continue / Open WebUI /
LibreChat / ChatGPT-Next-Web) ship a fixed catalogue of endpoints:

```
POST /v1/chat/completions      text + tool calls (have it)
POST /v1/embeddings            vector embeddings
POST /v1/images/generations    DALL-E / Imagen / Flux
POST /v1/audio/transcriptions  Whisper STT
POST /v1/audio/speech          TTS
POST /v1/rerank                Cohere / Jina / BGE
```

When a customer installs llmRx and calls `/v1/images/generations`,
today they get **404**. They uninstall. The same client works
against LiteLLM / One-API / Bifrost out of the box. See
`docs/COMPARATIVE.md` §2 — this is the top Tier-1 gap after P8
caching.

P9 ships the three highest-value endpoints (Image / Rerank /
Audio STT+TTS) so the OpenAI client experience is complete.

## 2. Scope

| Endpoint | Priority | LoC | Notes |
|---|:---:|---:|---|
| `POST /v1/images/generations` | 🥇 | ~250 | OpenAI DALL-E 3 spec, single + batch |
| `POST /v1/rerank` | 🥇 | ~350 | Cohere-spec primary; Jina + BGE via adapter |
| `POST /v1/audio/transcriptions` | 🥈 | ~350 | multipart/form-data, Whisper-style |
| `POST /v1/audio/speech` | 🥉 | ~250 | OpenAI TTS spec |
| `POST /v1/embeddings` | parked | ~200 | P9.5; trivial OpenAI passthrough |

Each endpoint follows the same pattern as `/v1/chat/completions`:
routing (L1 static match on model name) → upstream HTTP call →
log row → emitLog (with per-token spend tracking).

## 3. Routing strategy

### 3.1 Image (`/v1/images/generations`)

```
Request:
{
  "prompt": "a corgi in space",
  "model": "dall-e-3",
  "n": 1,
  "size": "1024x1024",
  "quality": "standard",
  "response_format": "url",   // or "b64_json"
  "user": "u-1"
}

Response:
{
  "created": 1699999999,
  "data": [
    { "url": "https://oaidalleapiprodscus.blob.core.windows.net/..." },
    { "b64_json": "..." }
  ]
}
```

Implementation: **per-channel `endpoint_kind` extension**.

Today `Channel.Protocol` is `openai | anthropic | gemini`. For Image,
the channel is still OpenAI-compatible but serves a different
endpoint (`/images/generations`). Two options:

| Option | Pros | Cons |
|---|---|---|
| **A. New `Channel.Kind` field** (`chat | image | rerank | audio`) | Explicit; lets us reject `POST /chat` on an image-only channel | Schema migration; some channels legitimately serve both |
| **B. URL prefix routing** (gateway rewrites `/chat/completions` → `/images/generations` based on handler) | No schema change | Implicit; harder to debug |

**Decision**: Option B. Each handler picks the right upstream URL;
`Channel.Protocol` stays unchanged. An Image channel uses
`OpenAIImageProvider` (new) which always POSTs to
`/images/generations` on its base URL.

### 3.2 Rerank (`/v1/rerank`)

```
Request:
{
  "model": "cohere-rerank-3",
  "query": "What is the capital of France?",
  "documents": ["Paris is the capital of France", "Berlin is in Germany", ...],
  "top_n": 3,
  "return_documents": true
}

Response:
{
  "id": "rerank-...",
  "results": [
    { "index": 0, "relevance_score": 0.95, "document": "Paris is the capital of France" },
    { "index": 4, "relevance_score": 0.61, "document": "Parisian cuisine is famous for ..." }
  ]
}
```

Cohere's wire format. Adapter translates to:
- Jina AI (`POST https://api.jina.ai/v1/rerank`)
- BGE reranker (local ONNX — out of scope for now)
- Custom (LLMRX_EMBED_COMPAT env var → POST `{base}/rerank`)

### 3.3 Audio STT (`/v1/audio/transcriptions`)

multipart/form-data:
```
file=@recording.mp3
model=whisper-1
language=en
response_format=json   // or text, srt, verbose_json
temperature=0
```

OpenAI Whisper spec. Upstream is OpenAI-compatible (`POST
/audio/transcriptions`). Multipart body parsing via Go stdlib
`mime/multipart` — no new deps.

### 3.4 Audio TTS (`/v1/audio/speech`)

```
Request:
{
  "model": "tts-1",
  "input": "Hello, world.",
  "voice": "alloy",
  "response_format": "mp3",     // or opus, aac, flac, wav, pcm
  "speed": 1.0
}

Response: binary audio stream
```

Upstream returns the raw bytes (not JSON). Handler streams the
bytes through to the client with the right `Content-Type`.

## 4. Pricing model

Each `Channel` gains an optional `endpoint_pricing` JSON column or a
dedicated `endpoint_prices` table. Schema choice:

```sql
CREATE TABLE endpoint_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id INTEGER NOT NULL,
    endpoint TEXT NOT NULL,           -- 'image' | 'rerank' | 'audio_stt' | 'audio_tts'
    model TEXT NOT NULL,
    unit_price_usd REAL NOT NULL DEFAULT 0,  -- per image, per search, per minute audio
    unit TEXT NOT NULL,               -- 'image' | 'search' | 'minute' | '1k_char'
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_endpoint_prices ON endpoint_prices(channel_id, endpoint, model);
```

Calc:
- Image: `n_images * unit_price_usd`
- Rerank: `n_documents_searched * unit_price_usd` (or a flat per-search fee — provider-dependent)
- STT: `minutes_audio * unit_price_usd`
- TTS: `characters * unit_price_usd / 1000`

`model.Log` gains:
```go
type Log struct {
    // ... existing fields ...
    Endpoint     string  `json:"endpoint"`     // 'chat' | 'image' | 'rerank' | 'audio_stt' | 'audio_tts'
    Units        int     `json:"units"`        // n_images, n_docs, minutes, chars
}
```

Migration via `addColumnIfMissing` (default `endpoint='chat'`,
`units=0`).

## 5. Per-token spend tracking (extension)

The same `IncrementTokenSpend(tokenID, amount)` from the multi-tenant
work applies. emitLog now reads:

```
real   = calcEndpointCost(channel, req, usage)   // channel-specific
billed = real * markup * plan_markup
```

`calcEndpointCost` is one switch per endpoint type.

## 6. Wiring on the `api.Handler`

```go
func (h *Handler) Routes() http.Handler {
    r := chi.NewRouter()
    r.Post("/chat/completions", h.ChatCompletions)
    r.Post("/images/generations", h.ImageGenerations)
    r.Post("/audio/transcriptions", h.AudioTranscriptions)
    r.Post("/audio/speech", h.AudioSpeech)
    r.Post("/rerank", h.Rerank)
    r.Get("/models", h.ListModels)
    return r
}
```

Each new handler:

1. Decodes request body (multipart for audio, JSON otherwise).
2. Calls `router.RouteWith(ctx, model)` — same L1-L5 pipeline.
3. Resolves the right `EndpointProvider` based on the channel's
   `protocol` (openai-compatible serves all 4; cohere-jina serves
   rerank; etc.).
4. Calls the provider.
5. Emits a log row with the right `endpoint` tag.

## 7. Providers

```go
// internal/provider/image.go
type ImageProvider interface {
    Name() string
    Generate(ctx context.Context, req *ImageRequest, apiKey, baseURL string) (*ImageResponse, error)
}

// internal/provider/rerank.go
type RerankProvider interface {
    Name() string
    Rerank(ctx context.Context, req *RerankRequest, apiKey, baseURL string) (*RerankResponse, error)
}

// internal/provider/audio.go
type AudioTranscriptionProvider interface {
    Name() string
    Transcribe(ctx context.Context, req *AudioRequest, apiKey, baseURL string) (*TranscriptionResponse, error)
}
type AudioSpeechProvider interface {
    Name() string
    Speak(ctx context.Context, req *SpeechRequest, apiKey, baseURL string) (io.ReadCloser, error)
}
```

Concrete implementations:

| Endpoint | Default provider | Notes |
|---|---|---|
| Image | `OpenAIImageProvider` | POST `{base}/images/generations` |
| Rerank | `CohereRerankProvider` | POST `{base}/rerank` |
| Audio STT | `OpenAIAudioProvider` (Whisper-compatible) | POST `{base}/audio/transcriptions` |
| Audio TTS | `OpenAIAudioProvider` | POST `{base}/audio/speech` (binary stream) |

All share the same `http.Client` plumbing as today's chat providers.

## 8. Tests

| Test | Coverage |
|---|---|
| `image_generations_happy_path` | 200, n=1, url returned |
| `image_generations_b64_json` | response_format=b64_json works |
| `image_generations_routes_to_cheapest_channel` | L3 cost sort applies |
| `image_generations_records_log_with_endpoint` | logs.endpoint='image' |
| `rerank_cohere_happy_path` | top_n + scores + documents |
| `rerank_no_documents` | 400 |
| `audio_transcriptions_whisper` | multipart upload → json result |
| `audio_transcriptions_verbose_json` | segments + duration |
| `audio_speech_mp3_stream` | bytes streamed through |
| `audio_speech_speed_validation` | 0.25..4.0 range |
| `calc_image_cost` | n=2 + 0.02/image = 0.04 |
| `calc_rerank_cost` | n_docs=1000 + 0.001/search = 1.0 |
| `calc_audio_stt_cost` | minutes=2.5 + 0.006/min = 0.015 |
| `calc_audio_tts_cost` | chars=10000 + 0.015/1k_char = 0.15 |

## 9. Acceptance criteria

| Metric | Target |
|---|---|
| All four endpoints respond 200 on happy path | ✅ |
| All four endpoints log `endpoint` field | ✅ |
| Image response_format b64_json works | ✅ |
| Rerank returns documents when `return_documents=true` | ✅ |
| STT accepts multipart upload up to 25 MB | ✅ |
| TTS streams binary bytes (no JSON wrap) | ✅ |
| Spend tracking increments for all 4 endpoints | ✅ |
| Test coverage ≥ 70 % for new packages | ✅ |
| Coverage gate overall ≥ 65 % | ✅ |
| Zero new external dependencies | ✅ (multipart is stdlib) |

## 10. Files to add / touch

```
internal/provider/image.go              # ImageProvider interface + OpenAI impl
internal/provider/rerank.go             # RerankProvider + Cohere impl
internal/provider/audio.go              # AudioProvider (STT + TTS)
internal/api/image.go                   # handler
internal/api/rerank.go                  # handler
internal/api/audio.go                   # handlers (multipart + stream)
internal/model/types.go                 # Log.Endpoint, Log.Units
internal/store/sqlite.go                # endpoint_prices table + log columns
internal/store/store.go                 # ListEndpointPrices + CreateEndpointPrice
internal/admin/handler.go               # endpoint_prices CRUD
web/src/pages/Settings.tsx              # new endpoint section
internal/api/image_test.go
internal/api/rerank_test.go
internal/api/audio_test.go
internal/provider/image_test.go
internal/provider/rerank_test.go
internal/provider/audio_test.go
```

## 11. Rollout

1. Add `Log.Endpoint` + `Log.Units` + `endpoint_prices` table.
2. Land `internal/provider/image.go` + handler + tests.
3. Land `internal/provider/rerank.go` + handler + tests.
4. Land `internal/provider/audio.go` + handlers + tests.
5. Admin UI for endpoint prices.
6. README + CHANGELOG.
7. Optional: `embeddings` (P9.5).

## 12. Risks

| Risk | Mitigation |
|---|---|
| Multipart parsing DoS | `http.MaxBytesReader(w, body, 25<<20)` (25 MB cap) |
| Binary audio responses confused for text | Inspect `Content-Type` from upstream; pass through verbatim |
| Rerank providers have non-uniform pricing | Per-channel pricing table; defaults to 0 (free) on miss |
| L4 intent classifier doesn't apply to non-chat | Skip L4 for image / audio / rerank handlers |