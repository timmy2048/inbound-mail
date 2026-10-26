package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"laijian/internal/config"
	"laijian/internal/store"
	"laijian/internal/workercheck"
)

type forbidWorker struct{ t *testing.T }

func (f forbidWorker) Verify(context.Context, string) (workercheck.Result, error) {
	f.t.Fatal("worker verify should not run")
	return workercheck.Result{}, nil
}

func TestAdminCanVerifyWorker(t *testing.T) {
	dir := t.TempDir()
	const secret = "cf-token-super-secret"
	cfg := config.Config{
		HTTPAddr:            "127.0.0.1:0",
		SMTPAddr:            "127.0.0.1:0",
		SMTPDomain:          "localhost",
		PublicBaseURL:       "http://127.0.0.1:8787",
		AdminPassword:       "test-password-please",
		RetentionDays:       3,
		AttachmentMaxBytes:  64,
		MessageMaxBytes:     1 << 20,
		MailDataDir:         dir,
		DatabasePath:        filepath.Join(dir, "mail.db"),
		ServerPublicIP:      "1.2.3.4",
		CloudflareAPIToken:  secret,
		CloudflareAccountID: "acct-should-not-render",
		WorkerDiagURL:       "https://example.test/_diag?token=" + secret,
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
	fake := &scriptedWorker{result: workercheck.Result{
		OK:       true,
		Tone:     "ok",
		Headline: "验证成功",
		Summary:  "全收已指向 laijian-cf-email-ingest，诊断地址可访问。",
		Findings: []workercheck.Finding{
			{OK: true, Title: "路由规则", Detail: "全收已启用，动作是 Worker laijian-cf-email-ingest。"},
			{OK: true, Title: "Worker 诊断", Detail: "可访问。健康检查 HTTP 200。"},
		},
	}}
	srv.SetWorkerVerifier(fake)
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
		t.Fatalf("add domain %d", status)
	}
	if fake.calls != 0 {
		t.Fatal("opening the domain page checked the worker")
	}
	if strings.Contains(body, secret) || strings.Contains(body, "acct-should-not-render") || strings.Contains(body, "example.test/_diag") {
		t.Fatal("domain page exposed worker configuration")
	}
	if strings.Contains(body, "路由规则检查需要 CLOUDFLARE_API_TOKEN") {
		t.Fatal("configured token should not be described as missing")
	}
	if !strings.Contains(body, "核对全收是否指向 laijian-cf-email-ingest") {
		t.Fatal(body)
	}
	workerAt := strings.Index(body, ">验证 Worker<")
	manualAt := strings.Index(body, "需要手动核对时展开")
	mxAt := strings.Index(body, "route1.mx.cloudflare.net")
	if workerAt < 0 || manualAt < 0 || mxAt < 0 || !(workerAt < manualAt && manualAt < mxAt) {
		t.Fatalf("order worker=%d manual=%d mx=%d", workerAt, manualAt, mxAt)
	}
	if strings.Contains(body, `<details class="manual" open`) {
		t.Fatal("manual MX details should stay closed")
	}
	csrf = mustCSRF(t, body)
	action := regexp.MustCompile(`/admin/domains/\d+/verify-worker`).FindString(body)
	if action == "" {
		t.Fatal("worker action missing")
	}
	status, _ = postForm(t, http.DefaultClient, webSrv.URL+action, url.Values{"csrf": {csrf}})
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous status %d", status)
	}
	if fake.calls != 0 {
		t.Fatal("anonymous post checked the worker")
	}
	status, loc := postNoFollow(t, nofollow, webSrv.URL+action, url.Values{"csrf": {csrf}})
	if status != http.StatusSeeOther || !strings.Contains(loc, "checked=worker") {
		t.Fatalf("redirect status=%d loc=%s", status, loc)
	}
	if fake.calls != 1 || fake.domain != "richrich.cc.cd" {
		t.Fatalf("calls=%d domain=%s", fake.calls, fake.domain)
	}
	status, body = get(t, client, absolute(webSrv.URL, loc))
	if status != http.StatusOK {
		t.Fatal(status)
	}
	for _, want := range []string{"验证成功", "全收已指向 laijian-cf-email-ingest", "健康检查 HTTP 200", "通过", "检查于", "北京时间"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, secret) {
		t.Fatal("result page exposed the token")
	}
	before := fake.calls
	status, body = get(t, client, absolute(webSrv.URL, loc))
	if status != http.StatusOK || !strings.Contains(body, "验证成功") || fake.calls != before {
		t.Fatalf("revisit status=%d calls=%d", status, fake.calls)
	}

	fake.result = workercheck.Result{
		Tone:     "error",
		Headline: "验证失败",
		Summary:  "指向了别的 Worker。",
		Findings: []workercheck.Finding{{Title: "路由规则", Detail: "指向了别的 Worker：other-worker。"}},
	}
	fake.err = nil
	csrf = mustCSRF(t, body)
	status, body = postForm(t, client, webSrv.URL+action, url.Values{"csrf": {csrf}})
	if status != http.StatusOK || !strings.Contains(body, "验证失败") || !strings.Contains(body, "指向了别的 Worker") || strings.Contains(body, "验证成功") {
		t.Fatalf("fail body:\n%s", body)
	}

	status, _ = get(t, client, webSrv.URL+"/admin/domains/99999/verify-worker")
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Fatalf("get verify-worker %d", status)
	}
	status, body = postForm(t, client, webSrv.URL+"/admin/domains/99999/verify-worker", url.Values{"csrf": {csrf}})
	if status != http.StatusNotFound || !strings.Contains(body, "找不到") {
		t.Fatalf("missing domain status=%d body=%s", status, body)
	}
}

type scriptedWorker struct {
	result workercheck.Result
	err    error
	calls  int
	domain string
}

func (s *scriptedWorker) Verify(_ context.Context, domain string) (workercheck.Result, error) {
	s.calls++
	s.domain = domain
	return s.result, s.err
}
