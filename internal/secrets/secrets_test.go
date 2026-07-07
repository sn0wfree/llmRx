package secrets

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return hex.EncodeToString(b)
}

func TestFromBytes_ValidKey(t *testing.T) {
	b := make([]byte, 32)
	m, err := FromBytes(b)
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	if m == nil || m.gcm == nil {
		t.Fatal("manager is nil")
	}
}

func TestFromBytes_WrongLength(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		make([]byte, 16),
		make([]byte, 31),
		make([]byte, 33),
		make([]byte, 64),
	}
	for _, b := range cases {
		if _, err := FromBytes(b); err == nil {
			t.Errorf("FromBytes(len=%d) should error", len(b))
		}
	}
}

func TestFromHexKey_Roundtrip(t *testing.T) {
	k := testKey(t)
	m, err := FromHexKey(k)
	if err != nil {
		t.Fatalf("FromHexKey: %v", err)
	}
	pt := []byte("sk-abc123-XYZ-secret")
	ct, err := m.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := m.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
	}
}

func TestFromHexKey_NotHex(t *testing.T) {
	if _, err := FromHexKey("not-hex-data!@#$%^&*()"); err == nil {
		t.Fatal("expected error for non-hex input")
	}
}

func TestFromHexKey_WrongLength(t *testing.T) {
	short := strings.Repeat("ab", 16) // 32 hex chars = 16 bytes
	if _, err := FromHexKey(short); err == nil {
		t.Fatal("expected error for 16-byte key (AES-128 not supported)")
	}
}

func TestFromEnv_OK(t *testing.T) {
	k := testKey(t)
	t.Setenv("LLMRX_KEY_MASTER", k)
	m, err := FromEnv("")
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if m.EnvName() != "LLMRX_KEY_MASTER" {
		t.Errorf("EnvName=%q want LLMRX_KEY_MASTER", m.EnvName())
	}
}

func TestFromEnv_CustomName(t *testing.T) {
	k := testKey(t)
	t.Setenv("MY_TEST_KEY", k)
	m, err := FromEnv("MY_TEST_KEY")
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if m.EnvName() != "MY_TEST_KEY" {
		t.Errorf("EnvName=%q want MY_TEST_KEY", m.EnvName())
	}
}

func TestFromEnv_Missing(t *testing.T) {
	// Make sure neither default nor custom var is set.
	t.Setenv("LLMRX_KEY_MASTER", "")
	os.Unsetenv("LLMRX_KEY_MASTER")
	if _, err := FromEnv(""); err == nil {
		t.Fatal("expected error when env unset")
	}
	if _, err := FromEnv("SOMETHING_NOT_SET"); err == nil {
		t.Fatal("expected error when named env unset")
	}
}

func TestEncrypt_NonceUniqueness(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	pt := []byte("the same plaintext")
	a, _ := m.Encrypt(pt)
	b, _ := m.Encrypt(pt)
	if a == b {
		t.Fatal("two encryptions of identical plaintext must differ (nonce uniqueness)")
	}
	// Both must still decrypt to the same plaintext.
	pa, _ := m.Decrypt(a)
	pb, _ := m.Decrypt(b)
	if !bytes.Equal(pa, pt) || !bytes.Equal(pb, pt) {
		t.Fatal("decrypted values do not match original plaintext")
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	if _, err := m.Encrypt(nil); err == nil {
		t.Fatal("expected error on empty plaintext")
	}
	if _, err := m.Encrypt([]byte{}); err == nil {
		t.Fatal("expected error on empty plaintext")
	}
}

func TestDecrypt_EmptyCiphertext(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	if _, err := m.Decrypt(""); err == nil {
		t.Fatal("expected error on empty ciphertext")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	m1, _ := FromBytes(make([]byte, 32))
	m2, _ := FromBytes(bytes.Repeat([]byte{0xff}, 32))
	ct, err := m1.Encrypt([]byte("secret payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Wrong key must fail.
	if _, err := m2.Decrypt(ct); err == nil {
		t.Fatal("decryption with wrong key should fail")
	}
	// Bit-flip in the base64 must fail.
	tampered := []byte(ct)
	tampered[len(tampered)-3] ^= 0x01
	if _, err := m1.Decrypt(string(tampered)); err == nil {
		t.Fatal("decryption of tampered ciphertext should fail")
	}
}

func TestDecrypt_TooShort(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	// 4 bytes < NonceSize (12) — base64 of "AAAA"
	if _, err := m.Decrypt("AAAA"); err == nil {
		t.Fatal("expected error on short ciphertext")
	}
}

func TestDecrypt_GarbageBase64(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	if _, err := m.Decrypt("!!!not-base64!!!"); err == nil {
		t.Fatal("expected error on invalid base64")
	}
}

func TestMask(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"sk-abcdefghijklmnop", "sk-a***mnop"},
		{"short", "short"},
		{"", ""},
		{"12345678", "12345678"},   // exactly 8 chars → no mask
		{"123456789", "1234***6789"},
	}
	for _, c := range cases {
		got := Mask(c.in)
		if got != c.want {
			t.Errorf("Mask(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewCipher_DifferentKeysDifferentCiphertext(t *testing.T) {
	m1, _ := FromBytes(make([]byte, 32))
	m2, _ := FromBytes(bytes.Repeat([]byte{0xaa}, 32))
	ct1, _ := m1.Encrypt([]byte("same"))
	ct2, _ := m2.Encrypt([]byte("same"))
	if ct1 == ct2 {
		t.Fatal("different master keys must produce different ciphertext")
	}
}