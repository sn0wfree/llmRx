package ratelimit

import (
	"testing"
	"time"
)

func TestMemoryBackend_AccountRequestWindow(t *testing.T) {
	b := NewMemoryBackend()
	now := time.Now()

	// No active window: no-op.
	b.AccountRequestWindow(1, now)
	if n := b.TrackedKeys(); n != 0 {
		t.Fatalf("TrackedKeys = %d, want 0 (no window yet)", n)
	}

	// After one allow, account request bumps RPM.
	if ok, _ := b.AllowWindow(1, 2, 0, 10, now); !ok {
		t.Fatal("allow 1")
	}
	b.AccountRequestWindow(1, now)
	// 2 requests used (allow + account), third rejected.
	if ok, reason := b.AllowWindow(1, 2, 0, 10, now); ok || reason != "rpm exceeded" {
		t.Fatalf("ok=%v reason=%q, want rpm exceeded", ok, reason)
	}
}

func TestMemoryBackend_AccountWindowNoKey(t *testing.T) {
	b := NewMemoryBackend()
	// No window for the key: silent no-op, no state created.
	b.AccountWindow(99, 50, time.Now())
	if n := b.TrackedKeys(); n != 0 {
		t.Fatalf("TrackedKeys = %d, want 0", n)
	}
}

func TestMemoryBackend_AccountWindowZeroTokens(t *testing.T) {
	b := NewMemoryBackend()
	b.AllowWindow(1, 100, 0, 5, time.Now())
	b.AccountWindow(1, 0, time.Now()) // no-op
	if ok, _ := b.AllowWindow(1, 100, 0, 5, time.Now()); !ok {
		t.Fatal("zero-token account must not consume TPM")
	}
}

func TestMemoryBackend_SetNow(t *testing.T) {
	b := NewMemoryBackend()
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	b.SetNow(func() time.Time { return base })

	if ok, _ := b.AllowWindow(5, 1, 0, 0, base); !ok {
		t.Fatal("allow")
	}
	// 90s later: exact sliding window has expired the old entry.
	if ok, _ := b.AllowWindow(5, 1, 0, 0, base.Add(90*time.Second)); !ok {
		t.Fatal("entry should expire after 60s")
	}
}

func TestMemoryBackend_TPMOnly(t *testing.T) {
	b := NewMemoryBackend()
	now := time.Now()
	// rpm=0 means RPM is unlimited; only TPM gates.
	if ok, _ := b.AllowWindow(1, 0, 100, 60, now); !ok {
		t.Fatal("allow 60 tokens")
	}
	if ok, reason := b.AllowWindow(1, 0, 100, 50, now); ok || reason != "tpm exceeded" {
		t.Fatalf("ok=%v reason=%q, want tpm exceeded", ok, reason)
	}
	// RPM with tpm=0: requests only.
	if ok, _ := b.AllowWindow(2, 1, 0, 0, now); !ok {
		t.Fatal("rpm-only allow")
	}
	if ok, reason := b.AllowWindow(2, 1, 0, 0, now); ok || reason != "rpm exceeded" {
		t.Fatalf("ok=%v reason=%q, want rpm exceeded", ok, reason)
	}
}

func TestMemoryBackend_Reset(t *testing.T) {
	b := NewMemoryBackend()
	now := time.Now()
	b.AllowWindow(1, 1, 0, 0, now)
	b.AllowWindow(2, 1, 0, 0, now)
	if n := b.TrackedKeys(); n != 2 {
		t.Fatalf("TrackedKeys = %d, want 2", n)
	}
	b.Reset()
	if n := b.TrackedKeys(); n != 0 {
		t.Fatalf("TrackedKeys after Reset = %d, want 0", n)
	}
	if ok, _ := b.AllowWindow(1, 1, 0, 0, now); !ok {
		t.Fatal("allow after reset")
	}
}

func TestMemoryBackend_TokenAccounting(t *testing.T) {
	b := NewMemoryBackend()
	now := time.Now()
	// AccountWindow adds to the most recent entry's tokens.
	if ok, _ := b.AllowWindow(1, 100, 100, 10, now); !ok {
		t.Fatal("allow")
	}
	b.AccountWindow(1, 40, now)
	// projected = 10 + 40 = 50; adding 60 would exceed tpm=100.
	if ok, reason := b.AllowWindow(1, 100, 100, 60, now); ok || reason != "tpm exceeded" {
		t.Fatalf("ok=%v reason=%q, want tpm exceeded (accounted tokens count)", ok, reason)
	}
}
