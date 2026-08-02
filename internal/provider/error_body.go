package provider

import (
	"bytes"
	"io"
)

// upstreamErrorBodyCap is the max number of bytes we'll keep from
// an upstream non-2xx response when constructing the error string.
// A misconfigured upstream returning a multi-megabyte HTML error
// page should not blow up the gateway's memory.
const upstreamErrorBodyCap = 4 * 1024

// readErrorSnippet reads up to upstreamErrorBodyCap bytes from r
// (the upstream response body) and returns them for inclusion in
// the returned error message. The body is fully drained (within
// io.ReadAll semantics) only up to the cap; anything beyond is
// discarded so a hostile upstream cannot pin the connection.
func readErrorSnippet(r io.Reader) string {
	if r == nil {
		return ""
	}
	// io.LimitReader + io.ReadAll: 4 KiB ceiling, then truncate.
	limited := io.LimitReader(r, upstreamErrorBodyCap)
	snippet, err := io.ReadAll(limited)
	if err != nil || len(snippet) == 0 {
		return ""
	}
	// Trim trailing whitespace so log lines don't have stray newlines.
	snippet = bytes.TrimSpace(snippet)
	if len(snippet) > upstreamErrorBodyCap {
		snippet = snippet[:upstreamErrorBodyCap]
	}
	return string(snippet)
}
