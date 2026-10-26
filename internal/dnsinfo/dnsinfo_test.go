package dnsinfo

import (
	"strings"
	"testing"
)

func TestChecklistForExampleDomain(t *testing.T) {
	in := For("richrich.cc.cd", "203.0.113.10")
	if in.MXHost != "mail.richrich.cc.cd" {
		t.Fatal(in.MXHost)
	}
	text := in.Plain()
	for _, want := range []string{
		"mail.richrich.cc.cd",
		"MX",
		"优先级 10",
		"203.0.113.10",
		"仅 DNS",
		"灰云",
		"Cloudflare",
		"未知收件人直接拒收",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("checklist missing %q:\n%s", want, text)
		}
	}
	if in.IPWarning == "" {
		t.Fatal("documentation IP should warn")
	}
	if IPWarning("1.2.3.4") != "" {
		t.Fatal("public IP should not warn")
	}
	other := For("example.test", "1.2.3.4")
	if other.IPWarning != "" {
		t.Fatal(other.IPWarning)
	}
	if !strings.Contains(other.Note, "richrich.cc.cd") || !strings.Contains(other.Note, "灰云") {
		t.Fatal(other.Note)
	}
}
