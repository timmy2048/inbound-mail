package web

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

var shanghai = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

func displayTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(shanghai).Format("2006-01-02 15:04")
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "0 B"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n == 1024*1024:
		return "1 MB"
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func showSubject(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无主题）"
	}
	return s
}

func contentDisposition(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if cleaned == "" {
		cleaned = "attachment"
	}
	var fallback strings.Builder
	for _, r := range cleaned {
		if r > unicode.MaxASCII {
			fallback.WriteByte('_')
			continue
		}
		fallback.WriteRune(r)
	}
	ascii := fallback.String()
	if strings.Trim(ascii, "._") == "" {
		ascii = "attachment"
	}
	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", ascii, url.PathEscape(cleaned))
}
