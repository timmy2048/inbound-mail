package dnsinfo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Resolver looks up the records a path needs. Tests supply a fake.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

// SystemResolver uses the process resolver. LookupIP is IPv4 only.
type SystemResolver struct{}

func (SystemResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, name)
}

func (SystemResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip4", host)
}

// Finding is one line in the verification result.
type Finding struct {
	OK     bool
	Title  string
	Detail string
}

// Result is the outcome of one DNS check. OK is true only when the path is effective.
type Result struct {
	Path     string
	OK       bool
	Tone     string
	Headline string
	Summary  string
	Findings []Finding
}

// Verify checks the domain against the chosen receive path.
// A non-nil error means the lookup itself failed; Result is still safe to show.
func Verify(ctx context.Context, path, domain, ip string, r Resolver) (Result, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" {
		return fail(path, "域名无效。"), nil
	}
	if r == nil {
		r = SystemResolver{}
	}
	switch path {
	case PathCloudflare:
		return verifyCloudflare(ctx, domain, r)
	case PathSMTP:
		return verifySMTP(ctx, domain, strings.TrimSpace(ip), r)
	default:
		return fail(path, "请选择要验证的收信方式。"), fmt.Errorf("unknown dns path %q", path)
	}
}

func verifyCloudflare(ctx context.Context, domain string, r Resolver) (Result, error) {
	mx, err := lookupMX(ctx, r, domain)
	if err != nil {
		return lookupProblem(PathCloudflare, err), err
	}
	seen := map[string]uint16{}
	var order []string
	for _, rec := range mx {
		host := normHost(rec.Host)
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = rec.Pref
		order = append(order, host)
	}

	findings := make([]Finding, 0, len(CloudflareMX)+4)
	ok := len(order) > 0
	for _, host := range CloudflareMX {
		pref, found := seen[host]
		if !found {
			ok = false
			findings = append(findings, Finding{Title: host, Detail: "没有这条记录"})
			continue
		}
		findings = append(findings, Finding{OK: true, Title: host, Detail: fmt.Sprintf("优先级 %d", pref)})
	}
	extras := 0
	for _, host := range order {
		if isCloudflareMX(host) {
			continue
		}
		extras++
		ok = false
		if extras > 8 {
			continue
		}
		findings = append(findings, Finding{
			Title:  host,
			Detail: fmt.Sprintf("这不是 Email Routing 的 MX，请删掉。优先级 %d", seen[host]),
		})
	}
	if extras > 8 {
		findings = append(findings, Finding{
			Title:  "其他 MX",
			Detail: fmt.Sprintf("还有 %d 条没有列出来，也请删掉", extras-8),
		})
	}

	if len(order) == 0 {
		return Result{
			Path:     PathCloudflare,
			Tone:     "error",
			Headline: "验证失败",
			Summary:  "没有查到 MX 记录。",
			Findings: findings,
		}, nil
	}
	if !ok {
		return Result{
			Path:     PathCloudflare,
			Tone:     "error",
			Headline: "验证失败",
			Summary:  "MX 还没只指向 Cloudflare Email Routing。",
			Findings: findings,
		}, nil
	}
	return Result{
		Path:     PathCloudflare,
		OK:       true,
		Tone:     "ok",
		Headline: "验证成功",
		Summary:  "查询到的 MX 与 Cloudflare Email Routing 一致。",
		Findings: findings,
	}, nil
}

func verifySMTP(ctx context.Context, domain, ip string, r Resolver) (Result, error) {
	host := "mail." + domain
	mx, err := lookupMX(ctx, r, domain)
	if err != nil {
		return lookupProblem(PathSMTP, err), err
	}
	ips, ipErr := r.LookupIP(ctx, host)
	if ipErr != nil && !isNotFound(ipErr) {
		return lookupProblem(PathSMTP, ipErr), ipErr
	}
	if isNotFound(ipErr) {
		ips = nil
	}
	mxFinding := smtpMXFinding(host, mx)
	aFinding := smtpAFinding(host, ip, ips)
	findings := []Finding{mxFinding, aFinding}
	if mxFinding.OK && aFinding.OK {
		return Result{
			Path:     PathSMTP,
			OK:       true,
			Tone:     "ok",
			Headline: "验证成功",
			Summary:  "查询到的 MX 和 A 记录与自建 SMTP 一致。",
			Findings: findings,
		}, nil
	}
	summary := "自建 SMTP 的 MX 或 A 记录还没对上。"
	if mxFinding.OK && !aFinding.OK && !publicIPv4(ip) && aRecordMatches(ip, ips) {
		summary = "记录已经写上，但这个地址收不到公网的信。"
	}
	return Result{
		Path:     PathSMTP,
		Tone:     "error",
		Headline: "验证失败",
		Summary:  summary,
		Findings: findings,
	}, nil
}

func smtpMXFinding(host string, mx []*net.MX) Finding {
	title := "MX " + host
	if len(mx) == 0 {
		return Finding{Title: title, Detail: "没有 MX 记录"}
	}
	best := mx[0].Pref
	seenBest := map[string]bool{}
	var bestHosts []string
	var expectedPref uint16
	saw := false
	for _, rec := range mx {
		h := normHost(rec.Host)
		if h == host {
			saw = true
			expectedPref = rec.Pref
		}
		if rec.Pref == best && !seenBest[h] {
			seenBest[h] = true
			bestHosts = append(bestHosts, h)
		}
	}
	if !saw {
		return Finding{Title: title, Detail: "当前是 " + joinMX(mx)}
	}
	if len(bestHosts) != 1 || bestHosts[0] != host {
		return Finding{Title: title, Detail: "最先投递的是 " + strings.Join(bestHosts, "、") + "。需要让 " + host + " 单独占最小的优先级数字"}
	}
	return Finding{OK: true, Title: title, Detail: fmt.Sprintf("优先级 %d，会最先投递", expectedPref)}
}

func smtpAFinding(host, ip string, ips []net.IP) Finding {
	title := "A " + host
	got := uniqueIPv4(ips)
	want := strings.TrimSpace(ip)
	parsed := net.ParseIP(want)
	if want == "" {
		return Finding{Title: title, Detail: "还没有设置 SERVER_PUBLIC_IP"}
	}
	if parsed == nil || parsed.To4() == nil {
		return Finding{Title: title, Detail: "SERVER_PUBLIC_IP 不是 IPv4"}
	}
	want = parsed.To4().String()
	if len(got) == 0 {
		return Finding{Title: title, Detail: "没有 A 记录"}
	}
	if len(got) == 1 && got[0] == want {
		if !publicIPv4(want) {
			return Finding{Title: title, Detail: IPWarning(want)}
		}
		return Finding{OK: true, Title: title, Detail: want}
	}
	return Finding{Title: title, Detail: "解析到 " + strings.Join(got, "、") + "，需要 " + want}
}

func aRecordMatches(ip string, ips []net.IP) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || parsed.To4() == nil {
		return false
	}
	got := uniqueIPv4(ips)
	return len(got) == 1 && got[0] == parsed.To4().String()
}

func lookupMX(ctx context.Context, r Resolver, name string) ([]*net.MX, error) {
	mx, err := r.LookupMX(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]*net.MX, 0, len(mx))
	for _, rec := range mx {
		if rec == nil || normHost(rec.Host) == "" {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pref != out[j].Pref {
			return out[i].Pref < out[j].Pref
		}
		return normHost(out[i].Host) < normHost(out[j].Host)
	})
	return out, nil
}

func lookupProblem(path string, err error) Result {
	summary := "这次没能完成 DNS 查询。请再试一次。"
	if isTimeout(err) {
		summary = "DNS 查询超时。记录可能还在传播，过几分钟再验证。"
	}
	return Result{
		Path:     path,
		Tone:     "warn",
		Headline: "暂时查不到",
		Summary:  summary,
	}
}

func fail(path, summary string) Result {
	return Result{
		Path:     path,
		Tone:     "error",
		Headline: "验证失败",
		Summary:  summary,
	}
}

func isCloudflareMX(host string) bool {
	for _, want := range CloudflareMX {
		if host == want {
			return true
		}
	}
	return false
}

func normHost(host string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
}

func joinMX(mx []*net.MX) string {
	parts := make([]string, 0, len(mx))
	seen := map[string]bool{}
	extra := 0
	for _, rec := range mx {
		host := normHost(rec.Host)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		if len(parts) == 6 {
			extra++
			continue
		}
		parts = append(parts, fmt.Sprintf("%s（优先级 %d）", host, rec.Pref))
	}
	text := strings.Join(parts, "、")
	if extra > 0 {
		text += " 等"
	}
	return text
}

func uniqueIPv4(ips []net.IP) []string {
	seen := map[string]bool{}
	var out []string
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		s := v4.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
