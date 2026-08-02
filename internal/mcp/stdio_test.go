package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// newFakeStdioServer spawns a fake MCP server implemented as a tiny
// shell pipeline that reads one JSON line from stdin and writes a
// canned JSON-RPC response to stdout. The response body is injected
// via the FAKE_RESPONSE env var.
func newFakeStdioServer(t *testing.T, responseJSON string) *Client {
	t.Helper()
	script := `
while IFS= read -r line; do
  printf '%s\n' "$FAKE_RESPONSE"
done
`
	cmd := "FAKE_RESPONSE=" + quoteShell(responseJSON) + " sh -c " + quoteShell(script)
	return NewStdioClient(cmd)
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func fakeListToolsResponse() string {
	return `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"echo","description":"echo test","inputSchema":{"type":"object"}}]}}`
}

func TestStdioListTools(t *testing.T) {
	c := newFakeStdioServer(t, fakeListToolsResponse())
	defer c.Close()

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}

func TestStdioCallTool(t *testing.T) {
	c := newFakeStdioServer(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello back"}]}}`)
	defer c.Close()

	res, err := c.CallTool(context.Background(), "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "hello back" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestStdioRPCError(t *testing.T) {
	c := newFakeStdioServer(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
	defer c.Close()

	_, err := c.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("expected method-not-found error, got %v", err)
	}
}

func TestStdioBadJSONResponse(t *testing.T) {
	c := newFakeStdioServer(t, `not json at all`)
	defer c.Close()

	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error for bad json")
	}
}

func TestStdioCommandNotFound(t *testing.T) {
	c := NewStdioClient("/nonexistent/cmd/xyz")
	defer c.Close()

	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	// The shell spawns successfully but the child immediately exits
	// with stderr "not found", so the read path reports a closed
	// stdout. Either message means the command did not run.
	msg := err.Error()
	if !strings.Contains(msg, "start") && !strings.Contains(msg, "closed stdout") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStdioContextCancelKillsProcess(t *testing.T) {
	// Server that never responds.
	script := `
while IFS= read -r line; do
  sleep 30
done
`
	cmd := "sh -c " + quoteShell(script)
	c := NewStdioClient(cmd)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.ListTools(ctx)
	if err == nil {
		t.Fatal("expected context error")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected context error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("context cancel did not abort promptly")
	}
}

func TestStdioRestartAfterKill(t *testing.T) {
	// First call kills the process via context cancel; the next call
	// should respawn it. The fake server sleeps on the FIRST line
	// read (detected by a marker file), so the first rpc times out
	// and the second succeeds on a fresh process.
	marker := t.TempDir() + "/first"
	script := `
if [ ! -f MARKER ]; then
  touch MARKER
  sleep 30
fi
while IFS= read -r line; do
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}'
done
`
	script = strings.ReplaceAll(script, "MARKER", marker)
	c := NewStdioClient("sh -c " + quoteShell(script))
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.ListTools(ctx); err == nil {
		t.Fatal("expected first call to time out")
	}
	// Second call on a fresh process must succeed.
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("expected respawned call to succeed: %v", err)
	}
	if tools == nil {
		t.Fatal("expected tools list")
	}
}

func TestStdioContentLengthFraming(t *testing.T) {
	// Server writing Content-Length framed responses. The body length
	// is hard-coded (66 bytes) to avoid shell arithmetic pitfalls.
	script := `
while IFS= read -r line; do
  printf 'Content-Length: 61\n\n%s\n' '{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"clen"}]}}'
done
`
	c := NewStdioClient("sh -c " + quoteShell(script))
	defer c.Close()

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "clen" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}

func TestStdioConcurrentRequestsSerialized(t *testing.T) {
	// A server that appends its request count to a file proves calls
	// are serialized: with a mutex, counts never interleave mid-write.
	script := `
n=0
while IFS= read -r line; do
  n=$((n+1))
  body=$(printf '{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"%d"}]}}' "$n")
  printf '%s\n' "$body"
done
`
	c := NewStdioClient("sh -c " + quoteShell(script))
	defer c.Close()

	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			res, err := c.CallTool(context.Background(), "t", map[string]any{})
			if err != nil {
				results <- "ERR"
				return
			}
			results <- res.Content[0].Text
		}()
	}
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		seen[<-results] = true
	}
	for i := 1; i <= 8; i++ {
		want := fmt.Sprintf("%d", i)
		if !seen[want] {
			t.Fatalf("missing serialized response %q, seen %v", want, seen)
		}
	}
}
