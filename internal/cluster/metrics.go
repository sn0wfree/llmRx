package cluster

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// requestMetrics accumulates request statistics for the heartbeat
// payload. Updated by MetricsMiddleware; read by collectMetrics.
type requestMetrics struct {
	total      atomic.Int64 // cumulative requests
	active     atomic.Int64 // in-flight
	latencySum atomic.Int64 // total ms
	latencyN   atomic.Int64
}

// defaultMetrics is the process-wide counter. A singleton keeps the
// middleware free of wiring: the heartbeat client reads it directly.
var defaultMetrics requestMetrics

// MetricsMiddleware records request count, active connections and
// latency for the heartbeat payload. Cheap: two atomic ops per
// request on the hot path.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultMetrics.active.Add(1)
		start := time.Now()
		next.ServeHTTP(w, r)
		defaultMetrics.active.Add(-1)
		ms := time.Since(start).Milliseconds()
		defaultMetrics.total.Add(1)
		defaultMetrics.latencySum.Add(ms)
		defaultMetrics.latencyN.Add(1)
	})
}

// cpuSample tracks the previous /proc/self/stat tick count for
// delta-based CPU usage. Zero values mean "no sample yet".
type cpuSample struct {
	mu    sync.Mutex
	ticks uint64
	at    time.Time
}

// readSelfCPUTicks returns utime+stime from /proc/self/stat in clock
// ticks (USER_HZ, typically 100), or ok=false when unavailable
// (non-Linux).
func readSelfCPUTicks() (uint64, bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// Format: pid (comm) state ... utime(14) stime(15) — 1-based.
	// comm may contain spaces/parens, so split after the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(s[i+1:])
	// After comm: state ppid pgrp session tty_nr tpgid flags
	// minflt cminflt majflt cmajflt utime stime ...
	// indices in fields[]: 0=state ... 11=utime 12=stime
	if len(fields) < 13 {
		return 0, false
	}
	ut, err1 := strconv.ParseUint(fields[11], 10, 64)
	st, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return ut + st, true
}

// userHZ is the kernel clock tick rate assumed for /proc CPU math.
// Linux reports it as 100 on every common config (CONFIG_HZ=100/250
// scale to the same USER_HZ=100 userspace view).
const userHZ = 100.0

// Usage samples process CPU as a fraction of total machine capacity
// since the previous call. First call (or non-Linux) returns 0.
func (s *cpuSample) Usage() float64 {
	ticks, ok := readSelfCPUTicks()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		return 0
	}
	if s.at.IsZero() {
		s.ticks, s.at = ticks, now
		return 0
	}
	elapsed := now.Sub(s.at).Seconds()
	if elapsed <= 0 {
		return 0
	}
	delta := float64(ticks-s.ticks) / userHZ // CPU seconds
	s.ticks, s.at = ticks, now
	r := delta / elapsed / float64(runtime.NumCPU())
	if r > 1 {
		r = 1
	}
	if r < 0 {
		r = 0
	}
	return r
}

// readProcField parses a "Key: <number> [unit]" line from a /proc
// file, returning the number (0 when missing or unparsable).
func readProcField(path, key string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, key) {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return 0
		}
		v, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0
		}
		return v // both VmRSS and MemTotal are in kB
	}
	return 0
}

// memUsageRatio returns process RSS / system MemTotal from /proc,
// falling back to the Go heap ratio when /proc is unavailable
// (non-Linux).
func memUsageRatio() float64 {
	rss := readProcField("/proc/self/status", "VmRSS:")
	total := readProcField("/proc/meminfo", "MemTotal:")
	if rss > 0 && total > 0 {
		r := rss / total
		if r > 1 {
			r = 1
		}
		return r
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapSys == 0 {
		return 0
	}
	r := float64(m.HeapInuse) / float64(m.HeapSys)
	if r > 1 {
		r = 1
	}
	return r
}
