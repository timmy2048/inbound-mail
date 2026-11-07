package generate

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
)

const (
	Alphabet        = "abcdefghijklmnopqrstuvwxyz0123456789"
	LocalPartLength = 10
)

// RandomLocalPart returns a fixed-length mailbox local part.
// The alphabet is lowercase a-z and digits 0-9, chosen without modulo bias.
func RandomLocalPart() (string, error) {
	return randomFrom(Alphabet, LocalPartLength)
}

// RandomToken returns an unguessable URL token.
func RandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ErrInvalidPrefix means a custom local part failed the prefix rules.
var ErrInvalidPrefix = errors.New("invalid prefix")

// ValidLocalPart reports whether s is a random 10-character local part.
func ValidLocalPart(s string) bool {
	if len(s) != LocalPartLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isAlnum(s[i]) {
			return false
		}
	}
	return true
}

// NormalizePrefix lowercases a custom mailbox prefix.
// An empty or whitespace-only value returns "" and a nil error, which means
// the caller should keep generating random local parts.
// A non-empty prefix must be 1–64 characters of a-z, 0-9, '.', '_' and '-',
// must not start or end with punctuation, and must not contain "..".
func NormalizePrefix(in string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	if s == "" {
		return "", nil
	}
	if len(s) > 64 || !isAlnum(s[0]) || !isAlnum(s[len(s)-1]) || strings.Contains(s, "..") {
		return "", ErrInvalidPrefix
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAlnum(c) || c == '.' || c == '_' || c == '-' {
			continue
		}
		return "", ErrInvalidPrefix
	}
	return s, nil
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func randomFrom(alphabet string, n int) (string, error) {
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}
