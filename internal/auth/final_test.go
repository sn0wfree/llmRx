package auth

import "testing"

func TestVerify_LegacyHash(t *testing.T) {
	r := Verify("plaintext-not-a-hash", "test")
	if r.OK {
		t.Errorf("verify with non-hash should fail")
	}
}

func TestIsLegacy_Plaintext(t *testing.T) {
	if IsLegacy("plaintext") {
		t.Errorf("plaintext should not be detected as legacy")
	}
}

func TestIsLegacy_ActualLegacy(t *testing.T) {
	legacy := "aabbccddeeff00112233445566778899:somehashdata"
	if !IsLegacy(legacy) {
		t.Errorf("32-hex-salt:hash format should be detected as legacy")
	}
}

func TestVerify_EmptyStored(t *testing.T) {
	r := Verify("", "test")
	if r.OK {
		t.Errorf("verify empty stored should fail")
	}
}

func TestVerify_EmptyPassword(t *testing.T) {
	hash, _ := Hash("testpass")
	r := Verify(hash, "")
	if r.OK {
		t.Errorf("verify with empty password should fail")
	}
}

func TestHash_LongPassword(t *testing.T) {
	longPw := string(make([]byte, 1000))
	for i := range longPw {
		longPw = longPw[:i] + "a" + longPw[i+1:]
	}
	hash, err := Hash(longPw)
	if err != nil {
		t.Fatalf("Hash long: %v", err)
	}
	if !Verify(hash, longPw).OK {
		t.Errorf("verify long password should succeed")
	}
}

func TestHash_SpecialChars(t *testing.T) {
	pw := "p@ssw0rd!#$%^&*()"
	hash, _ := Hash(pw)
	if !Verify(hash, pw).OK {
		t.Errorf("verify with special chars should succeed")
	}
}
