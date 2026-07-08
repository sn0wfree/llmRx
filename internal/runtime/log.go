package runtime

import (
	"bytes"
	"io"
	"log"
	"strings"
	"sync/atomic"
)

// LevelFilter wraps an io.Writer and drops lines whose level is
// below the configured threshold. The filter inspects the line
// prefix ("error:" / "warn:" / "info:" / "debug:") to determine
// the level; lines without a recognised prefix default to info.
//
// Designed to be installed via log.SetOutput in main.go so that
// all 40+ log.Printf call sites in the codebase pick up the
// runtime log_level without code changes. The filter does not
// rewrite the prefix — it only gates whether the line is emitted.
//
// The threshold is read from the Defaults passed in at
// construction; the filter holds a *atomic.Int64 pointer to the
// same word so admin updates take effect on the next line.
type LevelFilter struct {
	level *uint64 // points to Defaults.logLevelBits; loaded atomically
	out   io.Writer
}

// NewLevelFilter returns a writer that drops lines below the
// threshold in rt and forwards the rest to out.
func NewLevelFilter(rt *Defaults, out io.Writer) *LevelFilter {
	return &LevelFilter{
		level: &rt.logLevelBits,
		out:   out,
	}
}

func (f *LevelFilter) Write(p []byte) (int, error) {
	// Read the threshold once per write — admin updates land on
	// the next call.
	threshold := int64(atomic.LoadUint64(f.level))

	// Inspect the line. log.Printf terminates each call with \n.
	// A single Write may contain multiple lines if a caller used
	// log.Print without explicit newline handling, so we split.
	lines := bytes.Split(p, []byte("\n"))
	keep := make([]byte, 0, len(p))
	for i, ln := range lines {
		// Skip trailing empty fragment from the final \n.
		if i == len(lines)-1 && len(ln) == 0 {
			break
		}
		lvl := levelForLine(string(ln))
		if lvl >= threshold {
			keep = append(keep, ln...)
			keep = append(keep, '\n')
		}
	}
	if len(keep) == 0 {
		// Nothing to write, but report full length to satisfy
		// the io.Writer contract (the bytes were "consumed" by
		// being filtered, not lost).
		return len(p), nil
	}
	if _, err := f.out.Write(keep); err != nil {
		return 0, err
	}
	return len(p), nil
}

// InstallLogFilter wires the standard log package's output through
// rt. Call once at startup, after runtime.New() and after any DB
// overlay has been applied. Subsequent calls replace the
// previously installed filter.
func InstallLogFilter(rt *Defaults, w io.Writer) {
	log.SetOutput(NewLevelFilter(rt, w))
	// Also redirect the standard logger's prefix/sentinel output
	// to the same writer so the "log" package's own bookkeeping
	// (e.g. panic stack traces) goes through the filter too.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

// logLevelName returns the human-readable name of a level.
func logLevelName(level int64) string {
	switch level {
	case 0:
		return "debug"
	case 1:
		return "info"
	case 2:
		return "warn"
	case 3:
		return "error"
	}
	return strings.Repeat("?", int(level))
}

// LogLevelName is the exported version used by main.go and the
// admin handler to print the active level.
func LogLevelName(level int64) string { return logLevelName(level) }
