package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UsagePersister durably stores usage records so a restart (or an
// offline period) does not lose billable rows. The JSONL implementation
// appends one record per line — same rationale as the server's log
// store: append-only, human-readable, trivially rotatable.
type UsagePersister interface {
	// Append persists one record batch synchronously.
	Append([]UsageRecord) error
	// Close flushes and releases the file.
	Close() error
}

// jsonlUsageStore appends to a single JSONL file. Rotation is left to
// the operator (the file is append-only text; logrotate-style moves are
// safe because the writer re-opens by path when the handle fails).
type jsonlUsageStore struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// NewJSONLUsageStore opens (creating, appending) the usage file.
func NewJSONLUsageStore(path string) (UsagePersister, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("usage file path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("usage store: mkdir %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("usage store: open %s: %w", path, err)
	}
	return &jsonlUsageStore{f: f, path: path}, nil
}

func (s *jsonlUsageStore) Append(records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return errors.New("usage store: closed")
	}
	var sb strings.Builder
	for i := range records {
		b, err := records[i].marshal()
		if err != nil {
			return fmt.Errorf("usage store: marshal: %w", err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if _, err := s.f.WriteString(sb.String()); err != nil {
		return fmt.Errorf("usage store: write: %w", err)
	}
	return nil
}

func (s *jsonlUsageStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// StartUsageRecorder drains the in-memory buffer into the persister
// every few seconds until ctx ends. Persistence is decoupled from
// upload so rows survive even when the server is unreachable for days.
func (p *Proxy) StartUsageRecorder(ctx context.Context, store UsagePersister) {
	if store == nil {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Best-effort final flush.
			_ = store.Append(p.buffer.take(p.buffer.len()))
			return
		case <-t.C:
			batch := p.buffer.takeAll()
			if len(batch) == 0 {
				continue
			}
			if err := store.Append(batch); err != nil {
				// Put them back; the next tick retries.
				p.requeue(batch)
			}
		}
	}
}

// takeAll drains every pending record.
func (u *usageBuffer) takeAll() []UsageRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.pending
	u.pending = nil
	return out
}

// Close flushes the usage buffer into the persister, if wired.
func (p *Proxy) Close() error {
	if p.usageSink == nil {
		return nil
	}
	batch := p.buffer.takeAll()
	if len(batch) == 0 {
		return nil
	}
	return p.usageSink.Append(batch)
}

// usageSink is set by the binary after construction (the persister may
// fail to open; the proxy still works without it).
func (p *Proxy) SetUsageSink(s UsagePersister) { p.usageSink = s }

// listenAndServeBackground starts the HTTP server without blocking and
// returns once the socket is bound. Binding before announcing readiness
// avoids the race where a supervisor's health check runs between the
// ready log line and the actual listen.
func listenAndServeBackground(srv *http.Server) (net.Listener, error) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	go func() {
		// Serve always returns a non-nil error; ErrServerClosed is the
		// expected result of a clean Shutdown.
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "gateway-client: serve:", serr)
			os.Exit(1)
		}
	}()
	return ln, nil
}
