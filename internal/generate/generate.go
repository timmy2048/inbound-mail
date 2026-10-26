package generate

import (
	"crypto/rand"
	"encoding/base64"
	"math/big"
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

// ValidLocalPart reports whether s is a generated local part.
func ValidLocalPart(s string) bool {
	if len(s) != LocalPartLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
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
