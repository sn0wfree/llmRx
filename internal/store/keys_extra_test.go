package store

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

func TestDeleteKey(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "k1", KeyMasked: "k1", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "k2", KeyMasked: "k2", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.GetKeys(ch.ID)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if err := s.DeleteKey(keys[0].ID); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	keys, _ = s.GetKeys(ch.ID)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key after delete, got %d", len(keys))
	}
}

func TestWipeKeys(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "secret1", KeyMasked: "m1", Status: model.KeyActive})
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "secret2", KeyMasked: "m2", Status: model.KeyActive})

	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("WipeKeys: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 wiped, got %d", n)
	}

	keys, _ := s.GetKeys(ch.ID)
	for _, k := range keys {
		if k.Key != "" {
			t.Errorf("key %d should have empty Key after wipe", k.ID)
		}
	}
}

func TestWipeKeys_Empty(t *testing.T) {
	s := openTemp(t)
	n, err := s.WipeKeys()
	if err != nil {
		t.Fatalf("WipeKeys empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 wiped, got %d", n)
	}
}

func TestReencryptAllKeys(t *testing.T) {
	s, mgr := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)

	plain1 := "sk-key-aaa"
	plain2 := "sk-key-bbb"
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain1, KeyMasked: "m1", Status: model.KeyActive})
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: plain2, KeyMasked: "m2", Status: model.KeyActive})

	newMgr, err := secrets.FromBytes(bytesRepeat(0xCD, 32))
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.ReencryptAllKeys(mgr, newMgr)
	if err != nil {
		t.Fatalf("ReencryptAllKeys: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 re-encrypted, got %d", n)
	}

	s.SetSecrets(newMgr)
	keys, _ := s.GetKeys(ch.ID)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	found := map[string]bool{}
	for _, k := range keys {
		found[k.Key] = true
	}
	if !found[plain1] || !found[plain2] {
		t.Fatalf("decrypted keys mismatch: %v", found)
	}
}

func TestReencryptAllKeys_NoCiphertext(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "plain", KeyMasked: "m", Status: model.KeyActive})

	oldMgr, _ := secrets.FromBytes(bytesRepeat(0xAB, 32))
	newMgr, _ := secrets.FromBytes(bytesRepeat(0xCD, 32))

	n, err := s.ReencryptAllKeys(oldMgr, newMgr)
	if err != nil {
		t.Fatalf("ReencryptAllKeys no ciphertext: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 re-encrypted, got %d", n)
	}
}

func TestRotateMasterKey(t *testing.T) {
	s, _ := openTempWithSecrets(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-rot-aaa", KeyMasked: "m", Status: model.KeyActive})

	newKey := bytesRepeat(0xFF, 32)
	newHex := hex.EncodeToString(newKey)

	n, err := s.RotateMasterKey(newHex)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 rotated, got %d", n)
	}

	keys, _ := s.GetKeys(ch.ID)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].Key != "sk-rot-aaa" {
		t.Fatalf("decrypted key mismatch: got %q", keys[0].Key)
	}
}

func TestBYOK_NotImplemented(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	_, err := s.CreateBYOKChannel(ctx, &model.BYOKChannel{})
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("CreateBYOKChannel: expected errNotImplemented, got %v", err)
	}
	_, err = s.ListBYOKChannels(ctx)
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("ListBYOKChannels: expected errNotImplemented, got %v", err)
	}
	_, err = s.GetBYOKChannel(ctx, 1)
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("GetBYOKChannel: expected errNotImplemented, got %v", err)
	}
	err = s.DeleteBYOKChannel(ctx, 1)
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("DeleteBYOKChannel: expected errNotImplemented, got %v", err)
	}
}
