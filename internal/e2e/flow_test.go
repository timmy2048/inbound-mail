package e2e

import (
	"bytes"
	"context"
	"encoding/csv"
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
	"time"

	netsmtp "net/smtp"

	"laijian/internal/config"
	"laijian/internal/generate"
	"laijian/internal/parse"
	"laijian/internal/retention"
	"laijian/internal/rfc822"
	"laijian/internal/smtpsrv"
	"laijian/internal/store"
	"laijian/internal/web"
)

func TestAdminInboxAndInboundSMTP(t *testing.T) {
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

	httpSrv, err := web.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	webSrv := httptest.NewServer(httpSrv.Handler())
	t.Cleanup(webSrv.Close)

	smtpSrv, err := smtpsrv.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = smtpSrv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = smtpSrv.Shutdown(ctx)
	})

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	nofollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	status, body := get(t, nofollow, webSrv.URL+"/admin")
	if status != http.StatusSeeOther {
		t.Fatalf("admin without login status %d", status)
	}

	status, body = postForm(t, client, webSrv.URL+"/admin/login", url.Values{
		"username": {"admin"},
		"password": {"wrong-password"},
	})
	if status != http.StatusUnauthorized || !strings.Contains(body, "密码不正确") {
		t.Fatalf("bad login status=%d body=%s", status, body)
	}

	status, body = postForm(t, client, webSrv.URL+"/admin/login", url.Values{
		"username": {"admin"},
		"password": {"test-password-please"},
	})
	if status != http.StatusOK || !strings.Contains(body, "添加域名") {
		t.Fatalf("login status=%d body=%s", status, body)
	}
	csrf := mustCSRF(t, body)

	status, body = postForm(t, client, webSrv.URL+"/admin/domains", url.Values{
		"csrf":   {csrf},
		"domain": {"richrich.cc.cd"},
	})
	if status != http.StatusOK {
		t.Fatalf("add domain status=%d body=%s", status, body)
	}
	for _, want := range []string{
		"收信 DNS",
		"推荐：Cloudflare Email Routing",
		"route1.mx.cloudflare.net",
		"route2.mx.cloudflare.net",
		"route3.mx.cloudflare.net",
		"验证 DNS",
		"可选：自建 SMTP（实验室）",
		"mail.richrich.cc.cd",
		"203.0.113.10",
		"灰云",
		"不要把 MX 指到这台机器",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dns page missing %q", want)
		}
	}
	if strings.Contains(body, "把收信指到这台服务器") {
		t.Fatal("old dns heading still shown")
	}
	cfAt := strings.Index(body, "推荐：Cloudflare Email Routing")
	selfAt := strings.Index(body, "可选：自建 SMTP（实验室）")
	if cfAt < 0 || selfAt < 0 || cfAt > selfAt {
		t.Fatalf("path order cf=%d self=%d", cfAt, selfAt)
	}
	csrf = mustCSRF(t, body)
	domainPath := regexp.MustCompile(`/admin/domains/\d+`).FindString(body)
	if domainPath == "" {
		t.Fatalf("domain path missing")
	}

	status, body = postForm(t, client, webSrv.URL+"/admin/domains", url.Values{
		"csrf":   {csrf},
		"domain": {"richrich.cc.cd"},
	})
	if status != http.StatusConflict || !strings.Contains(body, "已经添加过了") {
		t.Fatalf("duplicate status=%d body=%s", status, body)
	}

	domainURL := webSrv.URL + domainPath
	status, body = get(t, client, domainURL)
	if status != http.StatusOK {
		t.Fatalf("domain page %d", status)
	}
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, domainURL+"/mailboxes", url.Values{
		"csrf":  {csrf},
		"count": {"4"},
	})
	if status != http.StatusOK || !strings.Contains(body, "已生成 4 个邮箱") {
		t.Fatalf("generate status=%d body=%s", status, body)
	}

	addresses := regexp.MustCompile(`[a-z0-9]{10}@richrich\.cc\.cd`).FindAllString(body, -1)
	uniq := map[string]bool{}
	for _, addr := range addresses {
		local, _, _ := strings.Cut(addr, "@")
		if !generate.ValidLocalPart(local) {
			t.Fatalf("bad local part %s", local)
		}
		uniq[addr] = true
	}
	if len(uniq) != 4 {
		t.Fatalf("addresses = %v", uniq)
	}

	status, csvBody := get(t, client, webSrv.URL+"/admin/mailboxes.csv")
	if status != http.StatusOK {
		t.Fatal(status)
	}
	if !strings.HasPrefix(csvBody, "\uFEFF") {
		t.Fatal("csv missing utf-8 bom")
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvBody, "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("csv rows %+v", records)
	}
	if strings.Join(records[0], ",") != "address,magic_link,created_at,domain" {
		t.Fatalf("header %v", records[0])
	}
	first := records[1]
	if first[3] != "richrich.cc.cd" || !strings.HasPrefix(first[1], cfg.PublicBaseURL+"/m/") {
		t.Fatalf("row %+v", first)
	}
	token := strings.TrimPrefix(first[1], cfg.PublicBaseURL+"/m/")
	target := first[0]

	if err := deliver(ln.Addr().String(), "stranger@example.com", "nobody@richrich.cc.cd", rfc822.Text("stranger@example.com", "nobody@richrich.cc.cd", "nope", "no")); err == nil {
		t.Fatal("unknown recipient was accepted")
	}
	kept := rfc822.Text("stranger@example.com", target, "kept-subject", "hello mailbox")
	if err := deliver(ln.Addr().String(), "stranger@example.com", target, kept); err != nil {
		t.Fatal(err)
	}
	again := rfc822.Text("stranger@example.com", target, "second-subject", "another")
	if err := deliver(ln.Addr().String(), "stranger@example.com", target, again); err != nil {
		t.Fatal(err)
	}
	fat, err := rfc822.WithAttachment("stranger@example.com", target, "fat-subject", "see attachment", "note.bin", "application/octet-stream", bytes.Repeat([]byte("Z"), 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := deliver(ln.Addr().String(), "stranger@example.com", target, fat); err != nil {
		t.Fatal(err)
	}

	mb, err := st.MailboxByAddress(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMessage(mb.ID, mb.Address, parse.Parsed{Sender: "old@example.com", Subject: "yesterday-note", Text: "yesterday"}, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMessage(mb.ID, mb.Address, parse.Parsed{Sender: "old@example.com", Subject: "expired-subject", Text: "old"}, time.Now().Add(-96*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteReceivedBefore(retention.Cutoff(time.Now(), 3)); err != nil {
		t.Fatal(err)
	}

	status, inbox := get(t, client, webSrv.URL+"/m/"+token)
	if status != http.StatusOK {
		t.Fatalf("inbox %d %s", status, inbox)
	}
	if strings.Contains(inbox, "expired-subject") {
		t.Fatal("expired message still listed")
	}
	keptAt := strings.Index(inbox, "kept-subject")
	secondAt := strings.Index(inbox, "second-subject")
	yesterdayAt := strings.Index(inbox, "yesterday-note")
	if keptAt < 0 || secondAt < 0 || yesterdayAt < 0 {
		t.Fatalf("inbox missing messages:\n%s", inbox)
	}
	if secondAt > keptAt || keptAt > yesterdayAt {
		t.Fatalf("order second=%d kept=%d yesterday=%d", secondAt, keptAt, yesterdayAt)
	}

	msgRef := regexp.MustCompile(`/m/`+regexp.QuoteMeta(token)+`/messages/(\d+)`).FindAllStringSubmatch(inbox, -1)
	var sawTruncated bool
	for _, ref := range msgRef {
		status, page := get(t, client, webSrv.URL+"/m/"+token+"/messages/"+ref[1])
		if status != http.StatusOK {
			t.Fatalf("message %s status %d", ref[1], status)
		}
		if strings.Contains(page, "已截断") {
			sawTruncated = true
		}
	}
	if !sawTruncated {
		t.Fatal("attachment over the limit was not marked truncated")
	}

	status, missing := get(t, client, webSrv.URL+"/m/not-a-real-token-value-here")
	if status != http.StatusNotFound || !strings.Contains(missing, "找不到") {
		t.Fatalf("bad token status=%d body=%s", status, missing)
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

func mustCSRF(t *testing.T, body string) string {
	t.Helper()
	const needle = `name="csrf" value="`
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatalf("csrf missing in %s", body)
	}
	rest := body[i+len(needle):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("csrf unterminated")
	}
	return rest[:j]
}

func deliver(addr, from, to string, raw []byte) error {
	var c *netsmtp.Client
	var err error
	for i := 0; i < 50; i++ {
		c, err = netsmtp.Dial(addr)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("inject.local"); err != nil {
		return err
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
