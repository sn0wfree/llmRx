// Package secrets provides AES-256-GCM encryption for at-rest secrets
// (channel API keys, etc.). The master key is loaded from an env var
// (default: LLMRX_KEY_MASTER) and must be a 32-byte (64 hex char) value.
//
// Generate one with:
//
//	openssl rand -hex 32
//
// Lost master keys cannot be recovered; back it up somewhere safe
// (password manager, sealed envelope, KMS, etc.).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// DefaultEnvVar is the env var the manager looks for when none is
// explicitly configured.
const DefaultEnvVar = "LLMRX_KEY_MASTER"

// keyLenBytes is the required master key length. AES-256 takes a
// 32-byte key.
const keyLenBytes = 32

// Manager wraps an AES-GCM AEAD. It is safe for concurrent use; the
// underlying cipher.NewGCM returns a value that holds no per-call
// state.
type Manager struct {
	gcm     cipher.AEAD
	envName string // remembered for FromEnv-style diagnostics
}

// FromEnv loads a master key from the named env var. If name is empty,
// DefaultEnvVar is used. Returns a clear error if the env is unset or
// the key is the wrong length so operators see the problem at startup
// rather than at the first request.
func FromEnv(name string) (*Manager, error) {
	if name == "" {
		name = DefaultEnvVar
	}
	hexKey := os.Getenv(name)
	if hexKey == "" {
		return nil, fmt.Errorf("secrets: %s env var not set (generate with `openssl rand -hex 32`)", name)
	}
	m, err := FromHexKey(hexKey)
	if err != nil {
		return nil, fmt.Errorf("secrets: %s invalid: %w", name, err)
	}
	m.envName = name
	return m, nil
}

// FromHexKey decodes a 64-char hex string and returns a Manager.
func FromHexKey(s string) (*Manager, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not hex: %w", err)
	}
	return FromBytes(b)
}

// FromBytes accepts exactly 32 bytes and returns a Manager.
func FromBytes(b []byte) (*Manager, error) {
	if len(b) != keyLenBytes {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", keyLenBytes, len(b))
	}
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Manager{gcm: gcm}, nil
}

// EnvName returns the env var this manager was loaded from (empty
// when constructed directly with FromBytes / FromHexKey).
func (m *Manager) EnvName() string { return m.envName }

// Encrypt seals plaintext under the master key and returns a base64
// string of (nonce || ciphertext || tag). Each call uses a fresh
// random nonce — callers must never reuse nonces for the same
// message under AES-GCM.
func (m *Manager) Encrypt(plaintext []byte) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("plaintext is empty")
	}
	nonce := make([]byte, m.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	sealed := m.gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt inverts Encrypt. Returns an error if the input is
// malformed or the ciphertext has been tampered with.
func (m *Manager) Decrypt(ciphertextB64 string) ([]byte, error) {
	if ciphertextB64 == "" {
		return nil, errors.New("ciphertext is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("b64: %w", err)
	}
	ns := m.gcm.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := m.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return pt, nil
}

// Mask returns a UI-friendly representation of a secret, showing
// only the first and last 4 characters (or the whole string when it
// is 8 characters or fewer). This mirrors the operator expectations
// from P6+: never display the full key in the admin UI.
func Mask(s string) string {
	if len(s) > 8 {
		return s[:4] + "***" + s[len(s)-4:]
	}
	return s
}