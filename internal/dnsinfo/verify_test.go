package dnsinfo

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

type fakeResolver struct {
	mx      []*net.MX
	mxErr   error
	ips     []net.IP
	ipErr   error
	mxCalls int
	ipCalls int
}

func (f *fakeResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	f.mxCalls++
	if f.mxErr != nil {
		return nil, f.mxErr
	}
	return f.mx, nil
}

func (f *fakeResolver) LookupIP(context.Context, string) ([]net.IP, error) {
	f.ipCalls++
	if f.ipErr != nil {
		return nil, f.ipErr
	}
	return f.ips, nil
}

func cloudflareMX() []*net.MX {
	return []*net.MX{
		{Host: "ROUTE2.MX.CLOUDFLARE.NET.", Pref: 77},
		{Host: "route1.mx.cloudflare.net.", Pref: 76},
		{Host: "route3.mx.cloudflare.net", Pref: 45},
	}
}

func TestVerifyCloudflare(t *testing.T) {
	ctx := context.Background()
	okRes := &fakeResolver{mx: cloudflareMX()}
	got, err := Verify(ctx, PathCloudflare, "Richrich.CC.CD.", "203.0.113.10", okRes)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Headline != "验证成功" || got.Tone != "ok" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Summary, "Cloudflare Email Routing") {
		t.Fatal(got.Summary)
	}
	if okRes.ipCalls != 0 {
		t.Fatalf("cloudflare check should not look up A records, calls=%d", okRes.ipCalls)
	}
	if okRes.mxCalls != 1 {
		t.Fatal(okRes.mxCalls)
	}

	dup := &fakeResolver{mx: append(cloudflareMX(), &net.MX{Host: "route1.mx.cloudflare.net.", Pref: 5})}
	got, err = Verify(ctx, PathCloudflare, "example.com", "", dup)
	if err != nil || !got.OK {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	missing := &fakeResolver{mx: []*net.MX{{Host: "route1.mx.cloudflare.net.", Pref: 10}}}
	got, err = Verify(ctx, PathCloudflare, "example.com", "", missing)
	if err != nil || got.OK || got.Headline != "验证失败" {
		t.Fatalf("err=%v result=%+v", err, got)
	}
	text := findingsText(got)
	if !strings.Contains(text, "route2.mx.cloudflare.net") || !strings.Contains(text, "没有这条记录") {
		t.Fatal(text)
	}

	extra := &fakeResolver{mx: append(cloudflareMX(), &net.MX{Host: "mail.example.com.", Pref: 10})}
	got, err = Verify(ctx, PathCloudflare, "example.com", "", extra)
	if err != nil || got.OK {
		t.Fatalf("err=%v result=%+v", err, got)
	}
	text = findingsText(got)
	if !strings.Contains(text, "mail.example.com") || !strings.Contains(text, "请删掉") {
		t.Fatal(text)
	}

	empty := &fakeResolver{}
	got, err = Verify(ctx, PathCloudflare, "example.com", "", empty)
	if err != nil || got.OK || !strings.Contains(got.Summary, "没有查到 MX") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	notFound := &fakeResolver{mxErr: &net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}}
	got, err = Verify(ctx, PathCloudflare, "example.com", "", notFound)
	if err != nil || got.OK || !strings.Contains(got.Summary, "没有查到 MX") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	servfail := &fakeResolver{mxErr: &net.DNSError{Err: "server misbehaving", Name: "example.com"}}
	got, err = Verify(ctx, PathCloudflare, "example.com", "", servfail)
	if err == nil || got.Headline != "暂时查不到" || got.Tone != "warn" || got.OK {
		t.Fatalf("err=%v result=%+v", err, got)
	}
	if strings.Contains(got.Summary, "没有查到 MX") || !strings.Contains(got.Summary, "没能完成") {
		t.Fatal(got.Summary)
	}

	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = Verify(ctxCancel, PathCloudflare, "example.com", "", &fakeResolver{mxErr: context.Canceled})
	if !errors.Is(err, context.Canceled) || got.Headline != "暂时查不到" || !strings.Contains(got.Summary, "超时") {
		t.Fatalf("err=%v result=%+v", err, got)
	}
}

func TestVerifySMTP(t *testing.T) {
	ctx := context.Background()
	okRes := &fakeResolver{
		mx:  []*net.MX{{Host: "mail.example.com.", Pref: 20}},
		ips: []net.IP{net.ParseIP("1.2.3.4"), net.ParseIP("1.2.3.4")},
	}
	got, err := Verify(ctx, PathSMTP, "Example.COM.", "1.2.3.4", okRes)
	if err != nil || !got.OK || got.Headline != "验证成功" {
		t.Fatalf("err=%v result=%+v", err, got)
	}
	if !strings.Contains(findingsText(got), "优先级 20") || !strings.Contains(findingsText(got), "1.2.3.4") {
		t.Fatal(findingsText(got))
	}

	backup := &fakeResolver{
		mx: []*net.MX{
			{Host: "mail.example.com.", Pref: 10},
			{Host: "backup.example.net.", Pref: 20},
		},
		ips: []net.IP{net.ParseIP("1.2.3.4")},
	}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", backup)
	if err != nil || !got.OK {
		t.Fatalf("backup mx should still pass: err=%v result=%+v", err, got)
	}

	stolen := &fakeResolver{
		mx: []*net.MX{
			{Host: "route1.mx.cloudflare.net.", Pref: 5},
			{Host: "mail.example.com.", Pref: 10},
		},
		ips: []net.IP{net.ParseIP("1.2.3.4")},
	}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", stolen)
	if err != nil || got.OK || !strings.Contains(findingsText(got), "route1.mx.cloudflare.net") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	tied := &fakeResolver{
		mx: []*net.MX{
			{Host: "mail.example.com.", Pref: 10},
			{Host: "other.example.net.", Pref: 10},
		},
		ips: []net.IP{net.ParseIP("1.2.3.4")},
	}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", tied)
	if err != nil || got.OK || !strings.Contains(findingsText(got), "other.example.net") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	wrongA := &fakeResolver{
		mx:  []*net.MX{{Host: "mail.example.com.", Pref: 10}},
		ips: []net.IP{net.ParseIP("9.9.9.9"), net.ParseIP("1.2.3.4")},
	}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", wrongA)
	if err != nil || got.OK || !strings.Contains(findingsText(got), "9.9.9.9") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	noA := &fakeResolver{
		mx:    []*net.MX{{Host: "mail.example.com.", Pref: 10}},
		ipErr: &net.DNSError{Err: "no such host", Name: "mail.example.com", IsNotFound: true},
	}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", noA)
	if err != nil || got.OK || !strings.Contains(findingsText(got), "没有 A 记录") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	doc := &fakeResolver{
		mx:  []*net.MX{{Host: "mail.example.com.", Pref: 10}},
		ips: []net.IP{net.ParseIP("203.0.113.10")},
	}
	got, err = Verify(ctx, PathSMTP, "example.com", "203.0.113.10", doc)
	if err != nil || got.OK || got.Headline != "验证失败" {
		t.Fatalf("err=%v result=%+v", err, got)
	}
	if !strings.Contains(got.Summary, "收不到公网的信") || !strings.Contains(findingsText(got), "203.0.113.10") {
		t.Fatalf("summary=%s findings=%s", got.Summary, findingsText(got))
	}

	cloudflareOnly := &fakeResolver{mx: cloudflareMX(), ips: []net.IP{net.ParseIP("1.2.3.4")}}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", cloudflareOnly)
	if err != nil || got.OK || !strings.Contains(findingsText(got), "route1.mx.cloudflare.net") {
		t.Fatalf("err=%v result=%+v", err, got)
	}

	timeout := &fakeResolver{ipErr: context.DeadlineExceeded, mx: []*net.MX{{Host: "mail.example.com.", Pref: 10}}}
	got, err = Verify(ctx, PathSMTP, "example.com", "1.2.3.4", timeout)
	if !errors.Is(err, context.DeadlineExceeded) || got.Headline != "暂时查不到" || len(got.Findings) != 0 {
		t.Fatalf("err=%v result=%+v", err, got)
	}
}

func TestVerifyRejectsUnknownPath(t *testing.T) {
	got, err := Verify(context.Background(), "elsewhere", "example.com", "1.2.3.4", &fakeResolver{})
	if err == nil || got.OK || !strings.Contains(got.Summary, "请选择要验证的收信方式") {
		t.Fatalf("err=%v result=%+v", err, got)
	}
	if got.Headline != "验证失败" {
		t.Fatal(got.Headline)
	}
}

func findingsText(got Result) string {
	var b strings.Builder
	for _, f := range got.Findings {
		b.WriteString(f.Title)
		b.WriteByte(' ')
		b.WriteString(f.Detail)
		b.WriteByte('\n')
	}
	return b.String()
}
