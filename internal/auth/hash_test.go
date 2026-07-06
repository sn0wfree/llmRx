package auth

import "testing"

func TestArgon2HashAndVerify(t *testing.T) {
	h, err := Hash("s3cret-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == "" {
		t.Fatal("empty hash")
	}
	if !IsArgon2(h) {
		t.Fatalf("expected argon2id prefix, got %q", h)
	}
	if got := Verify(h, "s3cret-pw"); !got.OK || got.NeedsUpgrade {
		t.Fatalf("verify ok=%v upgrade=%v", got.OK, got.NeedsUpgrade)
	}
	if got := Verify(h, "wrong"); got.OK {
		t.Fatal("expected mismatch")
	}
}

func TestHashEmpty(t *testing.T) {
	if _, err := Hash(""); err != ErrEmptyPassword {
		t.Fatalf("expected ErrEmptyPassword, got %v", err)
	}
	if got := Verify("ignored", ""); got.OK {
		t.Fatal("empty pw must not verify")
	}
	if got := Verify("", "x"); got.OK {
		t.Fatal("empty stored must not verify")
	}
}

func TestBcryptCompat(t *testing.T) {
	bc, err := bcryptHashForTest("hello")
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if !IsBcrypt(bc) {
		t.Fatalf("expected bcrypt, got %q", bc)
	}
	if got := Verify(bc, "hello"); !got.OK || !got.NeedsUpgrade {
		t.Fatalf("bcrypt verify ok=%v upgrade=%v", got.OK, got.NeedsUpgrade)
	}
	if got := Verify(bc, "wrong"); got.OK {
		t.Fatal("bcrypt mismatch must fail")
	}
}

func TestLegacyCompat(t *testing.T) {
	legacy := "00112233445566778899aabbccddeeff:hello"
	if !IsLegacy(legacy) {
		t.Fatal("should detect legacy")
	}
	if got := Verify(legacy, "hello"); !got.OK || !got.NeedsUpgrade {
		t.Fatalf("legacy verify ok=%v upgrade=%v", got.OK, got.NeedsUpgrade)
	}
	if got := Verify(legacy, "world"); got.OK {
		t.Fatal("legacy mismatch must fail")
	}
}

func TestVerifyCorruptedArgon2(t *testing.T) {
	h, _ := Hash("hello")
	// Truncate the hash part.
	bad := h[:len(h)-4] + "AAAA"
	if got := Verify(bad, "hello"); got.OK {
		t.Fatal("corrupted argon2 should not verify")
	}
}

func TestVerifyUnknownFormat(t *testing.T) {
	if got := Verify("not-a-hash", "hello"); got.OK {
		t.Fatal("unknown format must not verify")
	}
}

func TestHashesAreUnique(t *testing.T) {
	a, _ := Hash("same")
	b, _ := Hash("same")
	if a == b {
		t.Fatal("two hashes of the same password should differ (random salt)")
	}
}
