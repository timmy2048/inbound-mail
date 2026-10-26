package dnsinfo

import (
	"strings"
	"testing"
)

func TestGuideSeparatesReceivePaths(t *testing.T) {
	g := GuideFor("Richrich.CC.CD.", "203.0.113.10")
	if g.Domain != "richrich.cc.cd" {
		t.Fatal(g.Domain)
	}
	if g.Recommended.Title != "推荐：Cloudflare Email Routing" {
		t.Fatal(g.Recommended.Title)
	}
	if g.Optional.Title != "可选：自建 SMTP（实验室）" {
		t.Fatal(g.Optional.Title)
	}
	if len(g.Recommended.Records) != len(CloudflareMX) {
		t.Fatalf("records %+v", g.Recommended.Records)
	}
	for i, host := range CloudflareMX {
		rec := g.Recommended.Records[i]
		if rec.Type != "MX" || rec.Value != host {
			t.Fatalf("record %+v", rec)
		}
		if strings.Contains(rec.Value, "203.0.113.10") || strings.HasPrefix(rec.Value, "mail.") {
			t.Fatalf("recommended record points at the VPS: %+v", rec)
		}
		if !strings.Contains(g.Recommended.CopyAll, "MX @ "+host) {
			t.Fatal(g.Recommended.CopyAll)
		}
	}
	prose := g.Recommended.Summary + "\n" + g.Recommended.Note + "\n" + strings.Join(g.Recommended.Steps, "\n")
	for _, want := range []string{"OCI", "25", "不要把 MX 指到这台机器", "Email Routing", "验证 DNS"} {
		if !strings.Contains(prose, want) {
			t.Fatalf("recommended copy missing %q:\n%s", want, prose)
		}
	}
	if strings.Contains(prose, "灰云") || strings.Contains(prose, "SERVER_PUBLIC_IP") {
		t.Fatalf("recommended copy still reads like self-hosted SMTP:\n%s", prose)
	}

	if len(g.Optional.Records) != 2 {
		t.Fatalf("optional %+v", g.Optional.Records)
	}
	if g.Optional.Records[0].Value != "mail.richrich.cc.cd" || !strings.Contains(g.Optional.Records[0].Meta, "优先级 10") {
		t.Fatalf("mx %+v", g.Optional.Records[0])
	}
	if g.Optional.Records[1].Value != "203.0.113.10" {
		t.Fatalf("a %+v", g.Optional.Records[1])
	}
	if !strings.Contains(g.Optional.Note, "灰云") || !strings.Contains(g.Optional.Note, "橙色代理") {
		t.Fatal(g.Optional.Note)
	}
	if !strings.Contains(g.Optional.Summary, "127.0.0.1:2525") {
		t.Fatal(g.Optional.Summary)
	}
	if g.Optional.Warning == "" || !strings.Contains(g.Optional.Warning, "203.0.113.10") {
		t.Fatal(g.Optional.Warning)
	}

	public := GuideFor("example.test", "1.2.3.4")
	if public.Optional.Warning != "" {
		t.Fatal(public.Optional.Warning)
	}
	if public.Optional.Records[1].Copy != "A mail 1.2.3.4" {
		t.Fatal(public.Optional.Records[1].Copy)
	}
	unset := GuideFor("example.test", "  ")
	if unset.Optional.Records[1].Value != "未设置" || unset.Optional.Records[1].Copy != "" {
		t.Fatalf("unset %+v", unset.Optional.Records[1])
	}
	if IPWarning("1.2.3.4") != "" {
		t.Fatal("public IP should not warn")
	}
	if IPWarning("10.1.1.1") == "" || IPWarning("") == "" || IPWarning("not-an-ip") == "" {
		t.Fatal("bad addresses should warn")
	}
}
