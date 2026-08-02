// Package logging provides a structured JSON logger for llmRx.
// Every log line is a single JSON object on its own line, suitable
// for ingestion by log aggregators (Loki, Elasticsearch, Datadog).
//
// The package exposes:
//   - Default() returning the process-global Logger
//   - Init(level, format) configuring the default logger
//   - Log() / Logf() emitting structured records
//   - With() / WithField() building sub-loggers with bound fields
//   - Info / Warn / Error / Debug shortcuts
//
// Format options:
//   - "json"  (default): one JSON object per line
//   - "text"  : human-readable key=value form, fallback to stdlib log
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level matches the global runtime.Defaults log level (0=debug..3=error).
type Level int

const (
	LevelDebug Level = 0
	LevelInfo  Level = 1
	LevelWarn  Level = 2
	LevelError Level = 3
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "unknown"
}

// Format selects the output encoding.
type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// Logger is a structured JSON logger. Safe for concurrent use.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	level  Level
	format Format
	fields map[string]any
}

// New constructs a Logger writing to w.
func New(w io.Writer, level Level, format Format) *Logger {
	return &Logger{
		out:    w,
		level:  level,
		format: format,
		fields: map[string]any{},
	}
}

var (
	defaultMu     sync.Mutex
	defaultLogger = New(os.Stdout, LevelInfo, FormatJSON)
)

// Init configures the default logger. format="json" or "text".
func Init(level Level, format Format) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLogger = New(os.Stdout, level, format)
}

// SetOutput swaps the output writer (used by tests).
func SetOutput(w io.Writer) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLogger.out = w
}

// SetLevel adjusts the minimum level.
func SetLevel(l Level) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLogger.level = l
}

// Default returns the process-global logger.
func Default() *Logger {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultLogger
}

// With returns a sub-logger with the given fields merged in. The
// parent logger is unchanged.
func (l *Logger) With(fields map[string]any) *Logger {
	merged := make(map[string]any, len(l.fields)+len(fields))
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range fields {
		merged[k] = v
	}
	return &Logger{
		out:    l.out,
		level:  l.level,
		format: l.format,
		fields: merged,
	}
}

// WithField returns a sub-logger with one extra field.
func (l *Logger) WithField(key string, value any) *Logger {
	return l.With(map[string]any{key: value})
}

// Info / Warn / Error / Debug are level shortcuts.
func (l *Logger) Info(msg string, fields ...Field)  { l.log(LevelInfo, msg, fields) }
func (l *Logger) Warn(msg string, fields ...Field)  { l.log(LevelWarn, msg, fields) }
func (l *Logger) Error(msg string, fields ...Field) { l.log(LevelError, msg, fields) }
func (l *Logger) Debug(msg string, fields ...Field) { l.log(LevelDebug, msg, fields) }

// Field is a single key/value pair passed to log calls.
type Field struct {
	Key   string
	Value any
}

// F is a convenience constructor for Field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// bufPoolMax caps the buffer we return to the pool. A typical log
// line is well under 4 KiB; a stray 10 MiB dump should not stay
// pinned in the pool until the next GC.
const bufPoolMax = 16 * 1024

// mapPoolMax caps the number of pre-sized slots in a returned map.
// A normal log entry has 5-15 fields; a stray 1000-field record
// should not poison the pool.
const mapPoolMax = 64

// Buffer pool to reduce allocations on the hot path.
var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// mapPool reuses the record map to avoid allocation per log call.
var mapPool = sync.Pool{
	New: func() any {
		return make(map[string]any, 16)
	},
}

// putBuf returns buf to the pool only if its capacity stays
// under the cap. Oversized buffers are dropped, allowing the GC
// to reclaim the memory.
func putBuf(buf *bytes.Buffer) {
	if buf.Cap() <= bufPoolMax {
		bufPool.Put(buf)
	}
}

// putRecord returns m to the pool only when its bucket count stays
// reasonable. Pre-sized maps at the high end of normal usage can be
// returned; pathological oversized records are dropped so they
// don't permanently bloat the pool.
func putRecord(m map[string]any) {
	if len(m) <= mapPoolMax {
		mapPool.Put(m)
	}
}

// log emits a record at the given level.
func (l *Logger) log(level Level, msg string, fields []Field) {
	if level < l.level {
		return
	}

	// Reuse a map from the pool.
	record := mapPool.Get().(map[string]any)
	// Clear previous keys efficiently.
	for k := range record {
		delete(record, k)
	}

	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["level"] = level.String()
	record["msg"] = msg
	for k, v := range l.fields {
		record[k] = v
	}
	for _, f := range fields {
		record[f.Key] = f.Value
	}

	var line string
	if l.format == FormatText {
		line = formatText(record)
	} else {
		// JSON path: encode into a pooled buffer, write the
		// bytes directly under the logger lock (no extra string
		// copy from .String()), then return the buffer.
		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		enc := json.NewEncoder(buf)
		if err := enc.Encode(record); err != nil {
			putBuf(buf)
			putRecord(record)
			return
		}
		// json.Encoder always appends a trailing '\n'; trim it so
		// Fprintln emits exactly one final newline.
		b := buf.Bytes()
		if n := len(b); n > 0 && b[n-1] == '\n' {
			b = b[:n-1]
		}
		l.mu.Lock()
		_, _ = l.out.Write(b)
		_, _ = l.out.Write([]byte("\n"))
		l.mu.Unlock()
		putBuf(buf)
		putRecord(record)
		return
	}

	putRecord(record)

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintln(l.out, line)
}

func formatText(r map[string]any) string {
	// Stable order: ts, level, msg, then everything else.
	out := fmt.Sprintf("%s level=%s msg=%q", r["ts"], r["level"], r["msg"])
	keys := []string{"ts", "level", "msg"}
	for _, k := range keys {
		delete(r, k)
	}
	for k, v := range r {
		out += fmt.Sprintf(" %s=%v", k, v)
	}
	return out
}

// Package-level shortcuts use the default logger.
func Info(msg string, fields ...Field)  { Default().Info(msg, fields...) }
func Warn(msg string, fields ...Field)  { Default().Warn(msg, fields...) }
func Error(msg string, fields ...Field) { Default().Error(msg, fields...) }
func Debug(msg string, fields ...Field) { Default().Debug(msg, fields...) }

// With returns a sub-logger bound to the given fields, using the
// default logger.
func With(fields map[string]any) *Logger { return Default().With(fields) }

// WithRequestID returns a sub-logger with a request_id field bound.
func WithRequestID(requestID string) *Logger {
	return Default().WithField("request_id", requestID)
}
