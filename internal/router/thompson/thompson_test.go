package thompson

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func ch(id int64, prio int) *model.Channel {
	return &model.Channel{ID: id, Name: "c", Provider: "p", Priority: prio}
}

func TestBetaPriorUniform(t *testing.T) {
	s := New(Config{Seed: 42})
	got := s.Snapshot()
	if len(got) != 0 {
		t.Fatalf("expected empty snapshot, got %v", got)
	}
}

func TestRecordUpdatesPosterior(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordSuccess(1)
	s.RecordSuccess(1)
	s.RecordFailure(1)
	snap := s.Snapshot()
	ab := snap[1]
	// Prior was (1,1); +2 alpha, +1 beta.
	if ab[0] != 3 || ab[1] != 2 {
		t.Fatalf("posterior: %v", ab)
	}
}

func TestReset(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordSuccess(1)
	s.RecordSuccess(1)
	s.Reset(1)
	if _, ok := s.Snapshot()[1]; ok {
		t.Fatal("expected reset to remove channel 1")
	}
}

func TestSampleRankingConverges(t *testing.T) {
	s := New(Config{Seed: 7, BlendStaticWeight: 0, ExploreFraction: 0})
	// Channel 1: 100 successes, 0 failures  → posterior near 1
	// Channel 2: 0 successes, 100 failures → posterior near 0
	for i := 0; i < 100; i++ {
		s.RecordSuccess(1)
		s.RecordFailure(2)
	}
	c1 := ch(1, 0)
	c2 := ch(2, 0)
	// Run many samples; the good channel should be selected >99% of
	// the time when exploration is off.
	hits := 0
	const N = 200
	for i := 0; i < N; i++ {
		out := s.Sample([]*model.Channel{c1, c2})
		if out[0].Channel.ID == 1 {
			hits++
		}
	}
	if hits < int(0.99*float64(N)) {
		t.Fatalf("expected >= 99%% of samples to pick channel 1, got %d/%d", hits, N)
	}
}

func TestSampleBlendHonoursStatic(t *testing.T) {
	// With blend=1, static priority decides regardless of posterior.
	s := New(Config{Seed: 1, BlendStaticWeight: 1, ExploreFraction: 0})
	for i := 0; i < 100; i++ {
		s.RecordSuccess(2) // channel 2 is succeeding
		s.RecordFailure(1) // channel 1 is failing
	}
	c1 := ch(1, 100) // high static
	c2 := ch(2, 0)   // low static
	for i := 0; i < 50; i++ {
		out := s.Sample([]*model.Channel{c1, c2})
		if out[0].Channel.ID != 1 {
			t.Fatalf("static priority should win: got %v", out)
		}
	}
}

func TestSampleEmpty(t *testing.T) {
	s := New(Config{Seed: 1})
	if out := s.Sample(nil); len(out) != 0 {
		t.Fatalf("empty: %v", out)
	}
}
