package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/sn0wfree/llmRx/internal/logging"
)

// stdioMaxFrameBytes caps a single JSON-RPC frame read from a stdio
// server. Tool results can be large but unbounded reads let a
// misbehaving sidecar exhaust memory.
const stdioMaxFrameBytes = 16 << 20 // 16 MiB

// stdioTransport runs an MCP server as a local subprocess and
// exchanges JSON-RPC frames over stdin/stdout. Requests are
// serialized with a mutex because a subprocess has a single
// stdout pipe — concurrent requests would interleave frames.
type stdioTransport struct {
	command string // shell command, run via sh -c

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	closeOnce sync.Once
}

// NewStdioClient builds an MCP client backed by a local subprocess.
// command is executed via `sh -c` so `npx @modelcontextprotocol/...`
// and shell pipelines work.
func NewStdioClient(command string) *Client {
	return NewClientWithTransport(&stdioTransport{command: command})
}

func (t *stdioTransport) rpc(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	req := &Request{
		JSONRPC: Version,
		ID:      1,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal request: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureStarted(); err != nil {
		return nil, err
	}

	// Frame the request as one JSON line (newline-delimited).
	// Content-Length framing is accepted on read; writing the
	// newline form is compatible with both server generations.
	frame := append(body, '\n')
	if _, err := t.stdin.Write(frame); err != nil {
		t.kill()
		return nil, fmt.Errorf("mcp: stdio write: %w", err)
	}

	// Wait for the response line. Watch ctx so a client disconnect
	// doesn't block forever on a silent server.
	done := make(chan struct{})
	var raw []byte
	var readErr error
	go func() {
		raw, readErr = t.readFrame()
		close(done)
	}()
	select {
	case <-ctx.Done():
		t.kill()
		return nil, fmt.Errorf("mcp: stdio %w", ctx.Err())
	case <-done:
	}
	if readErr != nil {
		return nil, readErr
	}
	return decodeResponse(raw)
}

// ensureStarted lazily spawns the subprocess on first use.
func (t *stdioTransport) ensureStarted() error {
	if t.cmd != nil {
		return nil
	}
	cmd := exec.Command("sh", "-c", t.command)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdio stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdio stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdio stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp: stdio start %q: %w", t.command, err)
	}
	t.cmd = cmd
	t.stdin = stdin
	t.scanner = bufio.NewScanner(stdout)
	t.scanner.Buffer(make([]byte, 4096), stdioMaxFrameBytes)

	// Surface the sidecar's stderr in our logs; a silent server is
	// hard to debug otherwise.
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			logging.Info("mcp stdio stderr",
				logging.F("command", t.command),
				logging.F("line", s.Text()),
			)
		}
	}()
	return nil
}

// readFrame reads one JSON-RPC response frame from stdout, accepting
// both newline-delimited JSON and Content-Length framing. Blank lines
// are skipped (LSP-style framing uses them as separators).
func (t *stdioTransport) readFrame() ([]byte, error) {
	for {
		if !t.scanner.Scan() {
			err := t.scanner.Err()
			if err != nil {
				return nil, fmt.Errorf("mcp: stdio read: %w", err)
			}
			return nil, fmt.Errorf("mcp: stdio server closed stdout")
		}
		line := t.scanner.Bytes()
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Content-Length:") {
			// Content-Length framed protocol: next frame is the JSON
			// body. The scanner already consumed the header line; the
			// body may be a single line for compact JSON.
			return t.readContentLengthFrame(trimmed)
		}
		return []byte(trimmed), nil
	}
}

// readContentLengthFrame consumes the JSON body following a
// Content-Length header. The LSP/MCP framing is:
//
//	Content-Length: <n>\r\n\r\n<json>
//
// which the line scanner sees as: header line, blank line, body line.
func (t *stdioTransport) readContentLengthFrame(header string) ([]byte, error) {
	parts := strings.SplitN(header, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("mcp: stdio bad content-length header %q", header)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || n <= 0 || n > stdioMaxFrameBytes {
		return nil, fmt.Errorf("mcp: stdio bad content-length %q", parts[1])
	}
	// Skip the blank line between header and body.
	if !t.scanner.Scan() {
		return nil, fmt.Errorf("mcp: stdio missing content-length separator")
	}
	// Read the body line (compact JSON fits on one line).
	if !t.scanner.Scan() {
		return nil, fmt.Errorf("mcp: stdio missing content-length body")
	}
	body := strings.TrimSpace(t.scanner.Text())
	if len(body) != n {
		// Multi-line body: keep reading until we've consumed n bytes.
		var sb strings.Builder
		sb.WriteString(body)
		for sb.Len() < n && t.scanner.Scan() {
			sb.WriteByte('\n')
			sb.WriteString(t.scanner.Text())
		}
		if sb.Len() != n {
			return nil, fmt.Errorf("mcp: stdio content-length body mismatch: want %d got %d", n, sb.Len())
		}
		body = sb.String()
	}
	return []byte(body), nil
}

// kill terminates the subprocess and resets state so the next rpc
// call respawns it.
func (t *stdioTransport) kill() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_, _ = t.cmd.Process.Wait()
	}
	t.cmd = nil
	t.stdin = nil
	t.scanner = nil
}

// Close terminates the subprocess if running.
func (t *stdioTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.kill()
		t.mu.Unlock()
	})
	return nil
}
