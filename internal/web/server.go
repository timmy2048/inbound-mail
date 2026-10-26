package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"laijian/internal/config"
	"laijian/internal/dnsinfo"
	"laijian/internal/generate"
	"laijian/internal/store"
	"laijian/internal/workercheck"
)

//go:embed templates/*.html static/*
var assets embed.FS

const (
	sessionCookie = "laijian_session"
	sessionTTL    = 7 * 24 * time.Hour
	maxGenerate   = 500
	messageLimit  = 200
)

type ctxKey int

const ctxCSRF ctxKey = 1

// Server serves the admin UI and magic-link inboxes.
type Server struct {
	cfg      config.Config
	store    *store.Store
	tmpl     *template.Template
	files    map[string][]byte
	http     *http.Server
	limit    *limiter
	handler  http.Handler
	dnsMu    sync.RWMutex
	dns      dnsinfo.Resolver
	checks   dnsChecks
	workerMu sync.RWMutex
	worker   workerVerifier
	wchecks  workerChecks
}

func New(cfg config.Config, st *store.Store) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	entries, err := fs.ReadDir(assets, "static")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := assets.ReadFile("static/" + entry.Name())
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = b
	}
	s := &Server{
		cfg:   cfg,
		store: st,
		tmpl:  tmpl,
		files: files,
		limit: newLimiter(),
		worker: workercheck.Service{Config: workercheck.Config{
			Token:     cfg.CloudflareAPIToken,
			AccountID: cfg.CloudflareAccountID,
			DiagURL:   cfg.WorkerDiagURL,
		}},
	}
	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.Default(),
	}
	s.handler = s.http.Handler
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest", s.ingest)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /static/style.css", s.static("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/app.js", s.static("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /static/favicon.svg", s.static("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /admin/login", s.loginForm)
	mux.HandleFunc("POST /admin/login", s.loginSubmit)
	mux.HandleFunc("POST /admin/logout", s.requireAdmin(s.logout))
	mux.HandleFunc("GET /admin", s.requireAdmin(s.domains))
	mux.HandleFunc("POST /admin/domains", s.requireAdmin(s.createDomain))
	mux.HandleFunc("GET /admin/domains/{id}", s.requireAdmin(s.domainPage))
	mux.HandleFunc("POST /admin/domains/{id}/verify", s.requireAdmin(s.verifyDNS))
	mux.HandleFunc("POST /admin/domains/{id}/verify-worker", s.requireAdmin(s.verifyWorker))
	mux.HandleFunc("POST /admin/domains/{id}/mailboxes", s.requireAdmin(s.generateMailboxes))
	mux.HandleFunc("POST /admin/domains/{id}/delete", s.requireAdmin(s.deleteDomain))
	mux.HandleFunc("GET /admin/domains/{id}/mailboxes.csv", s.requireAdmin(s.exportDomainCSV))
	mux.HandleFunc("GET /admin/mailboxes.csv", s.requireAdmin(s.exportCSV))
	mux.HandleFunc("POST /admin/mailboxes/{id}/delete", s.requireAdmin(s.deleteMailbox))
	mux.HandleFunc("GET /m/{token}", s.inbox)
	mux.HandleFunc("GET /m/{token}/messages/{id}", s.message)
	mux.HandleFunc("GET /m/{token}/messages/{id}/html", s.messageHTML)
	mux.HandleFunc("GET /m/{token}/attachments/{id}", s.attachment)
	mux.HandleFunc("/", s.notFound)
	return s.secure(s.log(mux))
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		log.Printf("http %s %s %d", r.Method, safePath(r.URL.Path), ww.code)
	})
}

func safePath(p string) string {
	if strings.HasPrefix(p, "/m/") {
		return "/m/:token"
	}
	return p
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) static(name, contentType string) http.HandlerFunc {
	data := s.files[name]
	return func(w http.ResponseWriter, r *http.Request) {
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(); err != nil {
		http.Error(w, "db", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			s.deny(w, r)
			return
		}
		csrf, ok, err := s.store.Session(c.Value, time.Now())
		if err != nil {
			log.Printf("session: %v", err)
			s.problem(w, http.StatusInternalServerError, "后台暂时不可用。")
			return
		}
		if !ok {
			s.deny(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxCSRF, csrf)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	http.Error(w, "需要先登录", http.StatusUnauthorized)
}

func (s *Server) parsePost(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.problem(w, http.StatusBadRequest, "表单无法读取。")
		return false
	}
	return s.checkCSRF(w, r)
}

func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	want, _ := r.Context().Value(ctxCSRF).(string)
	got := r.FormValue("csrf")
	if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		s.problem(w, http.StatusBadRequest, "页面已过期，请刷新后再试。")
		return false
	}
	return true
}

type Base struct {
	Title         string
	CSRF          string
	Notice        string
	Error         string
	Admin         bool
	RetentionDays int
	AttachmentMax string
}

func (s *Server) view(title string) Base {
	return Base{
		Title:         title,
		RetentionDays: s.cfg.RetentionDays,
		AttachmentMax: humanBytes(s.cfg.AttachmentMaxBytes),
	}
}

func (s *Server) adminView(title string, r *http.Request) Base {
	b := s.view(title)
	b.Admin = true
	b.CSRF, _ = r.Context().Value(ctxCSRF).(string)
	b.Notice = noticeFrom(r)
	return b
}

func noticeFrom(r *http.Request) string {
	switch r.URL.Query().Get("notice") {
	case "generated":
		n := r.URL.Query().Get("n")
		if n != "" && isDigits(n) {
			return "已生成 " + n + " 个邮箱。每个地址一条永久查看链接。"
		}
	case "deleted-mailbox":
		return "已删除这个邮箱。之后发到该地址的信会被拒收。"
	case "deleted-domain":
		return "已删除这个域名，以及它下面的邮箱和邮件。"
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" || len(s) > 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Server) render(w http.ResponseWriter, name string, data any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func (s *Server) problem(w http.ResponseWriter, status int, msg string) {
	b := s.view("出错了")
	b.Error = msg
	s.render(w, "problem", struct{ Base }{Base: b}, status)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, "notfound", struct{ Base }{Base: s.view("找不到")}, http.StatusNotFound)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, "home", struct{ Base }{Base: s.view("来件")}, http.StatusOK)
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func (s *Server) newSession(now time.Time) (store.Session, error) {
	id, err := generate.RandomToken()
	if err != nil {
		return store.Session{}, err
	}
	csrf, err := generate.RandomToken()
	if err != nil {
		return store.Session{}, err
	}
	if err := s.store.CreateSession(now, sessionTTL, id, csrf); err != nil {
		return store.Session{}, err
	}
	return store.Session{ID: id, CSRF: csrf, ExpiresAt: now.Add(sessionTTL)}, nil
}

func (s *Server) setSession(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookie,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookie,
		MaxAge:   -1,
	})
}

type limiter struct {
	mu   sync.Mutex
	fail map[string][]time.Time
}

func newLimiter() *limiter {
	return &limiter{fail: map[string][]time.Time{}}
}

func (l *limiter) tooMany(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail[key] = recent(l.fail[key], now, 15*time.Minute)
	return len(l.fail[key]) >= 10
}

func (l *limiter) add(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail[key] = append(recent(l.fail[key], now, 15*time.Minute), now)
	if len(l.fail) > 10000 {
		l.fail = map[string][]time.Time{}
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fail, key)
}

func recent(times []time.Time, now time.Time, window time.Duration) []time.Time {
	cut := now.Add(-window)
	out := times[:0]
	for _, ts := range times {
		if ts.After(cut) {
			out = append(out, ts)
		}
	}
	return out
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
