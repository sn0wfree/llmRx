package thompson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestArmRecordUpdatesPosterior(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordArmSuccess("simple:deepseek-chat")
	s.RecordArmSuccess("simple:deepseek-chat")
	s.RecordArmFailure("simple:deepseek-chat")
	s.RecordArmFailure("complex:gpt-4o")
	snap := s.SnapshotArms()
	ab, ok := snap["simple:deepseek-chat"]
	if !ok {
		t.Fatalf("arm missing from snapshot: %v", snap)
	}
	// Prior was (1,1); +2 alpha, +1 beta.
	if ab[0] != 3 || ab[1] != 2 {
		t.Fatalf("posterior: %v", ab)
	}
	if _, ok := snap["complex:gpt-4o"]; !ok {
		t.Fatal("other arm should be present too")
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("arm records must not touch the channel space")
	}
}

func TestResetArm(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordArmSuccess("a")
	s.RecordArmSuccess("b")
	s.ResetArm("a")
	snap := s.SnapshotArms()
	if _, ok := snap["a"]; ok {
		t.Fatal("arm a should be removed after ResetArm")
	}
	if _, ok := snap["b"]; !ok {
		t.Fatal("arm b should still exist")
	}
}

// TestSampleArmsColdStartGate: below the min-samples threshold the
// input (cost) order must be preserved untouched, mirroring the
// channel sampler's L5 gate.
func TestSampleArmsColdStartGate(t *testing.T) {
	s := New(Config{Seed: 3, MinSamplesPerChannel: 5})
	keys := []string{"simple:deepseek-chat", "simple:gpt-4o-mini"}
	// Warm arm "b" far above the gate but leave "a" cold.
	for i := 0; i < 10; i++ {
		s.RecordArmSuccess(keys[1])
	}
	out := s.SampleArms(keys)
	if len(out) != 2 {
		t.Fatalf("expected 2 arms, got %d", len(out))
	}
	if out[0].Arm != keys[0] {
		t.Fatalf("cold start must preserve input order, got %v", out)
	}
	if out[0].Score != 0 || out[1].Score != 0 {
		t.Fatalf("cold start scores must be 0, got %v", out)
	}
}

// TestSampleArmsWarmConverges: once both arms clear the gate, the
// high-quality arm is selected ~always (pure θ, no static blend).
func TestSampleArmsWarmConverges(t *testing.T) {
	s := New(Config{Seed: 7, BlendStaticWeight: 0, ExploreFraction: 0})
	good, bad := "simple:deepseek-chat", "simple:gpt-4o-mini"
	for i := 0; i < 100; i++ {
		s.RecordArmSuccess(good)
		s.RecordArmFailure(bad)
	}
	hits := 0
	const N = 200
	for i := 0; i < N; i++ {
		out := s.SampleArms([]string{bad, good}) // bad first in cost order
		if out[0].Arm == good {
			hits++
		}
	}
	if hits < int(0.99*float64(N)) {
		t.Fatalf("expected >= 99%% of samples to pick %q, got %d/%d", good, hits, N)
	}
}

func TestSampleArmsEmpty(t *testing.T) {
	s := New(Config{Seed: 1})
	if out := s.SampleArms(nil); len(out) != 0 {
		t.Fatalf("empty: %v", out)
	}
	if out := s.SampleArms([]string{}); len(out) != 0 {
		t.Fatalf("empty: %v", out)
	}
}

// TestSaveLoadRoundTripV2: both arm spaces must survive a
// Save→Load cycle.
func TestSaveLoadRoundTripV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thompson.json")
	a := New(Config{Seed: 42})
	a.RecordSuccess(1)
	a.RecordSuccess(2)
	a.RecordFailure(2)
	a.RecordArmSuccess("simple:deepseek-chat")
	a.RecordArmSuccess("complex:gpt-4o")
	a.RecordArmFailure("complex:gpt-4o")
	if err := a.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b := New(Config{Seed: 99})
	if err := b.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	chA, chB := a.Snapshot(), b.Snapshot()
	if len(chA) != len(chB) {
		t.Fatalf("channel length mismatch: a=%v b=%v", chA, chB)
	}
	for id, abA := range chA {
		if abA != chB[id] {
			t.Fatalf("channel %d: a=%v b=%v", id, abA, chB[id])
		}
	}
	armA, armB := a.SnapshotArms(), b.SnapshotArms()
	if len(armA) != len(armB) {
		t.Fatalf("arm length mismatch: a=%v b=%v", armA, armB)
	}
	for key, abA := range armA {
		if abA != armB[key] {
			t.Fatalf("arm %q: a=%v b=%v", key, abA, armB[key])
		}
	}
}

// TestSaveWritesVersion2: the on-disk format must be version 2
// with channels+arms (no v1 "betas" field).
func TestSaveWritesVersion2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thompson.json")
	s := New(Config{Seed: 1})
	s.RecordSuccess(5)
	s.RecordArmSuccess("simple:deepseek-chat")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatal(err)
	}
	if sf.Version != 2 {
		t.Fatalf("version = %d, want 2", sf.Version)
	}
	if len(sf.Betas) != 0 {
		t.Fatalf("v2 file must not carry the v1 betas field: %v", sf.Betas)
	}
	if sf.Channels[5] != [2]float64{2, 1} {
		t.Fatalf("channels: %v", sf.Channels)
	}
	if sf.Arms["simple:deepseek-chat"] != [2]float64{2, 1} {
		t.Fatalf("arms: %v", sf.Arms)
	}
}

// TestLoadV1Migrates: a v1 file ("betas" keyed by channel ID)
// loads transparently, populates the channel space, and the next
// Save rewrites the file in v2 format without losing the data.
func TestLoadV1Migrates(t *testing.T) {
	dir := t.TempDir()
	v1 := filepath.Join(dir, "v1.json")
	if err := writeFile(v1, []byte(`{"version":1,"seed":0,"betas":{"7":[2,3],"8":[1,1]}}`)); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Seed: 5})
	if err := s.Load(v1); err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if got := s.Snapshot()[7]; got != [2]float64{2, 3} {
		t.Fatalf("channel 7 posterior: %v", got)
	}
	if got := s.Snapshot()[8]; got != [2]float64{1, 1} {
		t.Fatalf("channel 8 posterior: %v", got)
	}
	if len(s.SnapshotArms()) != 0 {
		t.Fatal("v1 file must not produce arm state")
	}

	out := filepath.Join(dir, "out.json")
	if err := s.Save(out); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatal(err)
	}
	if sf.Version != 2 {
		t.Fatalf("migrated file version = %d, want 2", sf.Version)
	}
	if sf.Channels[7] != [2]float64{2, 3} {
		t.Fatalf("migrated channel 7: %v", sf.Channels)
	}
	if len(sf.Betas) != 0 {
		t.Fatalf("migrated file must drop the v1 betas field: %v", sf.Betas)
	}
}

func TestResetAllClearsArms(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordSuccess(1)
	s.RecordArmSuccess("simple:deepseek-chat")
	s.RecordArmFailure("complex:gpt-4o")
	s.ResetAll()
	if len(s.Snapshot()) != 0 {
		t.Fatal("channels not cleared")
	}
	if len(s.SnapshotArms()) != 0 {
		t.Fatal("arms not cleared")
	}
}

// TestArmConcurrency: arm records, sampling and snapshots must be
// safe under parallel load (race detector).
func TestArmConcurrency(t *testing.T) {
	s := New(Config{Seed: 1})
	const goroutines = 8
	const iters = 200
	keys := []string{"simple:a", "simple:b", "complex:c"}
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				k := keys[(g+i)%len(keys)]
				if (g+i)%2 == 0 {
					s.RecordArmSuccess(k)
				} else {
					s.RecordArmFailure(k)
				}
				_ = s.SampleArms(keys)
				_ = s.SnapshotArms()
			}
		}(g)
	}
	wg.Wait()
	snap := s.SnapshotArms()
	for _, k := range keys {
		if _, ok := snap[k]; !ok {
			t.Fatalf("arm %q missing after concurrency", k)
		}
	}
}
