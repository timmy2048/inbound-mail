package web

import (
	"html"
	"html/template"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

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

// rewriteMailHTML keeps http(s) and mailto anchors, and gives them a safe
// rel and target. Other href schemes are dropped. Markup other than anchors
// is left in place; remote images stay blocked by the response CSP.
func rewriteMailHTML(src string) string {
	if strings.TrimSpace(src) == "" {
		return src
	}
	root, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			if n.Data == "a" {
				rewriteAnchor(n)
			} else {
				n.Attr = dropHandlers(n.Attr)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	var b strings.Builder
	if err := xhtml.Render(&b, root); err != nil {
		return src
	}
	return b.String()
}

func rewriteAnchor(n *xhtml.Node) {
	var href string
	var attrs []xhtml.Attribute
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		switch key {
		case "href":
			href = a.Val
		case "rel", "target":
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
	if raw == "" || strings.ContainsAny(raw, "\r\n\t") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
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
