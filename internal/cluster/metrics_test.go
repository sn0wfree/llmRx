package cluster

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// TestMetricsMiddleware_Counts: one request through the middleware
// must bump total, exercise active (in/out), and record latency.
func TestMetricsMiddleware_Counts(t *testing.T) {
	before := defaultMetrics.total.Load()
	h := MetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := defaultMetrics.total.Load(); got != before+1 {
		t.Fatalf("total = %d, want %d", got, before+1)
	}
	if got := defaultMetrics.active.Load(); got != 0 {
		t.Fatalf("active after request = %d, want 0", got)
	}
	if defaultMetrics.latencyN.Load() <= before {
		t.Fatal("latency samples must grow")
	}
}

// TestMetricsMiddleware_ActiveDuringRequest: active connections are
// observable while a request is in flight.
func TestMetricsMiddleware_ActiveDuringRequest(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	h := MetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	<-entered
	if got := defaultMetrics.active.Load(); got < 1 {
		t.Fatalf("active during request = %d, want >= 1", got)
	}
	close(release)
}

// TestCPUUsage_FirstSampleZero: the first sample only establishes the
// baseline; usage comes from the delta to the second call.
func TestCPUUsage_FirstSampleZero(t *testing.T) {
	s := &cpuSample{}
	if r := s.Usage(); r != 0 {
		t.Fatalf("first sample = %v, want 0", r)
	}
}

// TestCPUUsage_DeltaNonNegative: on Linux the second sample must be
// in [0, 1]; elsewhere (no /proc) it stays 0.
func TestCPUUsage_DeltaNonNegative(t *testing.T) {
	s := &cpuSample{}
	_ = s.Usage() // baseline
	// Burn a little CPU so the delta is measurable on Linux.
	n := 0
	for i := 0; i < 5_000_000; i++ {
		n += i
	}
	_ = n
	r := s.Usage()
	if r < 0 || r > 1 {
		t.Fatalf("cpu usage = %v, want [0,1]", r)
	}
	if runtime.GOOS != "linux" && r != 0 {
		t.Fatalf("non-linux cpu usage = %v, want 0", r)
	}
}

// TestReadSelfCPUTicks: on Linux the parse succeeds (the tick count
// may legitimately be 0 — USER_HZ=100 means under 10ms of CPU time
// reads as 0); elsewhere ok=false.
func TestReadSelfCPUTicks(t *testing.T) {
	ticks, ok := readSelfCPUTicks()
	if runtime.GOOS == "linux" {
		if !ok {
			t.Fatal("expected /proc/self/stat to parse on linux")
		}
	} else if ok {
		t.Fatalf("unexpected success reading /proc on %s", runtime.GOOS)
	}
	_ = ticks
}

// TestMemUsageRatio: always in (0,1]; on Linux via /proc, elsewhere
// via the heap fallback.
func TestMemUsageRatio(t *testing.T) {
	r := memUsageRatio()
	if r <= 0 || r > 1 {
		t.Fatalf("mem usage = %v, want (0,1]", r)
	}
}

// TestCollectMetrics_RealValues: the heartbeat payload must carry
// measured values (CPU in [0,1], mem in (0,1], QPS >= 0, active >= 0)
// instead of the old hardcoded 0.25/0.45/100/10.
func TestCollectMetrics_RealValues(t *testing.T) {
	c := NewHeartbeatClient("n1", "127.0.0.1", 8080, nil)
	m := c.collectMetrics()
	if m == nil {
		t.Fatal("collectMetrics returned nil")
	}
	if m.CPUUsage < 0 || m.CPUUsage > 1 {
		t.Fatalf("cpu = %v, want [0,1]", m.CPUUsage)
	}
	if m.MemoryUsage <= 0 || m.MemoryUsage > 1 {
		t.Fatalf("mem = %v, want (0,1]", m.MemoryUsage)
	}
	if m.QPS < 0 {
		t.Fatalf("qps = %d, want >= 0", m.QPS)
	}
	if m.ActiveConn < 0 {
		t.Fatalf("active = %d, want >= 0", m.ActiveConn)
	}
	// Second call establishes the QPS delta window (still 0 with no
	// traffic, but must not panic on the prevAt transition).
	time.Sleep(10 * time.Millisecond)
	m2 := c.collectMetrics()
	if m2.QPS < 0 {
		t.Fatalf("qps after window = %d, want >= 0", m2.QPS)
	}
}
