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
	got := strategyFromName(model.StrategyWeightedRandom)
	if got.Name() != string(model.StrategyWeightedRandom) {
		t.Fatalf("strategyFromName: %v", got.Name())
	}
}
