package subusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpTimeout bounds a single vendor call. The monitor also applies a
// context deadline; this is the backstop when a caller forgets one.
const httpTimeout = 10 * time.Second

// maxBody caps how much of a response we read (vendors occasionally
// answer with an HTML error page or a huge dashboard payload).
const maxBody = 1 << 20

// httpDoer is swappable in tests.
var httpDoer = func() *http.Client { return &http.Client{Timeout: httpTimeout} }

// SetHTTPClient replaces the client factory (tests). nil restores the
// default 10s client.
func SetHTTPClient(c func() *http.Client) {
	if c == nil {
		httpDoer = func() *http.Client { return &http.Client{Timeout: httpTimeout} }
		return
	}
	httpDoer = c
}

// getJSON performs a GET and decodes the body into dst. Returns the raw
// body (truncated) so callers can stash it for debugging.
func getJSON(ctx context.Context, url string, headers map[string]string, dst any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpDoer().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	raw := truncateRaw(body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, fmt.Errorf("upstream status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if dst != nil {
		if err := json.Unmarshal(body, dst); err != nil {
			return raw, fmt.Errorf("decode: %w", err)
		}
	}
	return raw, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// truncateRaw keeps the first 512 bytes of a response for the report's
// Raw field. Enough to see an error page, small enough for the DB.
func truncateRaw(b []byte) string { return truncate(string(b), 512) }
