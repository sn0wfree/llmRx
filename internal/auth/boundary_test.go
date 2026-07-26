package auth

import (
	"testing"
)

// TestVerifyArgon2_MalformedInputs tests verifyArgon2 with various
// malformed argon2id strings to cover all error branches.
func TestVerifyArgon2_MalformedInputs(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		pw     string
		want   bool
	}{
		// --- len(parts) != 6 ---
		{"too many parts", "$argon2id$v=19$m=65536,t=3,p=2$salt$key$extra", "pw", false},
		{"too few parts (4)", "$argon2id$v=19$m=65536,t=3,p=2", "pw", false},
		{"too few parts (5)", "$argon2id$v=19$m=65536,t=3,p=2$", "pw", false},

		// --- parts[1] != "argon2id" ---
		{"wrong algorithm argon2d", "$argon2d$v=19$m=65536,t=3,p=2$", "pw", false},
		{"wrong algorithm argon2i", "$argon2i$v=19$m=65536,t=3,p=2$", "pw", false},

		// --- version parse error ---
		{"uppercase V", "$argon2id$V=19$m=65536,t=3,p=2$", "pw", false},
		{"non-numeric version", "$argon2id$v=abc$m=65536,t=3,p=2$", "pw", false},

		// --- version mismatch ---
		{"wrong version v=13", "$argon2id$v=13$m=65536,t=3,p=2$", "pw", false},
		{"wrong version v=99", "$argon2id$v=99$m=65536,t=3,p=2$", "pw", false},

		// --- params parse error ---
		{"non-numeric memory", "$argon2id$v=19$m=abc,t=3,p=2$", "pw", false},
		{"non-numeric time", "$argon2id$v=19$m=65536,t=abc,p=2$", "pw", false},
		{"non-numeric threads", "$argon2id$v=19$m=65536,t=3,p=abc$", "pw", false},

		// --- zero params ---
		{"zero memory", "$argon2id$v=19$m=0,t=3,p=2$", "pw", false},
		{"zero iterations", "$argon2id$v=19$m=65536,t=0,p=2$", "pw", false},
		{"zero parallelism", "$argon2id$v=19$m=65536,t=3,p=0$", "pw", false},

		// --- base64 salt decode error ---
		{"bad base64 salt", "$argon2id$v=19$m=65536,t=3,p=2$!!!invalid!!!$BB", "pw", false},

		// --- base64 key decode error ---
		{"bad base64 key", "$argon2id$v=19$m=65536,t=3,p=2$AA$!!!invalid!!!", "pw", false},

		// --- password mismatch (valid format, wrong password) ---
		{"valid format wrong pw", hashForTest("correct"), "wrong", false},
		{"valid format correct pw", hashForTest("correct"), "correct", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifyArgon2(tt.stored, tt.pw)
			if got != tt.want {
				t.Errorf("verifyArgon2(%q, %q) = %v, want %v", tt.stored, tt.pw, got, tt.want)
			}
		})
	}
}

// hashForTest creates a valid argon2id hash for testing.
func hashForTest(pw string) string {
	h, _ := Hash(pw)
	return h
}

// TestIsLegacyHash_MalformedInputs tests isLegacyHash with various
// malformed input strings to cover all error branches.
func TestIsLegacyHash_MalformedInputs(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		want   bool
	}{
		{"colon at position 0", ":hello", false},
		{"colon at last position", "abcdef0123456789:", false},
		{"salt too short (4 chars)", "abcd:hello", false},
		{"salt too long (33 chars)", "aabbccddeeff0011223344556677889900:hello", false},
		{"uppercase hex", "AABBCCDDEEFF00112233445566778899:hello", false},
		{"non-hex char G", "aabbccddeeff00112233445566778899G:hello", false},
		{"no colon", "no-colon-here", false},
		{"empty string", "", false},
		{"valid legacy hash", "aabbccddeeff00112233445566778899:password", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLegacyHash(tt.stored)
			if got != tt.want {
				t.Errorf("isLegacyHash(%q) = %v, want %v", tt.stored, got, tt.want)
			}
		})
	}
}

// TestVerify_LegacyHashPath tests Verify with a legacy-format hash
// to cover the isLegacyHash true path.
func TestVerify_LegacyHashPath(t *testing.T) {
	// Create a legacy hash: 32 hex chars + ":" + password
	legacy := "aabbccddeeff00112233445566778899:mypassword"
	r := Verify(legacy, "mypassword")
	if !r.OK {
		t.Errorf("Verify with legacy hash should succeed")
	}
	r = Verify(legacy, "wrongpassword")
	if r.OK {
		t.Errorf("Verify with legacy hash wrong password should fail")
	}
}

// TestVerify_EmptyInputs tests Verify with empty stored/password.
func TestVerify_EmptyInputs(t *testing.T) {
	r := Verify("", "test")
	if r.OK {
		t.Errorf("Verify with empty stored should fail")
	}
	hash, _ := Hash("test")
	r = Verify(hash, "")
	if r.OK {
		t.Errorf("Verify with empty password should fail")
	}
}
