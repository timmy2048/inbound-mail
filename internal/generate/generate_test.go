package generate

import (
	"errors"
	"strings"
	"testing"
)

func TestLocalPartFormat(t *testing.T) {
	seen := map[string]bool{}
	var letters, digits int
	for i := 0; i < 200; i++ {
		part, err := RandomLocalPart()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidLocalPart(part) {
			t.Fatalf("local part %q is not 10 chars from a-z0-9", part)
		}
		if seen[part] {
			t.Fatalf("duplicate local part %s in 200 draws", part)
		}
		seen[part] = true
		for j := 0; j < len(part); j++ {
			switch {
			case part[j] >= 'a' && part[j] <= 'z':
				letters++
			case part[j] >= '0' && part[j] <= '9':
				digits++
			}
		}
	}
	if letters == 0 || digits == 0 {
		t.Fatalf("expected both letters and digits, letters=%d digits=%d", letters, digits)
	}
	if ValidLocalPart("abc") || ValidLocalPart("ABCDEFGHIJ") || ValidLocalPart("abcd-efghi") {
		t.Fatal("invalid samples were accepted")
	}
}

func TestNormalizePrefix(t *testing.T) {
	ok := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"A", "a"},
		{" Hello.World ", "hello.world"},
		{"support_1", "support_1"},
		{"a-b", "a-b"},
		{"a.b_c-d", "a.b_c-d"},
		{"9box", "9box"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
	}
	for _, tc := range ok {
		got, err := NormalizePrefix(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("NormalizePrefix(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	bad := []string{
		".",
		"-",
		"_",
		".hello",
		"hello.",
		"-hello",
		"hello-",
		"_hello",
		"hello_",
		"a..b",
		"a...b",
		"hello..world",
		"has space",
		"你好",
		"hello@world",
		"Hello World",
		strings.Repeat("a", 65),
		"a" + strings.Repeat(".b", 40),
	}
	for _, in := range bad {
		got, err := NormalizePrefix(in)
		if !errors.Is(err, ErrInvalidPrefix) || got != "" {
			t.Fatalf("NormalizePrefix(%q) = %q, %v; want invalid", in, got, err)
		}
	}
}

func TestTokenUnguessableLength(t *testing.T) {
	tok, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 40 {
		t.Fatalf("token too short: %d", len(tok))
	}
	for _, c := range tok {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			t.Fatalf("token %q has %q", tok, c)
		}
	}
}
