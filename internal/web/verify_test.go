package web

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"laijian/internal/config"
	"laijian/internal/dnsinfo"
	"laijian/internal/store"
)

type countingResolver struct {
	mx      []*net.MX
	mxErr   error
	ips     []net.IP
	ipErr   error
	mxCalls int
	ipCalls int
}

func (f *countingResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	f.mxCalls++
	if f.mxErr != nil {
		return nil, f.mxErr
	}
	return f.mx, nil
}

func (f *countingResolver) LookupIP(context.Context, string) ([]net.IP, error) {
	f.ipCalls++
	if f.ipErr != nil {
		return nil, f.ipErr
	}
	return f.ips, nil
}

func TestAdminCanVerifyDNS(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		HTTPAddr:           "127.0.0.1:0",
		SMTPAddr:           "127.0.0.1:0",
		SMTPDomain:         "localhost",
		PublicBaseURL:      "http://127.0.0.1:8787",
		AdminPassword:      "test-password-please",
		RetentionDays:      3,
		AttachmentMaxBytes: 64,
		MessageMaxBytes:    1 << 20,
		MailDataDir:        dir,
		DatabasePath:       filepath.Join(dir, "mail.db"),
		ServerPublicIP:     "1.2.3.4",
	}
	st, err := store.Open(cfg.DatabasePath, cfg.MailDataDir, cfg.AttachmentMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetWorkerVerifier(forbidWorker{t})
	idle := &countingResolver{}
	srv.SetDNSResolver(idle)
	webSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(webSrv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	nofollow := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	status, body := postForm(t, client, webSrv.URL+"/admin/login", url.Values{
		"username": {"admin"},
		"password": {"test-password-please"},
	})
	if status != http.StatusOK {
		t.Fatalf("login %d", status)
	}
	csrf := mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+"/admin/domains", url.Values{
		"csrf":   {csrf},
		"domain": {"richrich.cc.cd"},
	})
	if status != http.StatusOK {
		t.Fatalf("add domain %d %s", status, body)
	}
	if idle.mxCalls != 0 || idle.ipCalls != 0 {
		t.Fatalf("opening the domain page looked up DNS mx=%d ip=%d", idle.mxCalls, idle.ipCalls)
	}
	for _, want := range []string{
		"推荐：Cloudflare Email Routing",
		"可选：自建 SMTP（实验室）",
		`name="path" value="cloudflare"`,
		`name="path" value="smtp"`,
		"验证 DNS",
		"验证 Worker",
		"需要手动核对时展开",
		"在 Cloudflare 开启电子邮件路由",
		"路由规则检查需要 CLOUDFLARE_API_TOKEN",
		"验证自建 DNS",
		`data-copy="MX @ route1.mx.cloudflare.net"`,
		"不要把 MX 指到这台机器",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("domain page missing %q", want)
		}
	}
	if strings.Contains(body, "验证成功") || strings.Contains(body, "验证失败") {
		t.Fatal("unchecked page should not show a verification result")
	}
	csrf = mustCSRF(t, body)
	action := regexp.MustCompile(`/admin/domains/\d+/verify`).FindString(body)
	if action == "" {
		t.Fatalf("verify action missing:\n%s", body)
	}

	status, _ = postForm(t, http.DefaultClient, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathCloudflare},
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous verify status %d", status)
	}

	status, body = postForm(t, client, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {"elsewhere"},
	})
	if status != http.StatusBadRequest || !strings.Contains(body, "请选择要验证的收信方式") {
		t.Fatalf("bad path status=%d body=%s", status, body)
	}
	if idle.mxCalls != 0 {
		t.Fatal("rejected path should not query DNS")
	}

	cf := &countingResolver{mx: []*net.MX{
		{Host: "route1.mx.cloudflare.net.", Pref: 76},
		{Host: "route2.mx.cloudflare.net.", Pref: 77},
		{Host: "route3.mx.cloudflare.net.", Pref: 45},
	}}
	srv.SetDNSResolver(cf)
	status, loc := postNoFollow(t, nofollow, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathCloudflare},
	})
	if status != http.StatusSeeOther || !strings.Contains(loc, "checked=cloudflare") {
		t.Fatalf("redirect status=%d loc=%s", status, loc)
	}
	if cf.mxCalls != 1 || cf.ipCalls != 0 {
		t.Fatalf("cloudflare lookup mx=%d ip=%d", cf.mxCalls, cf.ipCalls)
	}
	status, body = get(t, client, absolute(webSrv.URL, loc))
	if status != http.StatusOK {
		t.Fatal(status)
	}
	for _, want := range []string{"验证成功", "查询到的 MX 与 Cloudflare Email Routing 一致", "route2.mx.cloudflare.net", "通过", "检查于", "北京时间"} {
		if !strings.Contains(body, want) {
			t.Fatalf("success page missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `class="path path-optional" open`) {
		t.Fatal("smtp section should stay closed after the cloudflare check")
	}
	before := cf.mxCalls
	status, body = get(t, client, absolute(webSrv.URL, loc))
	if status != http.StatusOK || !strings.Contains(body, "验证成功") {
		t.Fatalf("revisit status=%d", status)
	}
	if cf.mxCalls != before {
		t.Fatal("showing the saved result should not look up DNS again")
	}

	bad := &countingResolver{mx: []*net.MX{{Host: "mail.richrich.cc.cd.", Pref: 10}}}
	srv.SetDNSResolver(bad)
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathCloudflare},
	})
	if status != http.StatusOK || bad.mxCalls != 1 {
		t.Fatalf("recheck status=%d calls=%d", status, bad.mxCalls)
	}
	if strings.Contains(body, "验证成功") || !strings.Contains(body, "验证失败") || !strings.Contains(body, "没有这条记录") {
		t.Fatalf("recheck body:\n%s", body)
	}

	smtp := &countingResolver{
		mx:  []*net.MX{{Host: "mail.richrich.cc.cd.", Pref: 10}},
		ips: []net.IP{net.ParseIP("1.2.3.4")},
	}
	srv.SetDNSResolver(smtp)
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathSMTP},
	})
	if status != http.StatusOK || !strings.Contains(body, "验证成功") || !strings.Contains(body, `class="path path-optional" open`) {
		t.Fatalf("smtp pass status=%d open=%v", status, strings.Contains(body, "open"))
	}
	if !strings.Contains(body, "1.2.3.4") || smtp.ipCalls != 1 {
		t.Fatalf("smtp findings missing or ip calls=%d", smtp.ipCalls)
	}

	mismatch := &countingResolver{
		mx:  []*net.MX{{Host: "mail.richrich.cc.cd.", Pref: 10}},
		ips: []net.IP{net.ParseIP("9.9.9.9")},
	}
	srv.SetDNSResolver(mismatch)
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathSMTP},
	})
	if status != http.StatusOK || strings.Contains(body, "验证成功") || !strings.Contains(body, "验证失败") || !strings.Contains(body, "9.9.9.9") {
		t.Fatalf("smtp fail body:\n%s", body)
	}

	slow := &countingResolver{mxErr: context.DeadlineExceeded}
	srv.SetDNSResolver(slow)
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+action, url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathCloudflare},
	})
	if status != http.StatusOK || !strings.Contains(body, "暂时查不到") || !strings.Contains(body, "超时") {
		t.Fatalf("timeout body:\n%s", body)
	}

	status, _ = get(t, client, webSrv.URL+"/admin/domains/99999/verify")
	if status != http.StatusNotFound {
		t.Fatalf("get verify %d", status)
	}
	status, body = postForm(t, client, webSrv.URL+"/admin/domains/99999/verify", url.Values{
		"csrf": {csrf},
		"path": {dnsinfo.PathCloudflare},
	})
	if status != http.StatusNotFound || !strings.Contains(body, "找不到") {
		t.Fatalf("missing domain status=%d body=%s", status, body)
	}
}

func get(t *testing.T, c *http.Client, rawURL string) (int, string) {
	t.Helper()
	res, err := c.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

func postForm(t *testing.T, c *http.Client, rawURL string, form url.Values) (int, string) {
	t.Helper()
	res, err := c.PostForm(rawURL, form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

func postNoFollow(t *testing.T, c *http.Client, rawURL string, form url.Values) (int, string) {
	t.Helper()
	res, err := c.PostForm(rawURL, form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, res.Header.Get("Location")
}

func mustCSRF(t *testing.T, body string) string {
	t.Helper()
	const needle = `name="csrf" value="`
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatalf("csrf missing")
	}
	rest := body[i+len(needle):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("csrf unterminated")
	}
	return rest[:j]
}

func absolute(base, loc string) string {
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		return loc
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(loc, "/")
}
