package api

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
)

// chunkBufMaxBytesBench is a local copy of the cap threshold used
// in chunkBufPool. Duplicated here so the benchmark can compile
// against both the pre- and post-cap code paths.
const chunkBufMaxBytesBench = 64 * 1024

// BenchmarkSSEChunkFrame_Pool exercises the per-chunk SSE
// framing: pool get → reset → write prefix → json.Encode →
// write suffix → pool put. This is the path that fires once
// per upstream stream chunk.
func BenchmarkSSEChunkFrame_Pool(b *testing.B) {
	chunk := provider.StreamChunk{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "bench-model",
		Choices: []provider.StreamChoice{{
			Index: 0,
			Delta: provider.Message{Content: "hello world"},
		}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := chunkBufPool.Get().(*bytes.Buffer)
		buf.Reset()
		buf.Write(dataPrefix)
		_ = json.NewEncoder(buf).Encode(chunk)
		_ = buf.Bytes()
		if buf.Cap() <= chunkBufMaxBytesBench {
			chunkBufPool.Put(buf)
		}
	}
}

// BenchmarkSSEChunkFrame_NoPool is the same operation without the
// pool — measures the allocation savings of pool reuse alone.
func BenchmarkSSEChunkFrame_NoPool(b *testing.B) {
	chunk := provider.StreamChunk{
		ID:      "chatcmpl-bench",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "bench-model",
		Choices: []provider.StreamChoice{{
			Index: 0,
			Delta: provider.Message{Content: "hello world"},
		}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := &bytes.Buffer{}
		buf.Write(dataPrefix)
		_ = json.NewEncoder(buf).Encode(chunk)
		_ = buf.Bytes()
	}
}
