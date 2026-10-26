// Package workercheck verifies that Cloudflare Email Routing sends a domain
// to the ingest Worker, and that the Worker's diag endpoint is reachable.
// Results are safe to render: tokens and account ids are never included.
package workercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

const (
	// ScriptName is the Worker Email Routing must call.
	ScriptName = "laijian-cf-email-ingest"
	// DefaultDiagURL is the deployed Worker health endpoint.
	DefaultDiagURL = "https://laijian-cf-email-ingest.timmy2048.workers.dev/_diag"
	apiRoot        = "https://api.cloudflare.com/client/v4"
	maxBody        = 1 << 20
	maxDiagBody    = 32 << 10
)

// ErrIncomplete means a lookup timed out or Cloudflare did not answer.
// The Result is still safe to show. The error text never includes secrets or URLs.
var ErrIncomplete = errors.New("worker check incomplete")

// Config is the check input. Empty DiagURL uses DefaultDiagURL.
type Config struct {
	Token     string
	AccountID string
	DiagURL   string
	// APIBase overrides the Cloudflare API root. Tests set this.
	APIBase string
	// HTTP overrides the client. Tests set this.
	HTTP *http.Client
}

// Service checks one domain. It implements the admin verifier.
type Service struct{ Config }

// Finding is one line in the verification result.
type Finding struct {
	OK     bool
	Title  string
	Detail string
}

// Result is the outcome of 验证 Worker.
// OK is true only when the routing rule and the diag endpoint both pass.
type Result struct {
	OK       bool
	Tone     string
	Headline string
	Summary  string
	Findings []Finding
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeFail
	outcomeUnknown
)

// ParseDiagURL accepts an empty string (meaning the default) or an http(s) URL
// without userinfo. The error does not echo the raw value.
func ParseDiagURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultDiagURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", errors.New("invalid diag url")
	}
	u.Fragment = ""
	return u.String(), nil
}

// Verify checks the domain's catch-all (or matcher type=all) and the diag URL.
func (s Service) Verify(ctx context.Context, domain string) (Result, error) {
	domain = normDomain(domain)
	client := s.client()
	base := strings.TrimRight(strings.TrimSpace(s.APIBase), "/")
	if base == "" {
		base = apiRoot
	}
	token := strings.TrimSpace(s.Token)
	account := strings.TrimSpace(s.AccountID)

	var (
		routing, diag Finding
		rs, ds        outcome
		rerr, derr    error
	)
	done := make(chan struct{}, 2)
	go func() {
		routing, rs, rerr = s.checkRouting(ctx, client, base, domain, token, account)
		done <- struct{}{}
	}()
	go func() {
		diag, ds, derr = s.checkDiag(ctx, client, s.DiagURL)
		done <- struct{}{}
	}()
	<-done
	<-done

	res := combine(routing, diag, rs, ds)
	res = s.scrub(res)
	if rerr != nil || derr != nil {
		return res, ErrIncomplete
	}
	return res, nil
}

func (s Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("redirect")
			}
			req.Header.Del("Authorization")
			return nil
		},
	}
}

func (s Service) checkRouting(ctx context.Context, client *http.Client, base, domain, token, account string) (Finding, outcome, error) {
	title := "路由规则"
	if domain == "" {
		return Finding{Title: title, Detail: "域名无效。"}, outcomeFail, nil
	}
	if token == "" {
		return Finding{Title: title, Detail: "缺少 API Token。设置 CLOUDFLARE_API_TOKEN 后才能核对全收规则。"}, outcomeFail, nil
	}
	zoneID, find, st, err := s.lookupZone(ctx, client, base, domain, token, account)
	if st != outcomeOK {
		return find, st, err
	}
	catch, _, find, st, err := s.getCatchAll(ctx, client, base, zoneID, token)
	if st == outcomeFail {
		return find, st, err
	}
	var rules []rule
	if catch != nil {
		rules = append(rules, *catch)
		if catch.isAll() && catch.enabledRule() && catch.targetsScript() {
			f, o := judge(rules)
			return f, o, nil
		}
	}
	listed, listSt, listDetail, listErr := s.listRules(ctx, client, base, zoneID, token)
	if listSt == outcomeFail && catch == nil {
		if listDetail == "" {
			listDetail = "这次没能读到 Email Routing 规则。"
		}
		return Finding{Title: title, Detail: listDetail}, listSt, nil
	}
	rules = append(rules, listed...)
	if len(rules) == 0 {
		if st == outcomeUnknown || listSt == outcomeUnknown {
			detail := "这次没能读到 Email Routing 规则。"
			if isTimeout(err) || isTimeout(listErr) {
				detail = "Cloudflare 查询超时。"
			}
			if err == nil {
				err = listErr
			}
			return Finding{Title: title, Detail: detail}, outcomeUnknown, err
		}
		return Finding{Title: title, Detail: "全收未启用。"}, outcomeFail, nil
	}
	if listSt == outcomeUnknown && catch == nil {
		detail := "这次没能读到 Email Routing 规则。"
		if isTimeout(listErr) {
			detail = "Cloudflare 查询超时。"
		}
		return Finding{Title: title, Detail: detail}, outcomeUnknown, listErr
	}
	f, o := judge(rules)
	return f, o, nil
}

func (s Service) lookupZone(ctx context.Context, client *http.Client, base, domain, token, account string) (string, Finding, outcome, error) {
	title := "路由规则"
	u, err := url.Parse(base + "/zones")
	if err != nil {
		return "", Finding{Title: title, Detail: "这次没能读到 Email Routing 规则。"}, outcomeUnknown, ErrIncomplete
	}
	q := u.Query()
	q.Set("name", domain)
	q.Set("per_page", "50")
	if account != "" {
		q.Set("account.id", account)
	}
	u.RawQuery = q.Encode()
	status, body, callErr := s.get(ctx, client, token, u.String())
	if callErr != nil {
		detail := "这次没能读到 Email Routing 规则。"
		if isTimeout(callErr) {
			detail = "Cloudflare 查询超时。"
		}
		return "", Finding{Title: title, Detail: detail}, outcomeUnknown, callErr
	}
	st, detail := httpOutcome(status)
	if st != outcomeOK {
		if detail == "" {
			detail = "这次没能读到 Email Routing 规则。"
		}
		var ret error
		if st == outcomeUnknown {
			ret = ErrIncomplete
		}
		return "", Finding{Title: title, Detail: detail}, st, ret
	}
	id, ok := zoneID(body, domain)
	if !ok {
		detail := "没有找到这个域名的 Zone。"
		if account != "" {
			detail = "没有找到这个域名的 Zone。已按 CLOUDFLARE_ACCOUNT_ID 过滤。"
		}
		return "", Finding{Title: title, Detail: detail}, outcomeFail, nil
	}
	return id, Finding{}, outcomeOK, nil
}

func (s Service) getCatchAll(ctx context.Context, client *http.Client, base, zoneID, token string) (*rule, bool, Finding, outcome, error) {
	title := "路由规则"
	status, body, callErr := s.get(ctx, client, token, base+"/zones/"+zoneID+"/email/routing/rules/catch_all")
	if callErr != nil {
		detail := "这次没能读到 Email Routing 规则。"
		if isTimeout(callErr) {
			detail = "Cloudflare 查询超时。"
		}
		return nil, false, Finding{Title: title, Detail: detail}, outcomeUnknown, callErr
	}
	if status == http.StatusNotFound {
		return nil, true, Finding{}, outcomeOK, nil
	}
	st, detail := httpOutcome(status)
	if st != outcomeOK {
		if detail == "" {
			detail = "这次没能读到 Email Routing 规则。"
		}
		var ret error
		if st == outcomeUnknown {
			ret = ErrIncomplete
		}
		return nil, false, Finding{Title: title, Detail: detail}, st, ret
	}
	raw, err := cfResult(body)
	if err != nil {
		return nil, false, Finding{Title: title, Detail: "这次没能读到 Email Routing 规则。"}, outcomeUnknown, ErrIncomplete
	}
	if len(bytesTrim(raw)) == 0 {
		return nil, true, Finding{}, outcomeOK, nil
	}
	rl, err := decodeRule(raw, true)
	if err != nil {
		return nil, false, Finding{Title: title, Detail: "这次没能读到 Email Routing 规则。"}, outcomeUnknown, ErrIncomplete
	}
	return &rl, false, Finding{}, outcomeOK, nil
}

func (s Service) listRules(ctx context.Context, client *http.Client, base, zoneID, token string) ([]rule, outcome, string, error) {
	var all []rule
	for page := 1; page <= 3; page++ {
		endpoint := fmt.Sprintf("%s/zones/%s/email/routing/rules?per_page=50&page=%d", base, zoneID, page)
		status, body, callErr := s.get(ctx, client, token, endpoint)
		if callErr != nil {
			if page == 1 {
				return nil, outcomeUnknown, "", callErr
			}
			return all, outcomeOK, "", nil
		}
		if status == http.StatusNotFound && page == 1 {
			return nil, outcomeOK, "", nil
		}
		st, detail := httpOutcome(status)
		if st != outcomeOK {
			if page == 1 {
				if st == outcomeUnknown {
					return nil, outcomeUnknown, detail, ErrIncomplete
				}
				return nil, st, detail, nil
			}
			return all, outcomeOK, "", nil
		}
		batch, pages, err := decodeRulePage(body)
		if err != nil {
			if page == 1 {
				return nil, outcomeUnknown, "", ErrIncomplete
			}
			return all, outcomeOK, "", nil
		}
		all = append(all, batch...)
		if pages <= page || len(batch) == 0 {
			break
		}
	}
	return all, outcomeOK, "", nil
}

func (s Service) get(ctx context.Context, client *http.Client, token, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, ErrIncomplete
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return 0, nil, errTimeout
		}
		return 0, nil, ErrIncomplete
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return res.StatusCode, nil, ErrIncomplete
	}
	return res.StatusCode, body, nil
}

func (s Service) checkDiag(ctx context.Context, client *http.Client, rawURL string) (Finding, outcome, error) {
	title := "Worker 诊断"
	parsed, err := ParseDiagURL(rawURL)
	if err != nil {
		return Finding{Title: title, Detail: "诊断地址无效。"}, outcomeFail, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed, nil)
	if err != nil {
		return Finding{Title: title, Detail: "诊断地址无效。"}, outcomeFail, nil
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return Finding{Title: title, Detail: "暂时连不上。"}, outcomeUnknown, ErrIncomplete
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxDiagBody))
	if err != nil {
		return Finding{Title: title, Detail: "暂时连不上。"}, outcomeUnknown, ErrIncomplete
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
			return Finding{Title: title, Detail: fmt.Sprintf("暂时没有返回（HTTP %d）。", res.StatusCode)}, outcomeUnknown, ErrIncomplete
		}
		return Finding{Title: title, Detail: fmt.Sprintf("返回 HTTP %d。", res.StatusCode)}, outcomeFail, nil
	}
	found := interpretDiag(body)
	if found.OK {
		return found, outcomeOK, nil
	}
	return found, outcomeFail, nil
}

func interpretDiag(body []byte) Finding {
	title := "Worker 诊断"
	var d struct {
		OK           *bool      `json:"ok"`
		HealthStatus *int       `json:"healthStatus"`
		IngestStatus *int       `json:"ingestStatus"`
		IngestHost   string     `json:"ingestHost"`
		IngestPort   flexString `json:"ingestPort"`
		IngestPath   string     `json:"ingestPath"`
	}
	if json.Unmarshal(body, &d) != nil || (d.OK == nil && d.HealthStatus == nil && d.IngestStatus == nil) {
		return Finding{OK: true, Title: title, Detail: "可访问。"}
	}
	var parts []string
	if code, ok := httpCode(d.HealthStatus); ok {
		parts = append(parts, fmt.Sprintf("健康检查 HTTP %d", code))
	}
	if code, ok := httpCode(d.IngestStatus); ok {
		parts = append(parts, fmt.Sprintf("收信探测 HTTP %d", code))
	}
	if target := formatTarget(d.IngestHost, string(d.IngestPort), d.IngestPath); target != "" {
		parts = append(parts, "目标 "+target)
	}
	extra := strings.Join(parts, "，")
	if d.OK != nil && *d.OK {
		detail := "可访问。"
		if extra != "" {
			detail += extra + "。"
		}
		return Finding{OK: true, Title: title, Detail: detail}
	}
	if d.OK != nil && !*d.OK {
		detail := "诊断显示未就绪。"
		if extra != "" {
			detail += extra + "。"
		}
		return Finding{Title: title, Detail: detail}
	}
	if code, ok := httpCode(d.HealthStatus); ok && code >= 200 && code < 300 {
		return Finding{OK: true, Title: title, Detail: "可访问。" + extra + "。"}
	}
	if extra == "" {
		extra = "可访问。"
		return Finding{OK: true, Title: title, Detail: extra}
	}
	return Finding{Title: title, Detail: extra + "。"}
}

func combine(routing, diag Finding, rs, ds outcome) Result {
	findings := []Finding{routing, diag}
	switch {
	case rs == outcomeFail || ds == outcomeFail:
		return Result{Tone: "error", Headline: "验证失败", Summary: summary(routing, diag, rs, ds), Findings: findings}
	case rs == outcomeUnknown || ds == outcomeUnknown:
		return Result{Tone: "warn", Headline: "暂时查不到", Summary: summary(routing, diag, rs, ds), Findings: findings}
	default:
		return Result{OK: true, Tone: "ok", Headline: "验证成功", Summary: summary(routing, diag, rs, ds), Findings: findings}
	}
}

func summary(routing, diag Finding, rs, ds outcome) string {
	if strings.Contains(routing.Detail, "缺少 API Token") {
		switch {
		case diag.OK:
			return "路由规则检查需要 CLOUDFLARE_API_TOKEN。诊断地址可以访问。"
		case ds == outcomeUnknown:
			return "路由规则检查需要 CLOUDFLARE_API_TOKEN。诊断地址暂时连不上。"
		default:
			return "路由规则检查需要 CLOUDFLARE_API_TOKEN。诊断没有通过。"
		}
	}
	if rs == outcomeFail {
		return shortReason(routing.Detail)
	}
	if rs == outcomeUnknown {
		if diag.OK {
			return "这次没能向 Cloudflare 查到路由规则。诊断地址可以访问。"
		}
		return "这次没能完成检查。请再试一次。"
	}
	if diag.OK {
		return "全收已指向 " + ScriptName + "，诊断地址可访问。"
	}
	if ds == outcomeUnknown {
		return "全收已指向 Worker，但诊断地址暂时连不上。"
	}
	return "全收已指向 Worker，但诊断没有通过。"
}

func shortReason(detail string) string {
	for _, s := range []string{
		"全收未启用",
		"未指向 Worker",
		"指向了别的 Worker",
		"没有找到这个域名的 Zone",
		"API Token 无效",
		"API Token 无权读取 Email Routing",
		"域名无效",
	} {
		if strings.Contains(detail, s) {
			return s + "。"
		}
	}
	if detail == "" {
		return "验证失败。"
	}
	return detail
}

type rule struct {
	enabled   bool
	fromCatch bool
	matchers  []string
	actions   []action
}

type action struct {
	kind   string
	values []string
}

func (r rule) enabledRule() bool { return r.enabled }

func (r rule) isAll() bool {
	if r.fromCatch && len(r.matchers) == 0 {
		return true
	}
	for _, m := range r.matchers {
		if strings.EqualFold(m, "all") {
			return true
		}
	}
	return false
}

func (r rule) targetsScript() bool {
	for _, a := range r.actions {
		if !strings.EqualFold(a.kind, "worker") {
			continue
		}
		for _, v := range a.values {
			if strings.Contains(strings.ToLower(v), ScriptName) {
				return true
			}
		}
	}
	return false
}

func judge(rules []rule) (Finding, outcome) {
	title := "路由规则"
	var all []rule
	for _, r := range rules {
		if r.isAll() {
			all = append(all, r)
		}
	}
	for _, r := range all {
		if r.enabledRule() && r.targetsScript() {
			return Finding{OK: true, Title: title, Detail: "全收已启用，动作是 Worker " + ScriptName + "。"}, outcomeOK
		}
	}
	if len(all) == 0 {
		return Finding{Title: title, Detail: "全收未启用。"}, outcomeFail
	}
	var chosen *rule
	for i := range all {
		if all[i].enabledRule() {
			chosen = &all[i]
			break
		}
	}
	if chosen == nil {
		return Finding{Title: title, Detail: "全收未启用。"}, outcomeFail
	}
	if names := workerNames(*chosen); len(names) > 0 {
		return Finding{Title: title, Detail: "指向了别的 Worker：" + strings.Join(names, "、") + "。"}, outcomeFail
	}
	if phrase := actionPhrase(*chosen); phrase != "" {
		return Finding{Title: title, Detail: "未指向 Worker。当前是" + phrase + "。"}, outcomeFail
	}
	return Finding{Title: title, Detail: "未指向 Worker。"}, outcomeFail
}

func workerNames(r rule) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range r.actions {
		if !strings.EqualFold(a.kind, "worker") {
			continue
		}
		for _, v := range a.values {
			name := cleanToken(v)
			if name == "" || seen[name] || strings.Contains(strings.ToLower(name), ScriptName) {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func actionPhrase(r rule) string {
	for _, a := range r.actions {
		switch strings.ToLower(a.kind) {
		case "worker":
			return ""
		case "forward":
			return "转发"
		case "drop":
			return "丢弃"
		case "":
			continue
		default:
			if name := cleanToken(a.kind); name != "" {
				return name
			}
		}
	}
	return ""
}

func cleanToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 80 {
		return ""
	}
	for _, r := range s {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.') {
			return ""
		}
	}
	return s
}

func httpOutcome(status int) (outcome, string) {
	switch {
	case status == http.StatusUnauthorized:
		return outcomeFail, "API Token 无效。"
	case status == http.StatusForbidden:
		return outcomeFail, "API Token 无权读取 Email Routing。"
	case status == http.StatusTooManyRequests || status >= 500:
		return outcomeUnknown, "Cloudflare 暂时没有返回规则。"
	case status >= 200 && status < 300:
		return outcomeOK, ""
	case status == http.StatusNotFound:
		return outcomeFail, "没有找到这个域名的 Zone。"
	default:
		return outcomeFail, fmt.Sprintf("Cloudflare 返回 HTTP %d。", status)
	}
}

func zoneID(body []byte, domain string) (string, bool) {
	raw, err := cfResult(body)
	if err != nil || len(bytesTrim(raw)) == 0 {
		return "", false
	}
	var zones []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &zones) != nil {
		return "", false
	}
	domain = normDomain(domain)
	var id string
	for _, z := range zones {
		if normDomain(z.Name) != domain || !validID(z.ID) {
			continue
		}
		id = z.ID
		if strings.EqualFold(z.Status, "active") {
			return id, true
		}
	}
	return id, id != ""
}

func cfResult(body []byte) (json.RawMessage, error) {
	body = bytesTrim(body)
	if len(body) == 0 {
		return nil, errParse
	}
	var env struct {
		Success *bool           `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil, errParse
	}
	if env.Success != nil {
		if !*env.Success {
			return nil, errParse
		}
		if len(bytesTrim(env.Result)) == 0 || string(bytesTrim(env.Result)) == "null" {
			return nil, nil
		}
		return env.Result, nil
	}
	return body, nil
}

func decodeRule(raw json.RawMessage, fromCatch bool) (rule, error) {
	var in struct {
		Enabled  *bool `json:"enabled"`
		Matchers []struct {
			Type string `json:"type"`
		} `json:"matchers"`
		Actions []struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"actions"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return rule{}, errParse
	}
	rl := rule{enabled: true, fromCatch: fromCatch}
	if in.Enabled != nil {
		rl.enabled = *in.Enabled
	}
	for _, m := range in.Matchers {
		rl.matchers = append(rl.matchers, m.Type)
	}
	for _, a := range in.Actions {
		rl.actions = append(rl.actions, action{kind: a.Type, values: actionValues(a.Value)})
	}
	return rl, nil
}

func decodeRulePage(body []byte) ([]rule, int, error) {
	var env struct {
		Success    *bool           `json:"success"`
		Result     json.RawMessage `json:"result"`
		ResultInfo struct {
			TotalPages int `json:"total_pages"`
		} `json:"result_info"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil, 0, errParse
	}
	raw := env.Result
	if env.Success == nil {
		raw = body
	} else if !*env.Success {
		return nil, 0, errParse
	}
	raw = bytesTrim(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, env.ResultInfo.TotalPages, nil
	}
	if raw[0] == '{' {
		rl, err := decodeRule(raw, false)
		if err != nil {
			return nil, 0, err
		}
		return []rule{rl}, env.ResultInfo.TotalPages, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, 0, errParse
	}
	out := make([]rule, 0, len(items))
	for _, item := range items {
		rl, err := decodeRule(item, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rl)
	}
	return out, env.ResultInfo.TotalPages, nil
}

func actionValues(raw json.RawMessage) []string {
	raw = bytesTrim(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []string{s}
		}
		return nil
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	return nil
}

func formatTarget(host, port, path string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if !validHost(host) {
		return ""
	}
	target := host
	port = strings.TrimSpace(port)
	if port != "" && validPort(port) {
		target += ":" + port
	}
	path = strings.TrimSpace(path)
	if validPath(path) {
		target += path
	}
	return target
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 || strings.Contains(host, "..") {
		return false
	}
	for _, r := range host {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func validPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validPath(path string) bool {
	if path == "" || len(path) > 80 || !strings.HasPrefix(path, "/") {
		return false
	}
	for _, r := range path {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '/' && r != '.' && r != '_' && r != '-' && r != '~' {
			return false
		}
	}
	return true
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func normDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func httpCode(n *int) (int, bool) {
	if n == nil || *n < 100 || *n > 599 {
		return 0, false
	}
	return *n, true
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytesTrim(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(string(b))
	return nil
}

func (s Service) scrub(res Result) Result {
	res.Headline = s.redact(res.Headline)
	res.Summary = s.redact(res.Summary)
	for i := range res.Findings {
		res.Findings[i].Title = s.redact(res.Findings[i].Title)
		res.Findings[i].Detail = s.redact(res.Findings[i].Detail)
	}
	return res
}

func (s Service) redact(in string) string {
	out := in
	if tok := strings.TrimSpace(s.Token); len(tok) >= 8 && tok != ScriptName {
		out = strings.ReplaceAll(out, tok, "")
	}
	if acct := strings.TrimSpace(s.AccountID); len(acct) >= 8 {
		out = strings.ReplaceAll(out, acct, "")
	}
	return out
}

var (
	errParse   = errors.New("parse")
	errTimeout = errors.New("timeout")
)

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
