package auth

import "testing"

func TestHash_AndVerify(t *testing.T) {
	hash, err := Hash("mypassword")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash")
	}
	if hash == "mypassword" {
		t.Fatal("hash should not equal plaintext")
	}
	r := Verify(hash, "mypassword")
	if !r.OK {
		t.Errorf("Verify should succeed with correct password")
	}
	r = Verify(hash, "wrongpassword")
	if r.OK {
		t.Errorf("Verify should fail with wrong password")
	}
}

func TestHash_EmptyPassword(t *testing.T) {
	_, err := Hash("")
	if err == nil {
		t.Errorf("Hash empty should error")
	}
}

func TestIsLegacy_NotLegacy(t *testing.T) {
	hash, _ := Hash("test")
	if IsLegacy(hash) {
		t.Errorf("argon2id hash should not be legacy")
	}
}

func TestIsBcrypt_NotBcrypt(t *testing.T) {
	hash, _ := Hash("test")
	if IsBcrypt(hash) {
		t.Errorf("argon2id hash should not be bcrypt")
	}
}

func TestVerify_BcryptHash(t *testing.T) {
	bcryptHash := "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	r := Verify(bcryptHash, "wrongpassword")
	if r.OK {
		t.Errorf("verify with wrong password should fail")
	}
}

func TestIsBcrypt_ValidBcrypt(t *testing.T) {
	bcryptHash := "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	if !IsBcrypt(bcryptHash) {
		t.Errorf("should detect bcrypt hash")
	}
}

func TestVerify_InvalidHash(t *testing.T) {
	r := Verify("invalid-hash-format", "test")
	if r.OK {
		t.Errorf("verify invalid hash should fail")
	}
}

func TestVerify_EmptyHash(t *testing.T) {
	r := Verify("", "test")
	if r.OK {
		t.Errorf("verify empty hash should fail")
	}
}
