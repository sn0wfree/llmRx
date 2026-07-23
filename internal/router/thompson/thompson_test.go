package thompson

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// writeFile is a tiny helper to keep the test bodies compact.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

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

// TestNew_SeedZeroUsesTime verifies that the default cfg.Seed==0
// path produces a time-based seed (not the legacy fixed=1 that
// made every gateway instance draw the same samples).
func TestNew_SeedZeroUsesTime(t *testing.T) {
	a := New(Config{})
	b := New(Config{})
	// Two consecutive New() calls should draw different first
	// samples — proving the RNG was seeded differently. We can't
	// access the seed directly but the public RNG output differs.
	if a.rng.Int63() == b.rng.Int63() {
		t.Fatalf("seed=0 should produce distinct RNG streams")
	}
}

// TestSaveLoadRoundTrip: a sampler's posterior must survive a
// Save→Load cycle, so a graceful shutdown + restart doesn't drop
// L5 back to the uniform prior.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thompson.json")
	a := New(Config{Seed: 42})
	a.RecordSuccess(1)
	a.RecordSuccess(1)
	a.RecordFailure(2)
	a.RecordSuccess(2)
	if err := a.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Fresh sampler with a different seed: Load must overwrite
	// its state.
	b := New(Config{Seed: 99})
	if err := b.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotA := a.Snapshot()
	gotB := b.Snapshot()
	if len(gotA) != len(gotB) {
		t.Fatalf("length mismatch: a=%v b=%v", gotA, gotB)
	}
	for id, abA := range gotA {
		abB, ok := gotB[id]
		if !ok {
			t.Fatalf("channel %d missing from loaded snapshot", id)
		}
		if abA != abB {
			t.Fatalf("channel %d: a=%v b=%v", id, abA, abB)
		}
	}
}

// TestLoadMissingIsNoOp: a fresh install with no state file must
// not error — L5 simply starts with the uniform prior.
func TestLoadMissingIsNoOp(t *testing.T) {
	s := New(Config{Seed: 1})
	if err := s.Load(filepath.Join(t.TempDir(), "no-such-file.json")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatalf("missing file should leave state untouched, got %v", s.Snapshot())
	}
}

// TestLoadMalformedErrors: a corrupted state file must NOT
// silently fall back to the uniform prior — that would undo
// weeks of learned weights.
func TestLoadMalformedErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thompson.json")
	if err := writeFile(path, []byte("not-json{")); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Seed: 1})
	if err := s.Load(path); err == nil {
		t.Fatal("expected error for malformed state file")
	}
}

// TestLoadVersionMismatch: a future schema bump must refuse to
// load the old file (operator decides whether to delete or
// migrate).
func TestLoadVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thompson.json")
	if err := writeFile(path, []byte(`{"version":99,"betas":{}}`)); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Seed: 1})
	if err := s.Load(path); err == nil {
		t.Fatal("expected error for version mismatch")
	}
}
