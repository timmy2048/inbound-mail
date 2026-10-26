package web

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"html/template"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

// mailFrameSandbox is the iframe sandbox for HTML mail. Scripts are allowed
// only so the document can open http(s) links itself; CSP script-src is a
// nonce, and the frame stays a unique origin (no allow-same-origin).
const mailFrameSandbox = "allow-scripts allow-popups allow-popups-to-escape-sandbox"

// mailClickScript opens safe links in a new unsandboxed tab and cancels the
// click. Without this, a sandboxed frame follows the link itself. Sites that
// send X-Frame-Options: DENY then paint Chrome's blocked-frame placeholder
// where the message used to be.
const mailClickScript = `(function(){
document.addEventListener("click",openMailLink,true);
document.addEventListener("auxclick",openMailLink,true);
function openMailLink(event){
var el=event.target;
while(el&&el.nodeName!=="A"&&el.nodeName!=="AREA")el=el.parentNode;
if(!el)return;
var href=el.getAttribute("href")||"";
var scheme=href.slice(0,href.indexOf(":")).toLowerCase();
if(scheme!=="http"&&scheme!=="https"&&scheme!=="mailto")return;
el.setAttribute("target","_blank");
el.setAttribute("rel","noopener noreferrer");
event.preventDefault();
var opened=window.open(href,"_blank","noopener,noreferrer");
if(opened)opened.opener=null;
}
})();`

// linkifyText escapes plain text and turns http(s) URLs into links.
// The result is safe to render: everything outside a link is escaped.
func linkifyText(s string) template.HTML {
	var b strings.Builder
	for len(s) > 0 {
		start := indexHTTP(s)
		if start < 0 {
			b.WriteString(html.EscapeString(s))
			break
		}
		b.WriteString(html.EscapeString(s[:start]))
		raw := s[start:]
		end := endHTTP(raw)
		candidate := raw[:end]
		core, trail := trimTrail(candidate)
		if href, ok := safeHTTPURL(core); ok {
			writeLink(&b, href, core)
			b.WriteString(html.EscapeString(trail))
		} else {
			b.WriteString(html.EscapeString(candidate))
		}
		s = raw[end:]
	}
	return template.HTML(b.String())
}

func writeLink(b *strings.Builder, href, label string) {
	b.WriteString(`<a href="`)
	b.WriteString(html.EscapeString(href))
	b.WriteString(`" rel="noopener noreferrer" target="_blank">`)
	b.WriteString(html.EscapeString(label))
	b.WriteString(`</a>`)
}

func indexHTTP(s string) int {
	lower := strings.ToLower(s)
	from := 0
	for {
		i := strings.Index(lower[from:], "http")
		if i < 0 {
			return -1
		}
		i += from
		if !schemeAt(s, i) {
			from = i + 1
			continue
		}
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(s[:i])
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				from = i + 1
				continue
			}
		}
		return i
	}
}

func schemeAt(s string, i int) bool {
	rest := s[i:]
	if len(rest) >= 8 && strings.EqualFold(rest[:8], "https://") {
		return true
	}
	return len(rest) >= 7 && strings.EqualFold(rest[:7], "http://")
}

func endHTTP(s string) int {
	i := 0
	if strings.HasPrefix(strings.ToLower(s), "https://") {
		i = len("https://")
	} else if strings.HasPrefix(strings.ToLower(s), "http://") {
		i = len("http://")
	}
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if unicode.IsSpace(r) || strings.ContainsRune("<>\"'`", r) {
			break
		}
		if i-len("https://") > 2048 {
			break
		}
		i += size
	}
	return i
}

func trimTrail(raw string) (string, string) {
	end := len(raw)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(raw[:end])
		if r == ')' && strings.Count(raw[:end], "(") >= strings.Count(raw[:end], ")") {
			break
		}
		if !trailPunct(r) {
			break
		}
		end -= size
	}
	if end <= 0 {
		return raw, ""
	}
	return raw[:end], raw[end:]
}

func trailPunct(r rune) bool {
	switch r {
	case '.', ',', ';', ':', '!', '?', ')', ']', '}', '>', '"', '\'',
		'，', '。', '；', '：', '！', '？', '）', '】', '》', '」':
		return true
	default:
		return false
	}
}

func safeHTTPURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\t ") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if u.Host == "" {
		return "", false
	}
	return raw, true
}

// mailHTMLCSP is the policy for the framed message document.
// Remote images stay limited to data: URLs. The sandbox token list must
// match the iframe attribute: a CSP sandbox is intersected with it, and a
// shorter list would drop allow-popups-to-escape-sandbox.
func mailHTMLCSP(nonce string) string {
	scriptSrc := "'none'"
	sandbox := "sandbox"
	if nonce != "" {
		scriptSrc = "'nonce-" + nonce + "'"
		sandbox = "sandbox " + mailFrameSandbox
	}
	return sandbox + "; default-src 'none'; script-src " + scriptSrc + "; style-src 'unsafe-inline'; img-src data:; form-action 'none'; frame-ancestors 'self'"
}

// rewriteMailHTML keeps http(s) and mailto anchors, and gives them a safe
// rel and target. Other href schemes are dropped. The returned nonce allows
// exactly one inline script, which opens those links outside the frame.
// Remote images are left in the markup and blocked by the response CSP.
func rewriteMailHTML(src string) (string, string) {
	nonce, err := randomNonce()
	if err != nil || strings.TrimSpace(src) == "" {
		return src, ""
	}
	root, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return html.EscapeString(src), ""
	}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == xhtml.ElementNode && dropElement(c) {
				n.RemoveChild(c)
				c = next
				continue
			}
			if c.Type == xhtml.ElementNode {
				switch c.Data {
				case "a", "area":
					rewriteAnchor(c)
				default:
					c.Attr = dropHandlers(c.Attr)
				}
			}
			walk(c)
			c = next
		}
	}
	walk(root)
	if head := findElement(root, "head"); head != nil {
		head.AppendChild(&xhtml.Node{
			Type: xhtml.ElementNode,
			Data: "base",
			Attr: []xhtml.Attribute{{Key: "target", Val: "_blank"}},
		})
		script := &xhtml.Node{
			Type: xhtml.ElementNode,
			Data: "script",
			Attr: []xhtml.Attribute{{Key: "nonce", Val: nonce}},
		}
		script.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: mailClickScript})
		head.AppendChild(script)
	}
	var b strings.Builder
	if err := xhtml.Render(&b, root); err != nil {
		return html.EscapeString(src), ""
	}
	return b.String(), nonce
}

func randomNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func findElement(n *xhtml.Node, name string) *xhtml.Node {
	if n.Type == xhtml.ElementNode && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, name); found != nil {
			return found
		}
	}
	return nil
}

func dropElement(n *xhtml.Node) bool {
	switch n.Data {
	case "script", "iframe", "object", "embed", "applet", "frame", "frameset", "base", "link", "form":
		return true
	case "meta":
		for _, a := range n.Attr {
			if strings.EqualFold(a.Key, "http-equiv") {
				switch strings.ToLower(strings.TrimSpace(a.Val)) {
				case "refresh", "content-security-policy", "set-cookie":
					return true
				}
			}
		}
	}
	return false
}

func rewriteAnchor(n *xhtml.Node) {
	var href string
	var attrs []xhtml.Attribute
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		switch key {
		case "href":
			href = a.Val
		case "rel", "target", "ping":
			continue
		default:
			if strings.HasPrefix(key, "on") {
				continue
			}
			attrs = append(attrs, a)
		}
	}
	safe := safeHref(href)
	if safe == "" {
		n.Attr = attrs
		return
	}
	n.Attr = append(attrs,
		xhtml.Attribute{Key: "href", Val: safe},
		xhtml.Attribute{Key: "rel", Val: "noopener noreferrer"},
		xhtml.Attribute{Key: "target", Val: "_blank"},
	)
}

func dropHandlers(attrs []xhtml.Attribute) []xhtml.Attribute {
	out := attrs[:0]
	for _, a := range attrs {
		if strings.HasPrefix(strings.ToLower(a.Key), "on") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func safeHref(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\t ") {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return ""
		}
	case "mailto":
		if u.Opaque == "" && u.Path == "" {
			return ""
		}
	default:
		return ""
	}
	return raw
}
