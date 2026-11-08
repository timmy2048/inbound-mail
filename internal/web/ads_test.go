package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"laijian/internal/config"
	"laijian/internal/generate"
	"laijian/internal/parse"
	"laijian/internal/store"
)

func TestAdURLAndCSP(t *testing.T) {
	ok := []struct{ in, src string }{
		{"https://cdn.example.com/a.png", "https://cdn.example.com"},
		{"http://127.0.0.1:8787/static/favicon.svg", "http://127.0.0.1:8787"},
		{"https://cdn.example.com:8443/a.png?x=1", "https://cdn.example.com:8443"},
		{"https://[2001:db8::1]/a.png", "https://[2001:db8::1]"},
	}
	for _, tc := range ok {
		got, yes := safeAdURL(tc.in)
		if !yes || got != tc.in || adImageSource(tc.in) != tc.src {
			t.Fatalf("safeAdURL(%q)=%q,%v source=%q", tc.in, got, yes, adImageSource(tc.in))
		}
	}
	for _, bad := range []string{
		"",
		"javascript:alert(1)",
		"data:image/png;base64,aaaa",
		"//cdn.example.com/a.png",
		"https://user:pass@cdn.example.com/a.png",
		"https://cdn.example.com/a.png?q=1 2",
		"https://ex ample.com/a.png",
		"ftp://cdn.example.com/a.png",
	} {
		if _, yes := safeAdURL(bad); yes {
			t.Fatalf("accepted %q", bad)
		}
	}
	strict := pageCSP("")
	if strict != "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'" {
		t.Fatal(strict)
	}
	image := pageCSP("https://cdn.example.com")
	if !strings.Contains(image, "img-src 'self' data: https://cdn.example.com") || strings.Contains(image, "unsafe-inline") {
		t.Fatal(image)
	}
	loose := adScriptPageCSP()
	if !strings.Contains(loose, "script-src 'self' https: http: 'unsafe-inline' 'unsafe-eval'") || !strings.Contains(loose, "frame-src 'self' https: http:") || !strings.Contains(loose, "frame-ancestors 'none'") || !strings.Contains(loose, "object-src 'none'") {
		t.Fatal(loose)
	}
}

func TestInboxAdAndMailFrameCSP(t *testing.T) {
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
		ServerPublicIP:     "203.0.113.10",
	}
	st, err := store.Open(cfg.DatabasePath, cfg.MailDataDir, cfg.AttachmentMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d, err := st.CreateDomain("richrich.cc.cd", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := st.CreateMailboxes(d.ID, 1, generate.DefaultLocalPartLength, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	msgID, err := st.SaveMessage(boxes[0].ID, boxes[0].Address, parse.Parsed{
		Sender:  "ada@example.com",
		Subject: "hello",
		Text:    "plain",
		HTML:    `<p><a href="https://example.com/open">打开</a></p>`,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	webSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(webSrv.Close)

	inboxURL := webSrv.URL + "/m/" + boxes[0].Token
	res := mustGET(t, inboxURL)
	if res.StatusCode != http.StatusOK || strings.Contains(res.Body, `class="ad-slot"`) {
		t.Fatalf("empty ad leaked status=%d body=%s", res.StatusCode, res.Body)
	}
	if res.Header.Get("Content-Security-Policy") != pageCSP("") {
		t.Fatalf("strict csp %s", res.Header.Get("Content-Security-Policy"))
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, body := postForm(t, client, webSrv.URL+"/admin/login", url.Values{
		"username": {"admin"},
		"password": {"test-password-please"},
	})
	if status != http.StatusOK {
		t.Fatalf("login %d", status)
	}
	csrf := mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+"/admin/ads", url.Values{
		"csrf":      {csrf},
		"image_url": {"javascript:alert(1)"},
		"enabled":   {"1"},
	})
	if status != http.StatusBadRequest || !strings.Contains(body, "图片 URL 需要是 http 或 https 链接。") {
		t.Fatalf("bad image status=%d body=%s", status, body)
	}
	for _, gone := range []string{"验证 DNS", "验证 Worker", "收信 DNS", "自建 SMTP", "Cloudflare Email Routing"} {
		if strings.Contains(body, gone) {
			t.Fatalf("ads page shows %q", gone)
		}
	}

	script := `<ins class="adsbygoogle"></ins><script>window.__ad = 1</script>`
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+"/admin/ads", url.Values{
		"csrf":        {csrf},
		"enabled":     {"1"},
		"image_url":   {"https://cdn.example.com/banner.png"},
		"link_url":    {"https://example.com/go"},
		"title":       {"合作推广"},
		"description": {"收件页底部"},
		"html":        {script},
	})
	if status != http.StatusOK || !strings.Contains(body, "已保存广告设置。") || !strings.Contains(body, "合作推广") {
		t.Fatalf("save status=%d body=%s", status, body)
	}
	if strings.Contains(body, "<script>window.__ad") || !strings.Contains(body, "&lt;script&gt;window.__ad") {
		t.Fatal("admin page executed or dropped the pasted script")
	}
	adminCSP := headerGET(t, client, webSrv.URL+"/admin/ads")
	if strings.Contains(adminCSP, "unsafe-inline") || strings.Contains(adminCSP, "unsafe-eval") {
		t.Fatalf("admin csp %s", adminCSP)
	}

	page := mustGET(t, inboxURL)
	if page.StatusCode != http.StatusOK {
		t.Fatal(page.StatusCode)
	}
	for _, want := range []string{
		`class="ad-slot"`,
		`href="https://example.com/go"`,
		`target="_blank"`,
		`rel="sponsored noopener noreferrer"`,
		`src="https://cdn.example.com/banner.png"`,
		"合作推广",
		"收件页底部",
		script,
	} {
		if !strings.Contains(page.Body, want) {
			t.Fatalf("inbox missing %q\n%s", want, page.Body)
		}
	}
	if page.Header.Get("Content-Security-Policy") != adScriptPageCSP() {
		t.Fatalf("inbox csp %s", page.Header.Get("Content-Security-Policy"))
	}
	if page.Header.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatalf("inbox coop %s", page.Header.Get("Cross-Origin-Opener-Policy"))
	}

	msgURL := inboxURL + "/messages/" + strconv.FormatInt(msgID, 10)
	msg := mustGET(t, msgURL)
	if !strings.Contains(msg.Body, `class="ad-slot"`) || !strings.Contains(msg.Body, script) {
		t.Fatal("message page missing ad")
	}
	if !strings.Contains(msg.Body, `sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"`) {
		t.Fatal("mail iframe sandbox changed")
	}
	frame := mustGET(t, msgURL+"/html")
	csp := frame.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src data:") || strings.Contains(csp, "https:") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "img-src http") {
		t.Fatalf("mail csp %s", csp)
	}
	if frame.Header.Get("Cross-Origin-Opener-Policy") != "unsafe-none" {
		t.Fatalf("mail coop %s", frame.Header.Get("Cross-Origin-Opener-Policy"))
	}
	if !strings.Contains(frame.Body, `target="_blank"`) || !strings.Contains(frame.Body, `rel="noopener noreferrer"`) {
		t.Fatal(frame.Body)
	}

	home := mustGET(t, webSrv.URL+"/")
	if strings.Contains(home.Body, `class="ad-slot"`) || strings.Contains(home.Body, "合作推广") {
		t.Fatal("home shows the ad")
	}

	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+"/admin/ads", url.Values{
		"csrf":      {csrf},
		"image_url": {"https://cdn.example.com/banner.png"},
		"link_url":  {"https://example.com/go"},
		"title":     {"合作推广"},
		"html":      {script},
	})
	if status != http.StatusOK || !strings.Contains(body, "已保存广告设置。") {
		t.Fatalf("disable status=%d body=%s", status, body)
	}
	off := mustGET(t, inboxURL)
	if strings.Contains(off.Body, `class="ad-slot"`) || strings.Contains(off.Body, "合作推广") || strings.Contains(off.Body, "__ad") {
		t.Fatal("disabled ad still rendered")
	}
	if off.Header.Get("Content-Security-Policy") != pageCSP("") {
		t.Fatalf("disabled csp %s", off.Header.Get("Content-Security-Policy"))
	}

	csrf = mustCSRF(t, body)
	status, _ = postForm(t, client, webSrv.URL+"/admin/ads", url.Values{
		"csrf":      {csrf},
		"enabled":   {"1"},
		"image_url": {"https://cdn.example.com/only.png"},
		"link_url":  {"https://example.com/only"},
		"title":     {"仅图片"},
	})
	if status != http.StatusOK {
		t.Fatalf("image only %d", status)
	}
	imagePage := mustGET(t, inboxURL)
	if !strings.Contains(imagePage.Body, `src="https://cdn.example.com/only.png"`) || strings.Contains(imagePage.Body, "__ad") {
		t.Fatal(imagePage.Body)
	}
	if imagePage.Header.Get("Content-Security-Policy") != pageCSP("https://cdn.example.com") {
		t.Fatalf("image csp %s", imagePage.Header.Get("Content-Security-Policy"))
	}

	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, _ = postForm(t, anon, webSrv.URL+"/admin/ads", url.Values{"enabled": {"1"}, "html": {"<script>alert(1)</script>"}})
	if status != http.StatusUnauthorized {
		t.Fatalf("anon post %d", status)
	}
	kept := mustGET(t, inboxURL)
	if strings.Contains(kept.Body, "alert(1)") {
		t.Fatal("anonymous post changed the ad")
	}
}

type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       string
}

func mustGET(t *testing.T, rawURL string) httpResult {
	t.Helper()
	res, err := http.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{StatusCode: res.StatusCode, Header: res.Header.Clone(), Body: string(b)}
}

func headerGET(t *testing.T, c *http.Client, rawURL string) string {
	t.Helper()
	res, err := c.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.Header.Get("Content-Security-Policy")
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
