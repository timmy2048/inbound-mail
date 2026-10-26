package workercheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testToken   = "cf-token-super-secret"
	testAccount = "acct-should-not-render"
	testZone    = "0123456789abcdef0123456789abcdef"
)

func TestParseDiagURL(t *testing.T) {
	got, err := ParseDiagURL("  ")
	if err != nil || got != DefaultDiagURL {
		t.Fatalf("default %q %v", got, err)
	}
	got, err = ParseDiagURL("https://example.com/diag?x=1")
	if err != nil || got != "https://example.com/diag?x=1" {
		t.Fatalf("custom %q %v", got, err)
	}
	for _, raw := range []string{"http://user:secret@example.com/diag", "file:///etc/passwd", "notaurl", "ftp://example.com/diag"} {
		if _, err := ParseDiagURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestVerifyCatchAllPointsAtWorker(t *testing.T) {
	var listHits int
	res, api := run(t, script{
		catch:  `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["laijian-cf-email-ingest"]}]}}`,
		onList: func() { listHits++ },
	})
	if !res.OK || res.Headline != "验证成功" || res.Tone != "ok" {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Summary, ScriptName) || !strings.Contains(res.Summary, "诊断地址可访问") {
		t.Fatal(res.Summary)
	}
	diag := finding(t, res, "Worker 诊断")
	if !diag.OK || !strings.Contains(diag.Detail, "健康检查 HTTP 200") || !strings.Contains(diag.Detail, "收信探测 HTTP 422") || !strings.Contains(diag.Detail, "mail.richrich.cc.cd:8787/ingest") {
		t.Fatalf("%+v", diag)
	}
	if listHits != 0 {
		t.Fatalf("list called %d times after a matching catch-all", listHits)
	}
	if api.diagAuth != "" {
		t.Fatalf("diag request carried auth %q", api.diagAuth)
	}
	assertNoSecrets(t, res)
}

func TestVerifyRoutingFailures(t *testing.T) {
	cases := []struct {
		name    string
		catch   string
		list    string
		want    string
		summary string
	}{
		{
			name:    "disabled",
			catch:   `{"success":true,"result":{"enabled":false,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["laijian-cf-email-ingest"]}]}}`,
			want:    "全收未启用",
			summary: "全收未启用。",
		},
		{
			name:    "forward",
			catch:   `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"forward","value":["ops@example.com"]}]}}`,
			want:    "未指向 Worker",
			summary: "未指向 Worker。",
		},
		{
			name:    "other worker",
			catch:   `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["some-other-worker"]}]}}`,
			want:    "指向了别的 Worker",
			summary: "指向了别的 Worker。",
		},
		{
			name:    "drop",
			catch:   `{"success":true,"result":{"enabled":true,"actions":[{"type":"drop"}]}}`,
			want:    "未指向 Worker",
			summary: "未指向 Worker。",
		},
		{
			name:    "string value",
			catch:   `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":"laijian-cf-email-ingest"}]}}`,
			want:    "全收已启用",
			summary: "诊断地址可访问",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := run(t, script{catch: tc.catch, list: tc.list})
			if tc.want == "全收已启用" {
				if !res.OK {
					t.Fatalf("%+v", res)
				}
			} else if res.OK || res.Headline != "验证失败" {
				t.Fatalf("%+v", res)
			}
			if !strings.Contains(finding(t, res, "路由规则").Detail, tc.want) {
				t.Fatalf("%+v", res)
			}
			if !strings.Contains(res.Summary, tc.summary) {
				t.Fatal(res.Summary)
			}
			if strings.Contains(res.Summary+finding(t, res, "路由规则").Detail, "ops@example.com") {
				t.Fatal("forward destination leaked")
			}
			assertNoSecrets(t, res)
		})
	}
}

func TestVerifyMatcherAllOnRuleList(t *testing.T) {
	res, api := run(t, script{
		catchStatus: http.StatusNotFound,
		catch:       `{"success":false}`,
		list: `{"success":true,"result":[
			{"enabled":true,"matchers":[{"type":"literal","value":"a@richrich.cc.cd"}],"actions":[{"type":"worker","value":["laijian-cf-email-ingest"]}]},
			{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["laijian-cf-email-ingest"]}]}
		]}`,
	})
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	if api.listHits == 0 {
		t.Fatal("expected rule list")
	}
}

func TestVerifyDisabledCatchAllYieldsToEnabledAllMatcher(t *testing.T) {
	res, _ := run(t, script{
		catch: `{"success":true,"result":{"enabled":false,"matchers":[{"type":"all"}],"actions":[{"type":"drop"}]}}`,
		list:  `{"success":true,"result":[{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["workers/laijian-cf-email-ingest"]}]}]}`,
	})
	if !res.OK {
		t.Fatalf("%+v", res)
	}
}

func TestVerifyLiteralRuleIsNotCatchAll(t *testing.T) {
	res, _ := run(t, script{
		catch: `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"forward","value":["ops@example.com"]}]}}`,
		list:  `{"success":true,"result":[{"enabled":true,"matchers":[{"type":"literal"}],"actions":[{"type":"worker","value":["laijian-cf-email-ingest"]}]}]}`,
	})
	if res.OK || !strings.Contains(res.Summary, "未指向 Worker") {
		t.Fatalf("%+v", res)
	}
}

func TestVerifyWithoutTokenStillChecksDiag(t *testing.T) {
	res, api := run(t, script{tokenSet: true})
	if res.OK || res.Headline != "验证失败" {
		t.Fatalf("%+v", res)
	}
	if api.cfHits != 0 {
		t.Fatalf("cloudflare was called without a token: %d", api.cfHits)
	}
	if api.diagHits != 1 {
		t.Fatalf("diag hits %d", api.diagHits)
	}
	if !strings.Contains(res.Summary, "CLOUDFLARE_API_TOKEN") || !strings.Contains(res.Summary, "诊断地址可以访问") {
		t.Fatal(res.Summary)
	}
	if !strings.Contains(finding(t, res, "路由规则").Detail, "缺少 API Token") {
		t.Fatal(res.Findings)
	}
	if !finding(t, res, "Worker 诊断").OK {
		t.Fatal(res.Findings)
	}
}

func TestVerifyCloudflareErrors(t *testing.T) {
	t.Run("unauthorized", func(t *testing.T) {
		res, _ := run(t, script{
			zoneStatus: http.StatusUnauthorized,
			zone:       `{"success":false,"errors":[{"message":"` + testToken + `"}]}`,
		})
		if res.Headline != "验证失败" || !strings.Contains(res.Summary, "API Token 无效") {
			t.Fatalf("%+v", res)
		}
		assertNoSecrets(t, res)
	})
	t.Run("forbidden", func(t *testing.T) {
		res, _ := run(t, script{zoneStatus: http.StatusForbidden})
		if !strings.Contains(res.Summary, "无权读取 Email Routing") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("missing zone", func(t *testing.T) {
		res, _ := run(t, script{zone: `{"success":true,"result":[]}`})
		if res.Headline != "验证失败" || !strings.Contains(res.Summary, "没有找到这个域名的 Zone") {
			t.Fatalf("%+v", res)
		}
		if !strings.Contains(finding(t, res, "路由规则").Detail, "CLOUDFLARE_ACCOUNT_ID") {
			t.Fatal(res.Findings)
		}
		assertNoSecrets(t, res)
	})
	t.Run("unavailable", func(t *testing.T) {
		res, api := run(t, script{zoneStatus: http.StatusBadGateway})
		if !errors.Is(api.err, ErrIncomplete) || res.Headline != "暂时查不到" {
			t.Fatalf("err=%v res=%+v", api.err, res)
		}
		if !finding(t, res, "Worker 诊断").OK {
			t.Fatal("diag should still be reported")
		}
		if !strings.Contains(res.Summary, "诊断地址可以访问") {
			t.Fatal(res.Summary)
		}
	})
}

func TestVerifyDiagOutcomes(t *testing.T) {
	okCatch := `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["laijian-cf-email-ingest"]}]}}`
	t.Run("not ready", func(t *testing.T) {
		res, _ := run(t, script{catch: okCatch, diag: `{"ok":false,"healthStatus":500,"ingestStatus":401}`})
		if res.OK || res.Headline != "验证失败" || !strings.Contains(res.Summary, "诊断没有通过") {
			t.Fatalf("%+v", res)
		}
		if !strings.Contains(finding(t, res, "Worker 诊断").Detail, "未就绪") {
			t.Fatal(res.Findings)
		}
	})
	t.Run("gateway", func(t *testing.T) {
		res, api := run(t, script{catch: okCatch, diagStatus: http.StatusBadGateway})
		if !errors.Is(api.err, ErrIncomplete) || res.Headline != "暂时查不到" {
			t.Fatalf("err=%v %+v", api.err, res)
		}
		if !strings.Contains(res.Summary, "暂时连不上") {
			t.Fatal(res.Summary)
		}
	})
	t.Run("missing", func(t *testing.T) {
		res, api := run(t, script{catch: okCatch, diagStatus: http.StatusNotFound})
		if api.err != nil || res.Headline != "验证失败" {
			t.Fatalf("err=%v %+v", api.err, res)
		}
	})
}

func TestVerifyTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	res, err := (Service{Config{
		Token:   testToken,
		DiagURL: srv.URL + "/_diag",
		APIBase: srv.URL + "/client/v4",
		HTTP:    srv.Client(),
	}}).Verify(ctx, "richrich.cc.cd")
	if !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	if res.Headline != "暂时查不到" || !strings.Contains(res.Summary, "请再试一次") {
		t.Fatalf("%+v", res)
	}
	assertNoSecrets(t, res)
}

func TestVerifyTokenMissingAndDiagDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	res, _ := (Service{Config{
		DiagURL: srv.URL + "/_diag",
		APIBase: srv.URL + "/client/v4",
		HTTP:    srv.Client(),
	}}).Verify(ctx, "richrich.cc.cd")
	if res.Headline != "验证失败" || !strings.Contains(res.Summary, "CLOUDFLARE_API_TOKEN") || !strings.Contains(res.Summary, "暂时连不上") {
		t.Fatalf("%+v", res)
	}
}

func TestVerifyRedactsTokenUsedAsWorkerName(t *testing.T) {
	res, _ := run(t, script{
		catch: `{"success":true,"result":{"enabled":true,"matchers":[{"type":"all"}],"actions":[{"type":"worker","value":["` + testToken + `"]}]}}`,
	})
	if strings.Contains(res.Summary+finding(t, res, "路由规则").Detail, testToken) {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(finding(t, res, "路由规则").Detail, "指向了别的 Worker") && !strings.Contains(finding(t, res, "路由规则").Detail, "未指向 Worker") {
		t.Fatalf("%+v", res)
	}
}

type script struct {
	token       string
	tokenSet    bool
	zoneStatus  int
	zone        string
	catchStatus int
	catch       string
	listStatus  int
	list        string
	diagStatus  int
	diag        string
	onList      func()
}

type captured struct {
	diagAuth string
	diagHits int
	cfHits   int
	listHits int
	account  string
	err      error
}

func run(t *testing.T, sc script) (Result, captured) {
	t.Helper()
	token := testToken
	if sc.tokenSet {
		token = sc.token
	}
	capd := captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_diag") {
			capd.diagHits++
			capd.diagAuth = r.Header.Get("Authorization")
			status := sc.diagStatus
			if status == 0 {
				status = http.StatusOK
			}
			body := sc.diag
			if body == "" && status == http.StatusOK {
				body = `{"ok":true,"healthStatus":200,"ingestStatus":422,"ingestHost":"mail.richrich.cc.cd","ingestPort":"8787","ingestPath":"/ingest"}`
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		capd.cfHits++
		if got := r.Header.Get("Authorization"); token != "" && got != "Bearer "+token {
			t.Errorf("auth on %s = %q", r.URL.Path, got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/catch_all"):
			writeStatus(w, sc.catchStatus, sc.catch)
		case strings.Contains(r.URL.Path, "/email/routing/rules"):
			capd.listHits++
			if sc.onList != nil {
				sc.onList()
			}
			body := sc.list
			if body == "" && sc.listStatus == 0 {
				body = `{"success":true,"result":[]}`
			}
			writeStatus(w, sc.listStatus, body)
		case strings.HasSuffix(r.URL.Path, "/zones"):
			capd.account = r.URL.Query().Get("account.id")
			body := sc.zone
			if body == "" && sc.zoneStatus == 0 {
				body = `{"success":true,"result":[{"id":"` + testZone + `","name":"richrich.cc.cd","status":"active"}]}`
			}
			writeStatus(w, sc.zoneStatus, body)
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	res, err := (Service{Config{
		Token:     token,
		AccountID: testAccount,
		DiagURL:   srv.URL + "/_diag",
		APIBase:   srv.URL + "/client/v4",
		HTTP:      srv.Client(),
	}}).Verify(context.Background(), "Richrich.CC.CD.")
	if sc.zoneStatus >= 500 || sc.diagStatus >= 500 || sc.zoneStatus == http.StatusTooManyRequests {
		if !errors.Is(err, ErrIncomplete) {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if capd.account != "" && capd.account != testAccount {
		t.Fatalf("account query %s", capd.account)
	}
	capd.err = err
	return res, capd
}

func writeStatus(w http.ResponseWriter, status int, body string) {
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func finding(t *testing.T, res Result, title string) Finding {
	t.Helper()
	for _, f := range res.Findings {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("missing finding %s in %+v", title, res.Findings)
	return Finding{}
}

func assertNoSecrets(t *testing.T, res Result) {
	t.Helper()
	blob := res.Headline + "\n" + res.Summary
	for _, f := range res.Findings {
		blob += "\n" + f.Title + "\n" + f.Detail
	}
	if strings.Contains(blob, testToken) || strings.Contains(blob, testAccount) {
		t.Fatalf("secret leaked:\n%s", blob)
	}
}
