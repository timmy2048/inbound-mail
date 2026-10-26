package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Check compares a presented password with ADMIN_PASSWORD.
// Values that look like bcrypt hashes are checked with bcrypt; anything else
// is compared as plaintext without leaking the length through a short-circuit
// on the raw bytes.
func Check(configured, given string) bool {
	if configured == "" {
		return false
	}
	if isBcrypt(configured) {
		return bcrypt.CompareHashAndPassword([]byte(configured), []byte(given)) == nil
	}
	a := sha256.Sum256([]byte(configured))
	b := sha256.Sum256([]byte(given))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// Hash returns a bcrypt hash suitable for ADMIN_PASSWORD.
func Hash(password string) (string, error) {
	sum, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(sum), nil
}

func isBcrypt(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}
