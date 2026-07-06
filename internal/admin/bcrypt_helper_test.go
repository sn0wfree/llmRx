package admin_test

import "golang.org/x/crypto/bcrypt"

func authBcrypt(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 4)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
