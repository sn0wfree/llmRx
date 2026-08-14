package router

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func chWithPrice(id int64, price float64) *model.Channel {
	return &model.Channel{ID: id, Name: "c", InputPrice: price, OutputPrice: 0}
}

// TestWeightedRandom_InverseSquare: channels priced 1:2:3 receive
// ~36:9:4 of the first picks (weight = 1/price²), i.e. ~73% / 18%
// / 8% — biased strongly toward cheap without starving the rest.
func TestWeightedRandom_InverseSquare(t *testing.T) {
	s := WeightedRandomStrategy{}
	chs := []*model.Channel{
		chWithPrice(1, 1),
		chWithPrice(2, 2),
		chWithPrice(3, 3),
	}
	const N = 6000
	picks := [3]int{}
	for i := 0; i < N; i++ {
		out := s.Sort(chs)
		switch out[0].ID {
		case 1:
			picks[0]++
		case 2:
			picks[1]++
		case 3:
			picks[2]++
		}
	}
	// Expected: 36/49, 9/49, 4/49 of N.
	expect := [3]float64{float64(N) * 36 / 49, float64(N) * 9 / 49, float64(N) * 4 / 49}
	for i := 0; i < 3; i++ {
		got := float64(picks[i])
		diff := got - expect[i]
		if diff < -0.04*float64(N) || diff > 0.04*float64(N) {
			t.Errorf("channel %d picked %d times, want ~%.0f (±%d)", i+1, picks[i], expect[i], int(0.04*float64(N)))
		}
	}
}

// TestWeightedRandom_ZeroPriceFirst: unpriced channels (price 0)
// are treated as cheapest — they must win the vast majority of
// picks against priced competition.
func TestWeightedRandom_ZeroPriceFirst(t *testing.T) {
	s := WeightedRandomStrategy{}
	chs := []*model.Channel{
		chWithPrice(1, 0),
		chWithPrice(2, 10),
	}
	const N = 1000
	zeroWins := 0
	for i := 0; i < N; i++ {
		if s.Sort(chs)[0].ID == 1 {
			zeroWins++
		}
	}
	// weight 1 vs 1/100 → zero-price should win ~99% of picks.
	if zeroWins < int(0.97*float64(N)) {
		t.Errorf("zero-price channel won %d/%d, want >= 97%%", zeroWins, N)
	}
}

func TestWeightedRandom_Single(t *testing.T) {
	s := WeightedRandomStrategy{}
	out := s.Sort([]*model.Channel{chWithPrice(7, 5)})
	if len(out) != 1 || out[0].ID != 7 {
		t.Fatalf("single channel: %v", out)
	}
}

func TestWeightedRandom_Empty(t *testing.T) {
	s := WeightedRandomStrategy{}
	if out := s.Sort(nil); len(out) != 0 {
		t.Fatalf("empty: %v", out)
	}
}

func TestStrategyRegistryIncludesWeightedRandom(t *testing.T) {
	if _, ok := strategyMap[model.StrategyWeightedRandom]; !ok {
		t.Fatal("weighted_random strategy not registered")
	}
	got := strategyFromName(model.StrategyWeightedRandom, nil)
	if got.Name() != string(model.StrategyWeightedRandom) {
		t.Fatalf("strategyFromName: %v", got.Name())
	}
}

// fakeLatencyProvider is a LatencyProvider with a fixed per-channel
// latency map.
type fakeLatencyProvider struct {
	ms map[int64]float64
}

func (f *fakeLatencyProvider) LatencyMS(channelID int64) float64 { return f.ms[channelID] }

func TestLatencyStrategy_SortsByEWMA(t *testing.T) {
	s := LatencyStrategy{Provider: &fakeLatencyProvider{
		ms: map[int64]float64{1: 500, 2: 100, 3: 300},
	}}
	chs := []*model.Channel{chWithPrice(1, 1), chWithPrice(2, 1), chWithPrice(3, 1)}
	out := s.Sort(chs)
	// Fastest (2:100ms) first, then 3:300ms, then 1:500ms.
	want := []int64{2, 3, 1}
	for i, id := range want {
		if out[i].ID != id {
			t.Fatalf("pos %d: want channel %d, got %d", i, id, out[i].ID)
		}
	}
}

func TestLatencyStrategy_UnknownChannelsTrail(t *testing.T) {
	s := LatencyStrategy{Provider: &fakeLatencyProvider{
		ms: map[int64]float64{1: 500, 2: 100},
	}}
	chs := []*model.Channel{chWithPrice(3, 1), chWithPrice(1, 1), chWithPrice(2, 1)}
	out := s.Sort(chs)
	// Known latencies sorted first; unknown (3) keeps input order at the end.
	if out[0].ID != 2 || out[1].ID != 1 || out[2].ID != 3 {
		t.Fatalf("want [2,1,3], got [%d,%d,%d]", out[0].ID, out[1].ID, out[2].ID)
	}
}

func TestLatencyStrategy_NilProviderNoop(t *testing.T) {
	s := LatencyStrategy{}
	chs := []*model.Channel{chWithPrice(2, 1), chWithPrice(1, 1)}
	out := s.Sort(chs)
	if len(out) != 2 || out[0].ID != 2 || out[1].ID != 1 {
		t.Fatalf("nil provider must preserve input order, got %d,%d", out[0].ID, out[1].ID)
	}
}

func TestCostLatencyBlend_SortsByBlendedScore(t *testing.T) {
	s := CostLatencyBlendStrategy{
		Provider:      &fakeLatencyProvider{ms: map[int64]float64{1: 100, 2: 900}},
		CostWeight:    0.7,
		LatencyWeight: 0.3,
	}
	// ch1: price=1, lat=100ms -> score = 0.7*(1/5) + 0.3*(100/900) = 0.14 + 0.033 = 0.173
	// ch2: price=5, lat=900ms -> score = 0.7*(5/5) + 0.3*(900/900) = 0.7 + 0.3 = 1.0
	// ch1 wins.
	chs := []*model.Channel{chWithPrice(2, 5), chWithPrice(1, 1)}
	out := s.Sort(chs)
	if out[0].ID != 1 {
		t.Fatalf("blend: want cheap+fast channel first, got %d", out[0].ID)
	}
}

func TestCostLatencyBlend_DefaultWeights(t *testing.T) {
	s := CostLatencyBlendStrategy{
		Provider: &fakeLatencyProvider{ms: map[int64]float64{1: 100, 2: 900}},
	}
	// Zero weights -> default 0.7/0.3. Same expectation as above.
	chs := []*model.Channel{chWithPrice(2, 5), chWithPrice(1, 1)}
	out := s.Sort(chs)
	if out[0].ID != 1 {
		t.Fatalf("default weights: want cheap+fast channel first, got %d", out[0].ID)
	}
}

func TestStrategyFromNameLatencyStrategies(t *testing.T) {
	lp := &fakeLatencyProvider{ms: map[int64]float64{1: 50}}
	if s := strategyFromName(model.StrategyLatency, lp); s.Name() != string(model.StrategyLatency) {
		t.Fatalf("latency name mismatch: %v", s.Name())
	}
	if s := strategyFromName(model.StrategyCostLatency, lp); s.Name() != string(model.StrategyCostLatency) {
		t.Fatalf("blend name mismatch: %v", s.Name())
	}
	if s := strategyFromName(model.StrategyCheapest, lp); s.Name() != string(model.StrategyCheapest) {
		t.Fatalf("cheapest must remain registered: %v", s.Name())
	}
}
