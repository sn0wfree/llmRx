package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RandomTokenHex returns n bytes of crypto/rand entropy hex-encoded
// (2n characters). The single generation point for every token in the
// codebase — session tokens, API keys and client identifiers all share
// it so entropy size and rand error handling cannot drift apart.
func RandomTokenHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: crypto/rand failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewSessionToken returns the standard session token: 32 bytes
// (256-bit) of entropy, hex-encoded (64 characters). Sessions live in
// the DB and are revocable, so the format change from the older
// 24-byte variants does not affect existing sessions.
func NewSessionToken() (string, error) {
	return RandomTokenHex(32)
}

// NewAPIKey returns an "sk-" prefixed API key with the same entropy as
// NewSessionToken ("sk-" + 64 hex characters). The prefix is the
// OpenAI-style convention, not an enforcement mechanism.
func NewAPIKey() (string, error) {
	t, err := RandomTokenHex(32)
	if err != nil {
		return "", err
	}
	return "sk-" + t, nil
}
