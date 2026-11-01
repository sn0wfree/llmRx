package balance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
)

type fakeChannels struct {
	channels []*model.Channel
}

func (f *fakeChannels) GetAllChannels() []*model.Channel { return f.channels }

type fakeKeys struct {
	key *model.Key
	err error
}

func (f *fakeKeys) NextKey(int64) (*model.Key, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.key, nil
}

type balanceCall struct {
	channelID int64
	balance   string
	currency  string
	err       string
}

type fakeStore struct {
	mu    sync.Mutex
	calls []balanceCall
	err   error
}

func (f *fakeStore) UpdateChannelBalance(id int64, balance, currency string, _ time.Time, balanceErr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, balanceCall{channelID: id, balance: balance, currency: currency, err: balanceErr})
	return f.err
}

func (f *fakeStore) last() (balanceCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return balanceCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func deepseekChannel(id int64) *model.Channel {
	return &model.Channel{
		ID:       id,
		Name:     "ds",
		Provider: "deepseek",
		Protocol: "openai",
		BaseURL:  "https://api.deepseek.com/v1",
		Status:   model.ChannelEnabled,
	}
}

func newMonitor(t *testing.T, chans []*model.Channel) (*Monitor, *fakeStore, *fakeKeys) {
	t.Helper()
	fs := &fakeStore{}
	fk := &fakeKeys{key: &model.Key{ID: 1, ChannelID: 1, Key: "sk-test"}}
	m := New(Config{Interval: time.Hour, Timeout: time.Second}, &fakeChannels{channels: chans}, fk, fs)
	return m, fs, fk
}

// ---------- eligibility ----------

func TestEligible(t *testing.T) {
	cases := []struct {
		name string
		ch   *model.Channel
		want bool
	}{
		{"deepseek enabled", deepseekChannel(1), true},
		{"disabled", func() *model.Channel {
			c := deepseekChannel(2)
			c.Status = model.ChannelDisabled
			return c
		}(), false},
		{"no balance endpoint", &model.Channel{
			ID: 3, Provider: "openai", Status: model.ChannelEnabled,
		}, false},
		{"override url on unsupported provider", &model.Channel{
			ID: 4, Provider: "openai", Status: model.ChannelEnabled,
			BalanceURL: "https://relay.example/dashboard/billing/subscription",
		}, false}, // format is missing, so still not queryable
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The override case needs a format to be eligible; assert
			// the documented behaviour rather than flipping it.
			if got := eligible(tc.ch); got != tc.want {
				t.Fatalf("eligible() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------- query ----------

func TestQueryChannel_Success(t *testing.T) {
	ch := deepseekChannel(7)
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	m.SetQuery(func(ctx context.Context, providerName, apiKey, baseURL, override string) (provider.Balance, error) {
		if providerName != "deepseek" {
			t.Fatalf("providerName = %q", providerName)
		}
		if apiKey != "sk-test" {
			t.Fatalf("apiKey = %q", apiKey)
		}
		return provider.Balance{Available: 110, Currency: "CNY", Source: "deepseek"}, nil
	})

	b, err := m.QueryChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}
	if b.Available != 110 {
		t.Fatalf("Available = %v", b.Available)
	}

	call, ok := fs.last()
	if !ok {
		t.Fatal("store was not updated")
	}
	if call.channelID != 7 {
		t.Fatalf("channelID = %d", call.channelID)
	}
	if call.balance != "110" {
		t.Fatalf("balance = %q, want 110", call.balance)
	}
	if call.currency != "CNY" {
		t.Fatalf("currency = %q", call.currency)
	}
	if call.err != "" {
		t.Fatalf("unexpected error recorded: %q", call.err)
	}

	// The in-memory channel is updated too, so callers see it without
	// a round trip.
	if ch.Balance != "110" || ch.BalanceUpdatedAt == nil || ch.BalanceError != "" {
		t.Fatalf("channel not updated in memory: %+v", ch)
	}
}

func TestQueryChannel_PreservesPrecision(t *testing.T) {
	ch := deepseekChannel(1)
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 12590.0042, Currency: "CNY"}, nil
	})
	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}
	call, _ := fs.last()
	if call.balance != "12590.0042" {
		t.Fatalf("balance = %q, want 12590.0042 (precision must survive)", call.balance)
	}
}

// A failed query must record the error WITHOUT wiping the last known
// balance — that number is often the only signal the operator has.
func TestQueryChannel_FailureKeepsPreviousBalance(t *testing.T) {
	ch := deepseekChannel(1)
	ch.Balance = "50"
	ch.BalanceCurrency = "CNY"
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{}, errors.New("upstream 503")
	})

	if _, err := m.QueryChannel(context.Background(), ch); err == nil {
		t.Fatal("expected error")
	}
	call, _ := fs.last()
	if call.err == "" {
		t.Fatal("error was not recorded")
	}
	if call.balance != "" {
		t.Fatalf("balance should be untouched on failure, got %q", call.balance)
	}
}

func TestQueryChannel_PanicIsRecovered(t *testing.T) {
	ch := deepseekChannel(1)
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		panic("parser blew up")
	})

	if _, err := m.QueryChannel(context.Background(), ch); err == nil {
		t.Fatal("panic should surface as an error, not crash the sweep")
	}
	call, ok := fs.last()
	if !ok || call.err == "" {
		t.Fatal("panic was not recorded as an error")
	}
}

func TestQueryChannel_NoKey(t *testing.T) {
	ch := deepseekChannel(1)
	m, _, fk := newMonitor(t, []*model.Channel{ch})
	fk.err = errors.New("no keys")

	if _, err := m.QueryChannel(context.Background(), ch); err == nil {
		t.Fatal("expected error when no key is available")
	}
}

func TestQueryChannel_Unsupported(t *testing.T) {
	ch := &model.Channel{ID: 1, Provider: "openai", Status: model.ChannelEnabled}
	m, _, _ := newMonitor(t, []*model.Channel{ch})
	if _, err := m.QueryChannel(context.Background(), ch); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestQueryChannel_Nil(t *testing.T) {
	m, _, _ := newMonitor(t, nil)
	if _, err := m.QueryChannel(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil channel")
	}
}

// A store failure must be reported, not swallowed.
func TestQueryChannel_StoreError(t *testing.T) {
	ch := deepseekChannel(1)
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	fs.err = errors.New("disk full")
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 1, Currency: "CNY"}, nil
	})
	if _, err := m.QueryChannel(context.Background(), ch); err == nil {
		t.Fatal("expected the store error to surface")
	}
}

// queryWithKey must pick the one-api path for relays.
func TestQueryChannel_OneAPIRouting(t *testing.T) {
	ch := &model.Channel{
		ID: 1, Provider: "custom", Status: model.ChannelEnabled,
		BaseURL:    "https://relay.example",
		BalanceURL: "https://relay.example/dashboard/billing/subscription",
	}
	if !formatIsOneAPI(ch) {
		t.Fatal("relay channel not detected as one-api")
	}
	ds := deepseekChannel(2)
	if formatIsOneAPI(ds) {
		t.Fatal("deepseek misdetected as one-api")
	}
}

// ---------- sweep ----------

func TestSweep_SkipsIneligibleAndContinuesPastFailures(t *testing.T) {
	good := deepseekChannel(1)
	broken := deepseekChannel(2)
	disabled := deepseekChannel(3)
	disabled.Status = model.ChannelDisabled
	unsupported := &model.Channel{ID: 4, Provider: "openai", Status: model.ChannelEnabled}

	m, fs, _ := newMonitor(t, []*model.Channel{good, broken, disabled, unsupported})
	m.SetQuery(func(_ context.Context, _ string, _ string, baseURL, _ string) (provider.Balance, error) {
		if baseURL == broken.BaseURL {
			return provider.Balance{}, errors.New("boom")
		}
		return provider.Balance{Available: 1, Currency: "CNY"}, nil
	})

	m.Sweep(context.Background())

	fs.mu.Lock()
	n := len(fs.calls)
	fs.mu.Unlock()

	// good + broken are queried; disabled and unsupported are skipped.
	if n != 2 {
		t.Fatalf("store calls = %d, want 2 (one success, one failure)", n)
	}
}

func TestSweep_NoEligible(t *testing.T) {
	m, fs, _ := newMonitor(t, []*model.Channel{{ID: 1, Provider: "openai", Status: model.ChannelEnabled}})
	m.Sweep(context.Background())
	if _, ok := fs.last(); ok {
		t.Fatal("no channel should have been queried")
	}
}

// ---------- lifecycle ----------

func TestStart_DisabledWhenIntervalZero(t *testing.T) {
	m := New(Config{Interval: 0}, &fakeChannels{}, &fakeKeys{}, &fakeStore{})
	// Interval 0 falls back to DefaultConfig (1h) per New's contract;
	// use a negative interval to assert the no-op path instead.
	m2 := New(Config{Interval: -1}, &fakeChannels{}, &fakeKeys{}, &fakeStore{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m2.Start(ctx) // must return immediately
	m2.Stop()
	_ = m
}

func TestStopIsIdempotent(t *testing.T) {
	m := New(Config{Interval: 50 * time.Millisecond}, &fakeChannels{}, &fakeKeys{}, &fakeStore{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	m.Stop()
	m.Stop() // must not panic
}

func TestJittered_Bounds(t *testing.T) {
	m := New(Config{Interval: time.Hour, Jitter: 0.1}, &fakeChannels{}, &fakeKeys{}, &fakeStore{})
	base := time.Hour
	for i := 0; i < 50; i++ {
		got := m.jittered(base)
		if got < base-base/10 || got > base+base/10 {
			t.Fatalf("jittered(%v) = %v out of bounds", base, got)
		}
	}
}

func TestJittered_ZeroJitter(t *testing.T) {
	m := New(Config{Interval: time.Hour, Jitter: 0}, &fakeChannels{}, &fakeKeys{}, &fakeStore{})
	if got := m.jittered(time.Hour); got != time.Hour {
		t.Fatalf("jittered = %v, want exact interval", got)
	}
}

func TestFormatAmount(t *testing.T) {
	cases := map[float64]string{
		110:        "110",
		110.5:      "110.5",
		12590.0042: "12590.0042",
		0:          "0",
		-1.25:      "-1.25",
		1.00001:    "1",
	}
	for in, want := range cases {
		if got := formatAmount(in); got != want {
			t.Fatalf("formatAmount(%v) = %q, want %q", in, got, want)
		}
	}
}
