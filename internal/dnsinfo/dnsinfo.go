package dnsinfo

import (
	"fmt"
	"net"
	"strings"
)

// Instructions is the DNS checklist shown after a domain is added.
type Instructions struct {
	Domain    string
	MXHost    string
	IP        string
	Note      string
	IPWarning string
}

// For builds the checklist for one domain. mx host is always mail.<domain>.
func For(domain, ip string) Instructions {
	return Instructions{
		Domain:    domain,
		MXHost:    "mail." + domain,
		IP:        strings.TrimSpace(ip),
		Note:      note(domain),
		IPWarning: IPWarning(ip),
	}
}

// Plain is the copy-paste form of the same checklist.
func (in Instructions) Plain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "域名 %s\n", in.Domain)
	fmt.Fprintf(&b, "MX  名称 @  优先级 10  目标 %s  仅 DNS（灰云，不要橙色代理）\n", in.MXHost)
	fmt.Fprintf(&b, "A   名称 mail  完整名称 %s  IPv4 %s  仅 DNS（灰云，不要橙色代理）\n", in.MXHost, in.IP)
	b.WriteString("只接收后台生成过的地址，未知收件人直接拒收。\n")
	b.WriteString(in.Note)
	if in.IPWarning != "" {
		b.WriteString("\n")
		b.WriteString(in.IPWarning)
	}
	return b.String()
}

func note(domain string) string {
	if domain == "richrich.cc.cd" {
		return "richrich.cc.cd 的 DNS 在 Cloudflare。上面两条都要保持「仅 DNS」（灰云），不要打开橙色代理。SMTP 不能走 HTTP 代理。"
	}
	return "如果 DNS 在 Cloudflare（示例域名 richrich.cc.cd 就是这样托管的），每条记录都必须是「仅 DNS」（灰云），不要打开橙色代理。SMTP 不能走 HTTP 代理。"
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
