// Package api implements the OpenAI-compatible HTTP ingress.
//
// This file used to hold the entire handler (~2700 lines); it was
// split by endpoint domain. Layout map:
//
//	handler.go     Handler struct, construction, wiring, Routes()
//	buffers.go     pooled chunk/body/stream buffers
//	httpio.go      request body reading, JSON, error rendering
//	request.go     per-request helpers: token context, client IP, sessions
//	chat.go        POST /v1/chat/completions (non-auto combos + streaming)
//	chat_auto.go   mode:auto combo handling (scoring, pinned attempts)
//	models.go      GET /v1/models + the models cache
//	embeddings.go  POST /v1/embeddings
//	images.go      POST /v1/images/generations
//	audio.go       POST /v1/audio/speech and /v1/audio/transcriptions
//	rerank.go      POST /v1/rerank
//	billing.go     cost lookup, pricing gate, log emission facade
//	register.go    self-serve register/me/redeem endpoints
//	emitter.go     LogEmitter: async log fan-out (broker + logstore)
//	cost.go        CostCalculator: upstream cost math
//	images_ctx.go  per-request image-count context key
package api
