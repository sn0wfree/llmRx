// Package requestid assigns, propagates, and extracts request IDs.
//
// A request ID is a short, URL-safe identifier attached to every
// request flowing through the gateway. It appears in:
//   - the X-Request-Id HTTP header (request and response)
//   - every structured log line for the request
//   - log_store rows
//
// If the inbound request already supplies an X-Request-Id header
// (typical for upstream gateways), that value is reused; otherwise
// a fresh ID is generated from crypto/rand. IDs are 16 hex chars
// (8 bytes) which gives 2^64 uniqueness — collision-resistant
// without the long form of UUIDs.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// HeaderName is the canonical header for request IDs.
const HeaderName = "X-Request-Id"

// ctxKey is unexported to prevent collisions.
type ctxKey struct{}

// FromContext returns the request ID stored in ctx, or "" if absent.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// NewContext stores the given request ID in a new context.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Middleware assigns or reuses a request ID and stores it on the
// request context. The response also carries the ID in its header
// so clients can correlate logs.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderName)
		if id == "" {
			id = generate()
		}
		w.Header().Set(HeaderName, id)
		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), id)))
	})
}

// generate returns a fresh 16-hex-char ID.
func generate() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on Linux/macOS, but be defensive.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// OrNew returns the ID from the context, generating one if absent.
// Useful in places that don't have the middleware on their stack.
func OrNew(ctx context.Context) (context.Context, string) {
	if id := FromContext(ctx); id != "" {
		return ctx, id
	}
	id := generate()
	return NewContext(ctx, id), id
}
