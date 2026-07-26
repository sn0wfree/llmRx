package thompson

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestResetAll(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordSuccess(1)
	s.RecordFailure(1)
	s.RecordSuccess(2)
	s.RecordFailure(2)
	if len(s.Snapshot()) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(s.Snapshot()))
	}
	s.ResetAll()
	if len(s.Snapshot()) != 0 {
		t.Fatalf("expected 0 channels after ResetAll, got %d", len(s.Snapshot()))
	}
}

func TestResetAllEmpty(t *testing.T) {
	s := New(Config{Seed: 1})
	s.ResetAll()
	if len(s.Snapshot()) != 0 {
		t.Fatalf("expected 0 channels, got %d", len(s.Snapshot()))
	}
}

func TestResetSingleChannel(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RecordSuccess(1)
	s.RecordSuccess(2)
	s.Reset(1)
	snap := s.Snapshot()
	if _, ok := snap[1]; ok {
		t.Fatal("channel 1 should be removed after Reset")
	}
	if _, ok := snap[2]; !ok {
		t.Fatal("channel 2 should still exist")
	}
}

func TestLoadMalformedFile(t *testing.T) {
	s := New(Config{Seed: 1})
	dir := t.TempDir()
	path := dir + "/bad.json"
	if err := writeFile(path, []byte("not valid json")); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(path); err == nil {
		t.Fatal("expected error for malformed state file")
	}
}

func TestLoadWrongVersion(t *testing.T) {
	s := New(Config{Seed: 1})
	dir := t.TempDir()
	path := dir + "/wrongver.json"
	if err := writeFile(path, []byte(`{"version":99,"seed":0,"betas":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(path); err == nil {
		t.Fatal("expected error for wrong version")
	}
}

func TestSampleSingleChannel(t *testing.T) {
	s := New(Config{Seed: 1})
	c := ch(1, 5)
	ranked := s.Sample([]*model.Channel{c})
	if len(ranked) != 1 {
		t.Fatalf("expected 1 ranked, got %d", len(ranked))
	}
	if ranked[0].Channel.ID != 1 {
		t.Fatalf("expected channel 1, got %d", ranked[0].Channel.ID)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	s := New(Config{Seed: 1})
	snap := s.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected empty snapshot, got %d", len(snap))
	}
}
