// Package auth provides password hashing and verification.
//
// New hashes (P7+) use Argon2id with parameters tuned for an
// interactive login on a modest server (~50ms on 1 core, 64 MiB).
// The encoded string is the standard PHC format:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
//
// Verify also recognises older formats for transparent upgrade:
//
//   - bcrypt hashes ("$2a$...", "$2b$...", "$2y$...") from P6 —
//     verified and flagged for re-hash to argon2id.
//   - Legacy "<hex_salt>:<plaintext>" strings from pre-P6 — verified
//     and flagged for re-hash.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // 64 MiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// ErrEmptyPassword is returned when the supplied password is empty.
var ErrEmptyPassword = errors.New("empty password")

// Hash returns an Argon2id PHC-format hash of pw.
func Hash(pw string) (string, error) {
	if pw == "" {
		return "", ErrEmptyPassword
	}
	salt := randomBytes(argonSaltLen)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return encodeArgon2(salt, key), nil
}

// VerifyResult is the outcome of a password check.
type VerifyResult struct {
	OK           bool
	NeedsUpgrade bool
}

// Verify checks pw against the stored hash. When NeedsUpgrade is
// true the caller should re-hash with Hash and persist the new value
// (covers both legacy pre-P6 and P6 bcrypt formats).
func Verify(stored, pw string) VerifyResult {
	if stored == "" || pw == "" {
		return VerifyResult{}
	}
	if isLegacyHash(stored) {
		if verifyLegacy(stored, pw) {
			return VerifyResult{OK: true, NeedsUpgrade: true}
		}
		return VerifyResult{}
	}
	if isBcrypt(stored) {
		if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(pw)); err == nil {
			return VerifyResult{OK: true, NeedsUpgrade: true}
		}
		return VerifyResult{}
	}
	if verifyArgon2(stored, pw) {
		return VerifyResult{OK: true}
	}
	return VerifyResult{}
}

// IsLegacy reports whether stored uses the pre-P6 plaintext format.
func IsLegacy(stored string) bool { return isLegacyHash(stored) }

// IsBcrypt reports whether stored uses P6 bcrypt format.
func IsBcrypt(stored string) bool { return isBcrypt(stored) }

// IsArgon2 reports whether stored uses the P7+ argon2id format.
func IsArgon2(stored string) bool { return strings.HasPrefix(stored, "$argon2id$") }

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("auth: crypto/rand failed: %v", err))
	}
	return b
}

func encodeArgon2(salt, key []byte) string {
	enc := base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s", argon2.Version, argonMemory, argonTime, argonThreads, enc)
}

func verifyArgon2(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", "salt", "key"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}
	if version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m == 0 || t == 0 || p == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func isBcrypt(stored string) bool {
	return strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$") || strings.HasPrefix(stored, "$2y$")
}

func isLegacyHash(stored string) bool {
	idx := strings.IndexByte(stored, ':')
	if idx <= 0 || idx == len(stored)-1 {
		return false
	}
	salt := stored[:idx]
	if len(salt) != 32 {
		return false
	}
	for _, c := range salt {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func verifyLegacy(stored, pw string) bool {
	idx := strings.IndexByte(stored, ':')
	return stored[idx+1:] == pw
}
