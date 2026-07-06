package auth

import "golang.org/x/crypto/bcrypt"

// bcryptHashForTest produces a bcrypt hash for compatibility tests.
// Uses cost=4 to keep the test suite fast.
func bcryptHashForTest(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 4)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
