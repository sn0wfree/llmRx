package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestSQLite_BYOKCRUD(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Empty inputs should be rejected.
	if _, err := s.CreateBYOKChannel(ctx, &model.BYOKChannel{}); err == nil {
		t.Error("CreateBYOKChannel with empty fields should fail")
	}

	ch := &model.BYOKChannel{
		Provider:      "openai",
		KeyCiphertext: "ciphertext-blob",
		KeyMasked:     "sk-***abcd",
		OwnerIP:       "127.0.0.1",
		OwnerEmail:    "user@example.com",
		Status:        1,
	}
	id, err := s.CreateBYOKChannel(ctx, ch)
	if err != nil {
		t.Fatalf("CreateBYOKChannel: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateBYOKChannel returned id=0")
	}

	got, err := s.GetBYOKChannel(ctx, id)
	if err != nil {
		t.Fatalf("GetBYOKChannel: %v", err)
	}
	if got.Provider != "openai" || got.OwnerIP != "127.0.0.1" || got.KeyMasked != "sk-***abcd" {
		t.Errorf("GetBYOKChannel mismatch: %+v", got)
	}

	byIP, err := s.GetBYOKChannelByIP(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("GetBYOKChannelByIP: %v", err)
	}
	if byIP.ID != id {
		t.Errorf("GetBYOKChannelByIP id: got %d, want %d", byIP.ID, id)
	}

	if err := s.TouchBYOKChannel(ctx, id); err != nil {
		t.Fatalf("TouchBYOKChannel: %v", err)
	}
	after, _ := s.GetBYOKChannel(ctx, id)
	if after.UseCount != 1 {
		t.Errorf("UseCount: got %d, want 1", after.UseCount)
	}

	list, err := s.ListBYOKChannels(ctx)
	if err != nil {
		t.Fatalf("ListBYOKChannels: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List len: got %d, want 1", len(list))
	}

	if err := s.DeleteBYOKChannel(ctx, id); err != nil {
		t.Fatalf("DeleteBYOKChannel: %v", err)
	}
	if _, err := s.GetBYOKChannel(ctx, id); err != ErrNotFound {
		t.Errorf("GetBYOKChannel after delete: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteBYOKChannel(ctx, 99999); err != ErrNotFound {
		t.Errorf("DeleteBYOKChannel nonexistent: got %v, want ErrNotFound", err)
	}
}