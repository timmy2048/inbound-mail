package addr

import (
	"errors"
	"strings"
)

// ErrDomain means the domain is not an ASCII hostname we can publish in DNS.
var ErrDomain = errors.New("invalid domain")

// NormalizeDomain lowercases and checks an ASCII mail domain such as richrich.cc.cd.
func NormalizeDomain(in string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return "", ErrDomain
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return "", ErrDomain
	}
	for _, lab := range labels {
		if len(lab) < 1 || len(lab) > 63 {
			return "", ErrDomain
		}
		if lab[0] == '-' || lab[len(lab)-1] == '-' {
			return "", ErrDomain
		}
		for i := 0; i < len(lab); i++ {
			c := lab[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", ErrDomain
			}
		}
	}
	return s, nil
}

// NormalizeEmail lowercases a bare SMTP address and checks the domain.
// Local parts are kept except for case, so generated lowercase addresses match
// even when a sender capitalizes them.
func NormalizeEmail(in string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	s = strings.TrimSpace(s)
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || strings.Contains(local, "@") || len(local) > 64 {
		return "", errors.New("invalid address")
	}
	if strings.ContainsAny(local, " \t\r\n") {
		return "", errors.New("invalid address")
	}
	d, err := NormalizeDomain(domain)
	if err != nil {
		return "", err
	}
	return local + "@" + d, nil
}
