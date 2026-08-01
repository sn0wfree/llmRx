package provider

import (
	"strings"
	"testing"
)

// BenchmarkContentString_Cached measures the ContentString cost
// on the post-cache path. The first call computes and stores;
// subsequent calls return the cached string. This benchmark calls
// the function b.N times on the same Message, so every iteration
// after the first is a cache hit.
func BenchmarkContentString_Cached(b *testing.B) {
	m := Message{Role: "user", Content: "hello world, this is a typical message body that goes through routing"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.ContentString()
	}
}

// BenchmarkContentString_MultimodalCached exercises the
// multimodal branch ([]ContentPart) with cache enabled.
func BenchmarkContentString_MultimodalCached(b *testing.B) {
	parts := []ContentPart{
		{Type: "text", Text: strings.Repeat("alpha ", 50)},
		{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,..."}},
		{Type: "text", Text: strings.Repeat("beta ", 50)},
		{Type: "text", Text: strings.Repeat("gamma ", 50)},
	}
	m := Message{Role: "user", Content: parts}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.ContentString()
	}
}