package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSQLite_BYOKStubsReturnNotImplemented(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	if _, err := s.CreateBYOKChannel(ctx, nil); !errors.Is(err, errNotImplemented) {
		t.Errorf("CreateBYOKChannel: expected errNotImplemented, got %v", err)
	}
	if _, err := s.ListBYOKChannels(ctx); !errors.Is(err, errNotImplemented) {
		t.Errorf("ListBYOKChannels: expected errNotImplemented, got %v", err)
	}
	if _, err := s.GetBYOKChannel(ctx, 1); !errors.Is(err, errNotImplemented) {
		t.Errorf("GetBYOKChannel: expected errNotImplemented, got %v", err)
	}
	if err := s.DeleteBYOKChannel(ctx, 1); !errors.Is(err, errNotImplemented) {
		t.Errorf("DeleteBYOKChannel: expected errNotImplemented, got %v", err)
	}
}
