package secrets

import (
	"strings"
	"testing"
)

func TestEncrypt_LongPlaintext(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	plain := make([]byte, 64*1024)
	for i := range plain {
		plain[i] = byte(i % 256)
	}
	ct, err := m.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := m.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if len(pt) != len(plain) {
		t.Fatalf("len mismatch: got %d, want %d", len(pt), len(plain))
	}
	for i := range plain {
		if pt[i] != plain[i] {
			t.Fatalf("byte %d mismatch", i)
		}
	}
}

func TestFromEnv_NoEnvVar(t *testing.T) {
	t.Setenv("LLMRX_TEST_NONEXISTENT", "")
	_, err := FromEnv("LLMRX_TEST_NONEXISTENT")
	if err == nil {
		t.Error("expected error for unset/empty env var")
	}
}

func TestFromEnv_BadHex(t *testing.T) {
	t.Setenv("LLMRX_TEST_BADHEX", "not-hex-data")
	_, err := FromEnv("LLMRX_TEST_BADHEX")
	if err == nil {
		t.Error("expected error for bad hex")
	}
}

func TestFromEnv_WrongLength(t *testing.T) {
	// 16 bytes (32 hex chars) instead of 32 bytes.
	t.Setenv("LLMRX_TEST_SHORT", "00112233445566778899aabbccddeeff")
	_, err := FromEnv("LLMRX_TEST_SHORT")
	if err == nil {
		t.Error("expected error for short key")
	}
}

func TestFromEnv_Valid(t *testing.T) {
	t.Setenv("LLMRX_TEST_VALID", "0000000000000000000000000000000000000000000000000000000000000001")
	m, err := FromEnv("LLMRX_TEST_VALID")
	if err != nil {
		t.Fatal(err)
	}
	if m.EnvName() != "LLMRX_TEST_VALID" {
		t.Errorf("EnvName = %q, want LLMRX_TEST_VALID", m.EnvName())
	}
}

func TestManager_EnvName_EmptyForFromBytes(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	if m.EnvName() != "" {
		t.Errorf("EnvName = %q, want empty for FromBytes", m.EnvName())
	}
}

func TestFromHexKey_Valid(t *testing.T) {
	m, err := FromHexKey(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("nil manager")
	}
}

func TestEncrypt_DecryptRoundTrip_BinaryData(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	plain := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}
	ct, err := m.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := m.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plain {
		if pt[i] != plain[i] {
			t.Errorf("byte %d: got %x, want %x", i, pt[i], plain[i])
		}
	}
}

func TestEncrypt_DecryptRoundTrip_PlainASCII(t *testing.T) {
	m, _ := FromBytes(make([]byte, 32))
	plain := []byte("hello world")
	ct, err := m.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := m.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hello world" {
		t.Errorf("got %q, want hello world", pt)
	}
}
