package web

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"laijian/internal/store"
)

const maxAdHTML = 32 << 10

// AdSlot is the public inbox advertisement. HTML is rendered unescaped
// because pasting a third-party tag is the administrator's explicit action.
// Only a signed-in administrator can change it.
type AdSlot struct {
	Show        bool
	Title       string
	Description string
	ImageURL    string
	LinkURL     string
	HTML        template.HTML
}

type AdsView struct {
	Base
	Enabled     bool
	ImageURL    string
	LinkURL     string
	Title       string
	Description string
	HTML        string
}

func (s *Server) ads(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetAdSettings()
	if err != nil {
		log.Printf("ad settings: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出广告设置。")
		return
	}
	s.renderAds(w, r, cfg, "", http.StatusOK)
}

func (s *Server) saveAds(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
	cfg, msg := normalizeAdForm(r)
	if msg != "" {
		s.renderAds(w, r, cfg, msg, http.StatusBadRequest)
		return
	}
	if err := s.store.SaveAdSettings(cfg); err != nil {
		log.Printf("save ad settings: %v", err)
		s.renderAds(w, r, cfg, "暂时没能保存广告设置。", http.StatusInternalServerError)
		return
	}
	log.Printf("admin saved ad enabled=%t image=%t html=%t", cfg.Enabled, cfg.ImageURL != "", cfg.HTML != "")
	http.Redirect(w, r, "/admin/ads?notice=saved-ad", http.StatusSeeOther)
}

func (s *Server) renderAds(w http.ResponseWriter, r *http.Request, cfg store.AdSettings, errMsg string, status int) {
	view := AdsView{
		Base:        s.adminView("广告设置", r),
		Enabled:     cfg.Enabled,
		ImageURL:    cfg.ImageURL,
		LinkURL:     cfg.LinkURL,
		Title:       cfg.Title,
		Description: cfg.Description,
		HTML:        cfg.HTML,
	}
	view.Error = errMsg
	s.render(w, "ads", view, status)
}

func normalizeAdForm(r *http.Request) (store.AdSettings, string) {
	cfg := store.AdSettings{
		Enabled:     r.FormValue("enabled") == "1",
		ImageURL:    strings.TrimSpace(r.FormValue("image_url")),
		LinkURL:     strings.TrimSpace(r.FormValue("link_url")),
		Title:       strings.Join(strings.Fields(r.FormValue("title")), " "),
		Description: strings.Join(strings.Fields(r.FormValue("description")), " "),
		HTML:        strings.TrimSpace(r.FormValue("html")),
	}
	if cfg.ImageURL != "" {
		if _, ok := safeAdURL(cfg.ImageURL); !ok {
			return cfg, "图片 URL 需要是 http 或 https 链接。"
		}
	}
	if cfg.LinkURL != "" {
		if _, ok := safeAdURL(cfg.LinkURL); !ok {
			return cfg, "点击链接需要是 http 或 https 链接。"
		}
	}
	if utf8.RuneCountInString(cfg.Title) > 80 {
		return cfg, "标题请控制在 80 字以内。"
	}
	if utf8.RuneCountInString(cfg.Description) > 200 {
		return cfg, "说明请控制在 200 字以内。"
	}
	if len(cfg.HTML) > maxAdHTML {
		return cfg, "第三方代码过长。"
	}
	if strings.ContainsRune(cfg.Title, 0) || strings.ContainsRune(cfg.Description, 0) || strings.Contains(cfg.HTML, "\x00") {
		return cfg, "内容里不能有空字符。"
	}
	return cfg, ""
}

// publicAd loads the inbox slot and, when needed, replaces the page CSP.
// The mail HTML frame sets its own policy later and must not use this one.
func (s *Server) publicAd(w http.ResponseWriter) AdSlot {
	cfg, err := s.store.GetAdSettings()
	if err != nil {
		log.Printf("ad settings: %v", err)
		return AdSlot{}
	}
	slot := slotFrom(cfg)
	if !slot.Show {
		return AdSlot{}
	}
	if slot.HTML != "" {
		w.Header().Set("Content-Security-Policy", adScriptPageCSP())
		return slot
	}
	if src := adImageSource(slot.ImageURL); src != "" {
		w.Header().Set("Content-Security-Policy", pageCSP(src))
	}
	return slot
}

func slotFrom(cfg store.AdSettings) AdSlot {
	if !cfg.Enabled {
		return AdSlot{}
	}
	var slot AdSlot
	if image, ok := safeAdURL(cfg.ImageURL); ok {
		slot.ImageURL = image
		if link, ok := safeAdURL(cfg.LinkURL); ok {
			slot.LinkURL = link
		}
	}
	if strings.TrimSpace(cfg.HTML) != "" {
		slot.HTML = template.HTML(cfg.HTML)
	}
	if slot.ImageURL == "" && slot.HTML == "" {
		return AdSlot{}
	}
	slot.Show = true
	slot.Title = cfg.Title
	slot.Description = cfg.Description
	return slot
}

func pageCSP(imageOrigin string) string {
	img := "'self' data:"
	if imageOrigin != "" {
		img += " " + imageOrigin
	}
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src " + img + "; frame-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

// adScriptPageCSP is only for public mailbox pages that embed an
// administrator-supplied tag. It stays off the admin UI and off the mail frame.
func adScriptPageCSP() string {
	return "default-src 'self' https: http: data: blob:; script-src 'self' https: http: 'unsafe-inline' 'unsafe-eval' blob:; style-src 'self' https: http: 'unsafe-inline'; img-src 'self' https: http: data: blob:; font-src 'self' https: http: data:; connect-src 'self' https: http:; frame-src 'self' https: http:; media-src 'self' https: http: data: blob:; worker-src 'self' https: blob:; object-src 'none'; base-uri 'self'; form-action 'self' https: http:; frame-ancestors 'none'"
}

func safeAdURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\t <>\"'\\") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return "", false
	}
	if adImageSource(raw) == "" {
		return "", false
	}
	return raw, true
}

// adImageSource returns a CSP host source for an advertisement image.
func adImageSource(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := u.Hostname()
	if !isAdHost(host) {
		return ""
	}
	src := scheme + "://" + cspHost(host)
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
		src += ":" + port
	}
	return src
}

func isAdHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	alnum := false
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			alnum = true
		case c == '.', c == '-', c == ':':
		default:
			return false
		}
	}
	return alnum
}

func cspHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
