package api

import (
	"bytes"
	"sync"

)

// chunkBufMaxBytes caps the buffer we return to the pool. A typical
// SSE chunk is well under 4 KiB; a stray 10 MB streaming response
// should not stay pinned in the pool until the next GC.
const chunkBufMaxBytes = 64 * 1024

var chunkBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// putChunkBuf returns buf to the pool only if its capacity stays
// under the cap. Otherwise the buffer is dropped, allowing the GC
// to reclaim the oversized memory.
func putChunkBuf(buf *bytes.Buffer) {
	if buf.Cap() <= chunkBufMaxBytes {
		chunkBufPool.Put(buf)
	}
}

// bodyBufMaxBytes caps the request body buffer returned to the pool.
// A 256 KiB cap keeps the pool from pinning oversized uploads.
const bodyBufMaxBytes = 256 * 1024

var bodyBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func putBodyBuf(buf *bytes.Buffer) {
	if buf.Cap() <= bodyBufMaxBytes {
		buf.Reset()
		bodyBufPool.Put(buf)
	}
}

// streamBufMaxBytes caps the stream buffer returned to the pool.
// 512 KiB accommodates most cached SSE responses.
const streamBufMaxBytes = 512 * 1024

var streamBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func putStreamBuf(buf *bytes.Buffer) {
	if buf.Cap() <= streamBufMaxBytes {
		buf.Reset()
		streamBufPool.Put(buf)
	}
}
