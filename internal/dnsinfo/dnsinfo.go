package dnsinfo

import (
	"fmt"
	"net"
	"strings"
)

const (
	// PathCloudflare is production receive: MX points at Cloudflare Email Routing.
	// OCI and similar hosts block inbound port 25, so the VPS must not be the MX.
	PathCloudflare = "cloudflare"
	// PathSMTP is optional self-hosted receive, only when public port 25 works.
	PathSMTP = "smtp"
)

// CloudflareMX are the Email Routing exchanges published for a zone.
// Priorities are assigned per zone (they are not a global constant), so the
// checklist and the verifier compare hostnames only.
var CloudflareMX = []string{
	"route1.mx.cloudflare.net",
	"route2.mx.cloudflare.net",
	"route3.mx.cloudflare.net",
}

// Record is one copyable DNS row.
type Record struct {
	Type  string
	Value string
	Meta  string
	Copy  string
}

// Section is one receive path on the domain page.
type Section struct {
	Path     string
	Title    string
	Summary  string
	Steps    []string
	Records  []Record
	CopyAll  string
	Note     string
	Warning  string
	RecordsA string
}

// Guide is the DNS page shown after a domain is added.
type Guide struct {
	Domain      string
	Recommended Section
	Optional    Section
}

// GuideFor builds the checklist for one domain.
// ip is SERVER_PUBLIC_IP and is used only by the optional self-hosted path.
func GuideFor(domain, ip string) Guide {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	ip = strings.TrimSpace(ip)
	return Guide{
		Domain:      domain,
		Recommended: cloudflareSection(),
		Optional:    smtpSection(domain, ip),
	}
}

func cloudflareSection() Section {
	records := make([]Record, 0, len(CloudflareMX))
	lines := make([]string, 0, len(CloudflareMX))
	for _, host := range CloudflareMX {
		line := "MX @ " + host
		lines = append(lines, line)
		records = append(records, Record{
			Type:  "MX",
			Value: host,
			Copy:  line,
		})
	}
	return Section{
		Path:    PathCloudflare,
		Title:   "推荐：Cloudflare Email Routing",
		Summary: "公网来信走这里。OCI 拦截 25 端口，不要把 MX 指到这台机器。",
		Steps: []string{
			"在 Cloudflare 打开这个域名的 Email Routing。",
			"留下下面三条 MX，删掉其他 MX。",
			"点「验证 DNS」。三条都在、且没有别的 MX，才算通过。",
		},
		Records:  records,
		CopyAll:  strings.Join(lines, "\n"),
		Note:     "名称填 @（有的面板要填域名本身）。优先级由 Cloudflare 填写，手填时三条用不同数字即可。这版只收信，不用手填 DKIM。",
		RecordsA: "Cloudflare MX 记录",
	}
}

func smtpSection(domain, ip string) Section {
	host := "mail." + domain
	ipShown := ip
	ipCopy := ""
	if ipShown == "" {
		ipShown = "未设置"
	} else {
		ipCopy = "A mail " + ip
	}
	mxLine := "MX @ 10 " + host
	lines := []string{mxLine}
	if ipCopy != "" {
		lines = append(lines, ipCopy)
	}
	return Section{
		Path:    PathSMTP,
		Title:   "可选：自建 SMTP（实验室）",
		Summary: "仅当公网能连上这台机器的 25 端口。本地注入走 127.0.0.1:2525，不用配这里。",
		Steps: []string{
			"MX 指向下面的邮件主机，优先级 10。",
			"A 记录把 mail 指到服务器的公网 IPv4。",
			"点「验证自建 DNS」。",
		},
		Records: []Record{
			{
				Type:  "MX",
				Value: host,
				Meta:  "名称 @ · 优先级 10",
				Copy:  mxLine,
			},
			{
				Type:  "A",
				Value: ipShown,
				Meta:  "名称 mail · 完整名称 " + host,
				Copy:  ipCopy,
			},
		},
		CopyAll:  strings.Join(lines, "\n"),
		Note:     "两条都选仅 DNS（灰云），不要开橙色代理。",
		Warning:  IPWarning(ip),
		RecordsA: "自建 SMTP 记录",
	}
}

// IPWarning is empty when ip is a public IPv4 that can go in an A record.
func IPWarning(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "还没有设置 SERVER_PUBLIC_IP。外网的信投递不到。本地注入测试不受影响。"
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return "SERVER_PUBLIC_IP 需要是这台机器的公网 IPv4，才能写进 A 记录。本地注入测试不受影响。"
	}
	if docOrPrivate(parsed) {
		return fmt.Sprintf("当前服务器 IP 仍是 %s，不是可收信的公网地址。上机前改成 VPS 的公网 IPv4 并重启。本地注入测试不受影响。", ip)
	}
	return ""
}

func docOrPrivate(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return true
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return true
	}
	for _, cidr := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func publicIPv4(ip string) bool {
	return IPWarning(ip) == ""
}
