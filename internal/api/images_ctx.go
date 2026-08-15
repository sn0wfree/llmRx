package api

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/provider"
)

// requestImagesKey carries the number of input images for the
// current request. It is set once in ChatCompletions (after the
// request is decoded) so every emit path — streaming, combo, auto —
// can attribute image charges without threading the count through
// ~30 call sites.
type requestImagesKey struct{}

func ctxWithImages(ctx context.Context, req *provider.ChatRequest) context.Context {
	return context.WithValue(ctx, requestImagesKey{}, req.CountImages())
}

func imagesFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if n, ok := ctx.Value(requestImagesKey{}).(int); ok {
		return n
	}
	return 0
}
