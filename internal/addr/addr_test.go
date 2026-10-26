package addr

import "testing"

func TestNormalizeDomain(t *testing.T) {
	got, err := NormalizeDomain("  RichRich.CC.CD. ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "richrich.cc.cd" {
		t.Fatal(got)
	}
	for _, bad := range []string{"", "localhost", "rich rich.cc", "例子.cn", "-a.com", "a-.com", "http://richrich.cc.cd"} {
		if _, err := NormalizeDomain(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail("<AbCDefgh12@RichRich.CC.CD>")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abcdefgh12@richrich.cc.cd" {
		t.Fatal(got)
	}
}
