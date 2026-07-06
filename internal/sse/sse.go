// Package sse provides a tiny helper for writing Server-Sent Events
// responses. It handles the Content-Type, chunked transfer, periodic
// heartbeats, and graceful detection of client disconnect via the
// ResponseWriter's underlying cancel channel.
package sse

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Writer serializes events onto an http.ResponseWriter. Concurrent
// calls to Event/Comment are serialized with a mutex so callers
// don't have to coordinate.
type Writer struct {
	w  http.ResponseWriter
	fl interface{ Flush() }
	mu sync.Mutex
}

// New prepares w for SSE and sends the standard prelude. It also
// flushes immediately so the client sees the headers/200 right away.
func New(w http.ResponseWriter) (*Writer, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fl, _ := w.(interface{ Flush() })
	if fl != nil {
		fl.Flush()
	}
	return &Writer{w: w, fl: fl}, nil
}

// Event writes a named event with the given data payload. Multi-line
// data is split across multiple "data:" lines per the SSE spec.
func (s *Writer) Event(event, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := s.w.Write([]byte(b.String())); err != nil {
		return err
	}
	if s.fl != nil {
		s.fl.Flush()
	}
	return nil
}

// Comment writes an SSE comment frame. Useful as a heartbeat.
func (s *Writer) Comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	if s.fl != nil {
		s.fl.Flush()
	}
	return nil
}

// Heartbeat sends a comment every interval until ctx is done.
// Designed to be launched in a goroutine; it never returns until
// cancellation. Any write error is ignored (the caller will see it
// on the next event attempt and exit their own loop).
func (s *Writer) Heartbeat(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Comment("ping")
		}
	}
}
