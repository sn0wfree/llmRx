package subusage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// fakeStateStore is an in-memory StateStore.
type fakeStateStore struct {
	mu     sync.Mutex
	states map[int64]string
	errs   map[int64]string
	points []store.UsagePoint
}

func newFakeStore() *fakeStateStore {
	return &fakeStateStore{states: map[int64]string{}, errs: map[int64]string{}}
}

func (f *fakeStateStore) UpdateChannelUsageState(chID int64, state string, at time.Time, probeErr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if probeErr != "" {
		f.errs[chID] = probeErr
		return nil
	}
	f.states[chID] = state
	delete(f.errs, chID)
	return nil
}

func (f *fakeStateStore) AppendUsagePoint(p store.UsagePoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.points = append(f.points, p)
	return nil
}

func (f *fakeStateStore) UsagePoints(chID int64, from time.Time, limit int) ([]store.UsagePoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.UsagePoint
	for _, p := range f.points {
		if p.ChannelID == chID && !p.TS.Before(from) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStateStore) PruneUsagePoints(cutoff time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []store.UsagePoint
	var n int64
	for _, p := range f.points {
		if p.TS.Before(cutoff) {
			n++
			continue
		}
		kept = append(kept, p)
	}
	f.points = kept
	return n, nil
}

// fakeChannels serves a fixed channel list.
type fakeChannels struct{ chs []*model.Channel }

func (f *fakeChannels) GetAllChannels() []*model.Channel { return f.chs }

func probeableChannel(id int64) *model.Channel {
	return &model.Channel{
		ID:     id,
		Name:   "c",
		Status: model.ChannelEnabled,
		UsageProbe: model.UsageProbe{
			Kind: model.UsageProbeOpenRouter,
		},
	}
}

func TestMonitor_SweepPersistsReportAndHistory(t *testing.T) {
	st := newFakeStore()
	ch := probeableChannel(1)
	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Report{
			Source: "openrouter_key",
			Points: []UsagePoint{
				{Window: WindowKeyLimit, Used: 5, Limit: 10, Remaining: 5, Unit: UnitUSD},
				{Window: WindowMonthly, Used: 50, Limit: 100, Remaining: 50, Unit: UnitUSD},
			},
		}, nil
	})

	m.Sweep(context.Background())

	if st.states[1] == "" {
		t.Fatal("report not persisted")
	}
	rep := ReportFromState(st.states[1])
	if len(rep.Points) != 2 || rep.Source != "openrouter_key" {
		t.Fatalf("persisted report = %+v", rep)
	}
	if len(st.points) != 2 {
		t.Fatalf("history rows = %d, want 2 (one per window)", len(st.points))
	}
}

func TestMonitor_SkipsDisabledAndUnprobedChannels(t *testing.T) {
	st := newFakeStore()
	disabled := probeableChannel(1)
	disabled.Status = model.ChannelDisabled
	unprobed := &model.Channel{ID: 2, Status: model.ChannelEnabled}
	enabled := probeableChannel(3)

	var probed []int64
	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{disabled, unprobed, enabled}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		probed = append(probed, ch.ID)
		return Report{Source: "x", Points: []UsagePoint{{Window: WindowDaily, Used: 1, Limit: 2}}}, nil
	})
	m.Sweep(context.Background())

	if len(probed) != 1 || probed[0] != 3 {
		t.Fatalf("probed = %v, want only channel 3", probed)
	}
}

func TestMonitor_FailureKeepsPreviousState(t *testing.T) {
	st := newFakeStore()
	st.states[1] = `{"source":"openrouter_key","points":[{"window":"key_limit","used":1,"limit":10,"remaining":9}]}`
	ch := probeableChannel(1)

	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Report{}, errors.New("upstream 500")
	})
	m.Sweep(context.Background())

	if st.errs[1] != "upstream 500" {
		t.Fatalf("error not recorded: %+v", st.errs)
	}
	if st.states[1] == "" {
		t.Fatal("the last good report must survive a failed probe")
	}
	if len(st.points) != 0 {
		t.Fatal("a failed probe must not write history")
	}
}

func TestMonitor_HistoryDedup(t *testing.T) {
	st := newFakeStore()
	ch := probeableChannel(1)
	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Report{Source: "s", Points: []UsagePoint{
			{Window: WindowDaily, Used: 7, Limit: 10, Remaining: 3},
		}}, nil
	})

	m.Sweep(context.Background())
	m.Sweep(context.Background()) // identical reading inside the dedup window
	if len(st.points) != 1 {
		t.Fatalf("identical readings wrote %d rows, want 1", len(st.points))
	}

	// A changed reading must be recorded.
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Report{Source: "s", Points: []UsagePoint{
			{Window: WindowDaily, Used: 8, Limit: 10, Remaining: 2},
		}}, nil
	})
	m.Sweep(context.Background())
	if len(st.points) != 2 {
		t.Fatalf("changed reading wrote %d rows, want 2", len(st.points))
	}
}

func TestMonitor_PerChannelIntervalOverride(t *testing.T) {
	st := newFakeStore()
	ch := probeableChannel(1)
	ch.UsageProbe.IntervalSec = 3600
	recent := time.Now().Add(-time.Minute)
	ch.UsageCheckedAt = &recent

	var probed int
	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		probed++
		return Report{Source: "s", Points: []UsagePoint{{Window: WindowDaily, Used: 1, Limit: 2}}}, nil
	})
	m.Sweep(context.Background())
	if probed != 0 {
		t.Fatal("a channel checked inside its own interval must be skipped")
	}
}

func TestMonitor_BuildInputNeedsKeyForOpenRouter(t *testing.T) {
	st := newFakeStore()
	ch := probeableChannel(1)
	var gotKey string
	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, fakeKeys{key: "sk-test"}, st)
	m.SetProbe(func(ctx context.Context, c *model.Channel, in Input) (Report, error) {
		gotKey = in.APIKey
		return Report{Source: "s", Points: []UsagePoint{{Window: WindowDaily, Used: 1, Limit: 2}}}, nil
	})
	m.Sweep(context.Background())
	if gotKey != "sk-test" {
		t.Fatalf("API key not passed to the parser: %q", gotKey)
	}
}

// fakeKeys is a KeyProvider returning one fixed key.
type fakeKeys struct{ key string }

func (f fakeKeys) NextKey(int64) (*model.Key, error) { return &model.Key{Key: f.key}, nil }

func TestMonitor_ConcurrentSweeps(t *testing.T) {
	st := newFakeStore()
	ch := probeableChannel(1)
	m := New(Config{Interval: time.Hour, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Report{Source: "s", Points: []UsagePoint{{Window: WindowDaily, Used: 1, Limit: 2}}}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Sweep(context.Background())
		}()
	}
	wg.Wait()
	if st.states[1] == "" {
		t.Fatal("state lost under concurrent sweeps")
	}
}

func TestMonitor_StartStop(t *testing.T) {
	st := newFakeStore()
	ch := probeableChannel(1)
	m := New(Config{Interval: 20 * time.Millisecond, Timeout: time.Second},
		&fakeChannels{chs: []*model.Channel{ch}}, nil, st)
	m.SetProbe(func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Report{Source: "s", Points: []UsagePoint{{Window: WindowDaily, Used: 1, Limit: 2}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go m.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	cancel()
	m.Stop() // must not deadlock
	if st.states[1] == "" {
		t.Fatal("the loop never probed")
	}
}

func TestReportFromState_CorruptIsEmpty(t *testing.T) {
	if rep := ReportFromState("{not json"); rep.Source != "" || len(rep.Points) != 0 {
		t.Fatalf("corrupt state = %+v", rep)
	}
	if rep := ReportFromState(""); rep.Source != "" {
		t.Fatalf("empty state = %+v", rep)
	}
}

func TestReport_TightestIgnoresUnbounded(t *testing.T) {
	rep := Report{Points: []UsagePoint{
		{Window: WindowCredits, Used: 999, Limit: 0},
		{Window: WindowDaily, Used: 1, Limit: 10},
	}}
	tight := rep.Tightest()
	if tight == nil || tight.Window != WindowDaily {
		t.Fatalf("tightest = %+v (unbounded windows must be skipped)", tight)
	}
}
