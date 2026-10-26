package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"laijian/internal/config"
	"laijian/internal/parse"
	"laijian/internal/store"
)

func TestLinkifyTextMakesHTTPLinks(t *testing.T) {
	got := string(linkifyText("F5加速 <http://fast520.cc.cd>"))
	want := `F5加速 &lt;<a href="http://fast520.cc.cd" rel="noopener noreferrer" target="_blank">http://fast520.cc.cd</a>&gt;`
	if got != want {
		t.Fatalf("got %s", got)
	}

	query := string(linkifyText("打开 https://example.com/a?x=1&y=2."))
	if !strings.Contains(query, `href="https://example.com/a?x=1&amp;y=2"`) || !strings.Contains(query, "</a>.") {
		t.Fatal(query)
	}

	paren := string(linkifyText("https://en.wikipedia.org/wiki/URL_(disambiguation)"))
	if !strings.Contains(paren, `href="https://en.wikipedia.org/wiki/URL_(disambiguation)"`) {
		t.Fatal(paren)
	}
}

func TestLinkifyTextLeavesDangerousSchemesEscaped(t *testing.T) {
	got := string(linkifyText("see javascript:alert(1) and <script>alert(1)</script> nothttp://example.com"))
	if strings.Contains(got, "<script>") || strings.Contains(got, "href=") {
		t.Fatalf("unexpected markup: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "javascript:alert(1)") || !strings.Contains(got, "nothttp://example.com") {
		t.Fatal(got)
	}
}

func TestRewriteMailHTMLKeepsSafeAnchors(t *testing.T) {
	src := `<p>F5加速 <a href="http://fast520.cc.cd" onclick="steal()">打开</a> <a href="javascript:alert(1)">bad</a><img src="https://tracker.example/p.png" alt="dot"></p>`
	got := rewriteMailHTML(src)
	for _, want := range []string{
		`href="http://fast520.cc.cd"`,
		`rel="noopener noreferrer"`,
		`target="_blank"`,
		`src="https://tracker.example/p.png"`,
		"打开",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "javascript:") || strings.Contains(got, "onclick") {
		t.Fatalf("unsafe html kept: %s", got)
	}
}

func TestMessagePageAutolinksText(t *testing.T) {
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
	boxes, err := st.CreateMailboxes(d.ID, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveMessage(boxes[0].ID, boxes[0].Address, parse.Parsed{
		Sender:  "news@example.com",
		Subject: "加速",
		Text:    "F5加速 <http://fast520.cc.cd>\n<script>alert(1)</script>",
		HTML:    `<p><a href="http://fast520.cc.cd">打开</a><img src="https://tracker.example/p.png"></p>`,
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

	pageURL := webSrv.URL + "/m/" + boxes[0].Token + "/messages/" + strconv.FormatInt(id, 10)
	status, body := get(t, http.DefaultClient, pageURL)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{
		"F5加速",
		`href="http://fast520.cc.cd"`,
		`rel="noopener noreferrer"`,
		`target="_blank"`,
		"&lt;script&gt;",
		"外链图片不会加载",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("script tag was not escaped")
	}

	res, err := http.Get(pageURL + "/html")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	htmlBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src data:") || strings.Contains(csp, "img-src http") || strings.Contains(csp, "img-src *") {
		t.Fatalf("csp %s", csp)
	}
	htmlText := string(htmlBody)
	if !strings.Contains(htmlText, `rel="noopener noreferrer"`) || !strings.Contains(htmlText, `target="_blank"`) {
		t.Fatal(htmlText)
	}
	if !strings.Contains(htmlText, `src="https://tracker.example/p.png"`) {
		t.Fatal(htmlText)
	}
}
