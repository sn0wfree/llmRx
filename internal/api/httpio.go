package api

import (
	"bytes"
	"io"
	"strings"

	json "github.com/goccy/go-json"
	"net/http"
)

type errorResp struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func errorTypeFor(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "invalid_request_error"
	case status == http.StatusNotFound:
		return "invalid_request_error"
	case status >= 500:
		return "api_error"
	default:
		return "upstream_error"
	}
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	// Defensive: a status of 0 means the upstream provider never
	// produced an HTTP response (DNS failure, connection refused,
	// TLS handshake error). The net/http package rejects
	// WriteHeader(0) with "invalid WriteHeader code" — which
	// otherwise surfaces as a panic recovered by chi middleware
	// and shows up to the client as a 0-byte 200 response.
	if status <= 0 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := errorResp{}
	resp.Error.Message = msg
	resp.Error.Type = errorTypeFor(status)
	resp.Error.Code = code
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

// readBody reads the full request body, capped at limit bytes.
// The body is replaced with a fresh reader so downstream code
// can still read from r.Body if needed.
func readBody(r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	buf := bodyBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	_, err := buf.ReadFrom(r.Body)
	r.Body.Close()
	if err != nil {
		putBodyBuf(buf)
		return nil, err
	}
	b := buf.Bytes()
	body := make([]byte, len(b))
	copy(body, b)
	putBodyBuf(buf)
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// mustMarshalJSON marshals v to JSON, panicking on error. Used
// in the cache path where the response is guaranteed to marshal
// successfully since it came from the upstream.
func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("api: mustMarshalJSON: " + err.Error())
	}
	return b
}

// defaultRequestBodyMaxBytes is the fallback cap applied by
// bodyLimit() when the operator did not configure
// request_body_max_bytes. 64 MiB comfortably fits multimodal
// OpenAI requests with embedded base64 images.
const defaultRequestBodyMaxBytes int64 = 64 * 1024 * 1024

// bodyLimit returns the effective MaxBytesReader cap for the
// current Handler. 0 disables the cap (the decoder can then grow
// unbounded — almost always a bug).
func (h *Handler) bodyLimit() int64 {
	if h.cfg != nil && h.cfg.Server.RequestBodyMaxBytes > 0 {
		return h.cfg.Server.RequestBodyMaxBytes
	}
	return defaultRequestBodyMaxBytes
}

// decodeJSONBody wraps r.Body in MaxBytesReader (so an over-sized
// upload is rejected with 413 before the decoder allocates) and
// decodes into out. On *http.MaxBytesError the caller sees a
// distinct sentinel via errors.As so it can produce the right
// response shape.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return json.NewDecoder(r.Body).Decode(out)
}

// isBodyTooLarge reports whether err came from MaxBytesReader
// refusing to read past the limit.
//
// Go 1.19+ exposes the limit as *http.MaxBytesError; Go 1.18 (the
// minimum version declared by go.mod) returns a plain error whose
// message matches the canonical stdlib string. We match on the
// message because that string is part of the documented stdlib
// contract for both versions.
func isBodyTooLarge(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "http: request body too large")
}

// itoa formats an int as decimal string without importing strconv
// just for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
