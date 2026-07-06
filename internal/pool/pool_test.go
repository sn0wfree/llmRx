package pool

import (
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestPool_RoundRobinSequential(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyActive},
			{Key: "k2", Status: model.KeyActive},
			{Key: "k3", Status: model.KeyActive},
		},
	}

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		k, err := p.NextKey(1)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		seen[k.Key]++
	}
	// 6 requests across 3 keys → each returned exactly twice.
	for _, k := range []string{"k1", "k2", "k3"} {
		if seen[k] != 2 {
			t.Fatalf("key %s: expected 2 hits, got %d", k, seen[k])
		}
	}
}

func TestPool_SkipsInactiveKeys(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyRateLimited},
			{Key: "k2", Status: model.KeyDisabled},
			{Key: "k3", Status: model.KeyActive},
		},
	}

	for i := 0; i < 5; i++ {
		k, err := p.NextKey(1)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if k.Key != "k3" {
			t.Fatalf("expected only k3 to be returned, got %s", k.Key)
		}
	}
}

func TestPool_NoActiveKeyReturnsErr(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyDisabled},
			{Key: "k2", Status: model.KeyRateLimited},
		},
	}

	_, err := p.NextKey(1)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestPool_EmptyChannelReturnsErr(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys:    []*keyEntry{},
	}
	_, err := p.NextKey(1)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestPool_UnknownChannelReturnsErr(t *testing.T) {
	p := NewChannelPool()
	_, err := p.NextKey(42)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestPool_MaskedKeyFormat(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "sk-abcdefghijklmnop", Status: model.KeyActive},
		},
	}
	k, _ := p.NextKey(1)
	if k.KeyMasked != "sk-a***mnop" {
		t.Fatalf("expected sk-a***mnop, got %s", k.KeyMasked)
	}
}

func TestPool_UpsertReplacesChannel(t *testing.T) {
	p := NewChannelPool()
	p.UpsertChannel(
		&model.Channel{ID: 7, Name: "x"},
		[]model.Key{{Key: "k1", Status: model.KeyActive}},
	)
	k, err := p.NextKey(7)
	if err != nil || k.Key != "k1" {
		t.Fatalf("Upsert: got key=%s err=%v", k.Key, err)
	}

	p.RemoveChannel(7)
	if _, err := p.NextKey(7); !errors.Is(err, ErrNoKey) {
		t.Fatalf("RemoveChannel: expected ErrNoKey, got %v", err)
	}
}