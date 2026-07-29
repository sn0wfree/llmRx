package prober

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// fakeStore implements channelLister in-memory.
type fakeStore struct {
	mu    sync.Mutex
	chs   []model.Channel
}

func (s *fakeStore) GetChannels() ([]model.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Channel, len(s.chs))
	copy(out, s.chs)
	return out, nil
}

func (s *fakeStore) set(chs []model.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chs = chs
}

// fakeKeyPicker returns a fixed key for any channel.
type fakeKeyPicker struct {
	key *model.Key
	err error
}

func (f *fakeKeyPicker) NextKey(_ int64) (*model.Key, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.key, nil
}

func TestCache_HealthyWhenEmpty(t *testing.T) {
	c := New(Config{}, &fakeStore{}, &fakeKeyPicker{key: &model.Key{Key: "k"}})
	defer c.Stop()
	if !c.Healthy(1) {
		t.Error("cold cache should be healthy")
	}
}

func TestCache_LatestEmptyForUnknownChannel(t *testing.T) {
	c := New(Config{}, &fakeStore{}, &fakeKeyPicker{key: &model.Key{Key: "k"}})
	defer c.Stop()
	if _, ok := c.Latest(42); ok {
		t.Error("unknown channel should return ok=false")
	}
}

func TestCache_ProbeChannel_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	st := &fakeStore{}
	st.set([]model.Channel{{
		ID: 1, Name: "ch1", Provider: "openai", Protocol: "openai",
		BaseURL: srv.URL, Models: []string{"m1"}, Status: model.ChannelEnabled,
	}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	c := New(Config{Timeout: 2 * time.Second}, st, kp)
	defer c.Stop()

	c.ProbeChannel(context.Background(), &st.chs[0])
	if !c.Healthy(1) {
		r, _ := c.Latest(1)
		t.Errorf("expected healthy after successful probe, got result=%+v", r)
	}
}

func TestCache_ProbeChannel_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	st := &fakeStore{}
	st.set([]model.Channel{{
		ID: 2, Name: "ch2", Provider: "openai", Protocol: "openai",
		BaseURL: srv.URL, Models: []string{"m1"}, Status: model.ChannelEnabled,
	}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	c := New(Config{Timeout: 2 * time.Second}, st, kp)
	defer c.Stop()

	c.ProbeChannel(context.Background(), &st.chs[0])
	if c.Healthy(2) {
		t.Error("expected unhealthy after failed probe")
	}
	r, ok := c.Latest(2)
	if !ok {
		t.Fatal("expected result entry")
	}
	if r.OK {
		t.Error("expected OK=false")
	}
	if r.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestCache_ProbeChannel_NoKey(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{
		ID: 3, Name: "ch3", Provider: "openai", Protocol: "openai",
		BaseURL: "http://x", Models: []string{"m1"}, Status: model.ChannelEnabled,
	}})
	kp := &fakeKeyPicker{err: errors.New("no keys")}

	c := New(Config{}, st, kp)
	defer c.Stop()

	c.ProbeChannel(context.Background(), &st.chs[0])
	if c.Healthy(3) {
		t.Error("expected unhealthy when no keys available")
	}
}

func TestCache_ProbeAll_SkipsDisabled(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{
		{ID: 4, Name: "enabled", Status: model.ChannelEnabled},
		{ID: 5, Name: "disabled", Status: model.ChannelDisabled},
	})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	c := New(Config{}, st, kp)
	defer c.Stop()
	c.ProbeAll(context.Background())

	snap := c.Snapshot()
	if _, ok := snap[4]; !ok {
		t.Error("enabled channel should be in snapshot")
	}
	if _, ok := snap[5]; ok {
		t.Error("disabled channel should NOT be in snapshot")
	}
}

func TestCache_Defaults(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()
	if cfg.Interval != 30*time.Second {
		t.Errorf("default Interval = %v, want 30s", cfg.Interval)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("default Timeout = %v, want 5s", cfg.Timeout)
	}
	if cfg.TTL != 60*time.Second {
		t.Errorf("default TTL = %v, want 60s", cfg.TTL)
	}
}

func TestCache_HealthyAfterTTL(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 6, Name: "ch6", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{TTL: 50 * time.Millisecond}
	c := New(cfg, st, kp)
	defer c.Stop()

	c.record(6, Result{OK: false, Error: "x", CheckedAt: time.Now().Add(-time.Second)})
	if !c.Healthy(6) {
		t.Error("stale entry should default to healthy")
	}
}

func TestCache_SuccessSchedulesHealthyInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	st := &fakeStore{}
	st.set([]model.Channel{{ID: 7, Name: "ch7", Provider: "openai", Protocol: "openai",
		BaseURL: srv.URL, Models: []string{"m1"}, Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{Interval: 30 * time.Second, HealthyInterval: 5 * time.Minute}
	c := New(cfg, st, kp)
	defer c.Stop()

	ch0 := st.chs[0]
	c.ProbeChannel(context.Background(), &ch0)
	r, ok := c.Latest(7)
	if !ok {
		t.Fatal("expected result")
	}
	if !r.OK {
		t.Error("expected OK=true after successful probe")
	}
	if !r.NextProbeAt.After(time.Now().Add(4 * time.Minute)) {
		t.Errorf("success should schedule ~5m out, got NextProbeAt=%v (now=%v)", r.NextProbeAt, time.Now())
	}
}

func TestCache_FailureAppliesExponentialBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := &fakeStore{}
	st.set([]model.Channel{{ID: 8, Name: "ch8", Provider: "openai", Protocol: "openai",
		BaseURL: srv.URL, Models: []string{"m1"}, Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{Interval: 30 * time.Second, MaxBackoff: 10 * time.Minute}
	c := New(cfg, st, kp)
	defer c.Stop()

	// New() triggers startup ProbeAll asynchronously. Wait until
	// the cache reflects at least one failed probe of channel 8.
	deadline := time.Now().Add(2 * time.Second)
	var baseline int
	for time.Now().Before(deadline) {
		r, ok := c.Latest(8)
		if ok && r.ConsecFails > 0 {
			baseline = r.ConsecFails
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if baseline == 0 {
		t.Fatal("startup probe never produced a failed result")
	}
	prevDelay := 30 * time.Second
	for i := 0; i < baseline-1; i++ {
		prevDelay *= 2
		if prevDelay > 10*time.Minute {
			prevDelay = 10 * time.Minute
		}
	}
	r0, _ := c.Latest(8)
	if got := r0.NextProbeAt.Sub(r0.CheckedAt); got != prevDelay {
		t.Errorf("after startup=%d fails, backoff should be %v, got %v", baseline, prevDelay, got)
	}

	ch0 := st.chs[0]
	c.ProbeChannel(context.Background(), &ch0)
	r1, _ := c.Latest(8)
	wantFails := baseline + 1
	if r1.ConsecFails != wantFails {
		t.Errorf("after manual probe, ConsecFails=%d, want %d", r1.ConsecFails, wantFails)
	}
	wantDelay := prevDelay * 2
	if wantDelay > 10*time.Minute {
		wantDelay = 10 * time.Minute
	}
	if got := r1.NextProbeAt.Sub(r1.CheckedAt); got != wantDelay {
		t.Errorf("after %d fails, backoff should be %v, got %v", wantFails, wantDelay, got)
	}

	for i := 0; i < 8; i++ {
		c.ProbeChannel(context.Background(), &ch0)
	}
	rFinal, _ := c.Latest(8)
	if got := rFinal.NextProbeAt.Sub(rFinal.CheckedAt); got > 10*time.Minute+time.Second {
		t.Errorf("backoff should cap at MaxBackoff=10m, got %v", got)
	}
}

func TestCache_SuccessResetsBackoff(t *testing.T) {
	var failNext = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	st := &fakeStore{}
	st.set([]model.Channel{{ID: 9, Name: "ch9", Provider: "openai", Protocol: "openai",
		BaseURL: srv.URL, Models: []string{"m1"}, Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{Interval: 30 * time.Second, HealthyInterval: 5 * time.Minute}
	c := New(cfg, st, kp)
	defer c.Stop()

	ch0 := st.chs[0]
	c.ProbeChannel(context.Background(), &ch0)
	c.ProbeChannel(context.Background(), &ch0)
	failNext = false
	c.ProbeChannel(context.Background(), &ch0)
	r, _ := c.Latest(9)
	if r.OK != true {
		t.Error("after recovery probe, OK should be true")
	}
	if r.ConsecFails != 0 {
		t.Errorf("after recovery, ConsecFails=%d, want 0", r.ConsecFails)
	}
	if r.NextProbeAt.Sub(r.CheckedAt) != 5*time.Minute {
		t.Errorf("after recovery, next probe should be HealthyInterval=5m, got %v", r.NextProbeAt.Sub(r.CheckedAt))
	}
}

func TestCache_TickSkipsChannelsDueLater(t *testing.T) {
	st := &fakeStore{}
	chs := []model.Channel{
		{ID: 10, Name: "early", Status: model.ChannelEnabled},
		{ID: 11, Name: "late", Status: model.ChannelEnabled},
	}
	st.set(chs)
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{Interval: 10 * time.Millisecond}
	c := New(cfg, st, kp)
	defer c.Stop()

	pinned := time.Now().Add(time.Hour)
	c.record(10, Result{OK: true, CheckedAt: time.Now(), NextProbeAt: pinned})
	c.record(11, Result{OK: true, CheckedAt: time.Now(), NextProbeAt: pinned})

	c.tick()
	for id, r := range c.Snapshot() {
		if !r.NextProbeAt.Equal(pinned) {
			t.Errorf("channel %d: NextProbeAt should be unchanged (tick must skip), got %v want %v", id, r.NextProbeAt, pinned)
		}
	}
}

func TestCache_ProbeCadenceEstimate(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()
	if cfg.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", cfg.Interval)
	}
	if cfg.HealthyInterval != 5*time.Minute {
		t.Errorf("HealthyInterval = %v, want 5m", cfg.HealthyInterval)
	}
	if cfg.MaxBackoff != 10*time.Minute {
		t.Errorf("MaxBackoff = %v, want 10m", cfg.MaxBackoff)
	}
	if cfg.RecentTrafficWindow != 5*time.Minute {
		t.Errorf("RecentTrafficWindow = %v, want 5m", cfg.RecentTrafficWindow)
	}
}

// ---------- RecordSuccess / RecordFailure ----------

func TestCache_RecordSuccess_UpdatesLastSuccessAndNextProbe(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 20, Name: "ch20", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{HealthyInterval: 5 * time.Minute}
	c := New(cfg, st, kp)
	defer c.Stop()

	c.RecordSuccess(20)
	r, ok := c.Latest(20)
	if !ok {
		t.Fatal("expected entry after RecordSuccess")
	}
	if !r.LastSuccessAt.Equal(r.CheckedAt) || r.LastSuccessAt.IsZero() {
		t.Errorf("LastSuccessAt should be set to now, got %v", r.LastSuccessAt)
	}
	if r.NextProbeAt.Sub(r.LastSuccessAt) != 5*time.Minute {
		t.Errorf("NextProbeAt should be 5m after success, got %v", r.NextProbeAt.Sub(r.LastSuccessAt))
	}
}

func TestCache_RecordSuccess_ClearsPriorFailure(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 21, Name: "ch21", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	c := New(Config{}, st, kp)
	defer c.Stop()

	c.record(21, Result{OK: false, Error: "stale", CheckedAt: time.Now().Add(-time.Hour)})
	c.RecordSuccess(21)
	r, _ := c.Latest(21)
	if !r.OK {
		t.Error("real success should clear prior probe failure")
	}
	if r.ConsecFails != 0 {
		t.Errorf("ConsecFails should reset to 0, got %d", r.ConsecFails)
	}
}

func TestCache_RecordFailure_ForcesImmediateProbe(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 22, Name: "ch22", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{HealthyInterval: 5 * time.Minute}
	c := New(cfg, st, kp)
	defer c.Stop()

	c.record(22, Result{OK: true, NextProbeAt: time.Now().Add(time.Hour)})
	c.RecordFailure(22)
	r, _ := c.Latest(22)
	if r.NextProbeAt.After(time.Now().Add(time.Second)) {
		t.Errorf("real failure should force immediate probe, got NextProbeAt=%v", r.NextProbeAt)
	}
	if r.ConsecRealFails != 1 {
		t.Errorf("ConsecRealFails should be 1, got %d", r.ConsecRealFails)
	}
}

// ---------- tick with recent traffic ----------

func TestCache_TickSkipsChannelWithRecentSuccess(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 30, Name: "ch30", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{Interval: 10 * time.Millisecond, HealthyInterval: 5 * time.Minute}
	c := New(cfg, st, kp)
	defer c.Stop()

	c.record(30, Result{OK: true, NextProbeAt: time.Now().Add(-time.Hour), CheckedAt: time.Now().Add(-time.Hour)})
	c.RecordSuccess(30)
	// LastSuccessAt is now, NextProbeAt is 5m from now.

	c.tick()
	r, _ := c.Latest(30)
	// tick() must NOT have run a probe — CheckedAt should still
	// be 1h ago (untouched).
	if time.Since(r.CheckedAt) < 1*time.Minute {
		t.Errorf("tick should skip due to recent real success, but CheckedAt was advanced to %v (%.0fs ago)",
			r.CheckedAt, time.Since(r.CheckedAt).Seconds())
	}
	if !r.NextProbeAt.After(time.Now()) {
		t.Errorf("NextProbeAt should remain in future, got %v", r.NextProbeAt)
	}
}

func TestCache_TickProbesChannelAfterRecentTrafficWindow(t *testing.T) {
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 31, Name: "ch31", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}

	cfg := Config{Interval: 10 * time.Millisecond, RecentTrafficWindow: 100 * time.Millisecond, HealthyInterval: 10 * time.Millisecond, Timeout: 100 * time.Millisecond}
	c := New(cfg, st, kp)
	defer c.Stop()

	c.RecordSuccess(31)
	// Wait for traffic window to elapse + NextProbeAt to pass.
	time.Sleep(150 * time.Millisecond)

	c.tick()
	r, _ := c.Latest(31)
	// After window passes, tick should have probed (CheckedAt recent).
	if time.Since(r.CheckedAt) > 1*time.Second {
		t.Errorf("tick should probe after window expires, CheckedAt=%v", r.CheckedAt)
	}
}

// ---------- RouterObserver adapter ----------

func TestRouterObserver_ImplementsInterface(t *testing.T) {
	// Compile-time check: RouterObserver must satisfy
	// router.TrafficObserver. We don't import router here to
	// avoid a test-only dependency, but the runtime dispatch
	// below proves the method shape.
	st := &fakeStore{}
	st.set([]model.Channel{{ID: 40, Name: "ch40", Status: model.ChannelEnabled}})
	kp := &fakeKeyPicker{key: &model.Key{Key: "k"}}
	c := New(Config{}, st, kp)
	defer c.Stop()

	o := NewRouterObserver(c)
	o.OnRealSuccess(40)
	r, ok := c.Latest(40)
	if !ok || r.LastSuccessAt.IsZero() {
		t.Error("OnRealSuccess should populate LastSuccessAt")
	}

	o.OnRealFailure(40)
	r, _ = c.Latest(40)
	if r.ConsecRealFails != 1 {
		t.Errorf("OnRealFailure should bump ConsecRealFails, got %d", r.ConsecRealFails)
	}
}

func TestRouterObserver_NilSafe(t *testing.T) {
	var o *RouterObserver
	o.OnRealSuccess(1)
	o.OnRealFailure(1)
	// Must not panic.
}