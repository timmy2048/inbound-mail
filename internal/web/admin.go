package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"laijian/internal/addr"
	"laijian/internal/auth"
	"laijian/internal/dnsinfo"
	"laijian/internal/store"
)

type LoginView struct{ Base }

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.loggedIn(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.render(w, "login", LoginView{Base: s.view("登录")}, http.StatusOK)
}

func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	_, ok, err := s.store.Session(c.Value, time.Now())
	return err == nil && ok
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	now := time.Now()
	if s.limit.tooMany(ip, now) {
		view := LoginView{Base: s.view("登录")}
		view.Error = "试得太勤了，请过几分钟再来。"
		s.render(w, "login", view, http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		view := LoginView{Base: s.view("登录")}
		view.Error = "表单无法读取。"
		s.render(w, "login", view, http.StatusBadRequest)
		return
	}
	if !auth.Check(s.cfg.AdminPassword, r.FormValue("password")) {
		s.limit.add(ip, now)
		log.Printf("admin login failed ip=%s", ip)
		view := LoginView{Base: s.view("登录")}
		view.Error = "密码不正确。"
		s.render(w, "login", view, http.StatusUnauthorized)
		return
	}
	s.limit.reset(ip)
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(c.Value)
	}
	sess, err := s.newSession(now)
	if err != nil {
		log.Printf("session create: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能登录。")
		return
	}
	s.setSession(w, sess.ID)
	log.Printf("admin login ok ip=%s", ip)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(c.Value)
	}
	s.clearSession(w)
	w.Header().Set("Clear-Site-Data", `"cookies"`)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

type DomainRow struct {
	ID      int64
	Name    string
	Count   int
	Created string
	RFC     string
}

type DomainsView struct {
	Base
	Domains []DomainRow
	Prefill string
}

func (s *Server) domains(w http.ResponseWriter, r *http.Request) {
	s.domainsPage(w, r, "", "", http.StatusOK)
}

func (s *Server) domainsPage(w http.ResponseWriter, r *http.Request, prefill, errMsg string, status int) {
	list, err := s.store.ListDomains()
	if err != nil {
		log.Printf("list domains: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出域名。")
		return
	}
	view := DomainsView{Base: s.adminView("域名", r), Prefill: prefill}
	view.Error = errMsg
	for _, d := range list {
		view.Domains = append(view.Domains, DomainRow{
			ID:      d.ID,
			Name:    d.Name,
			Count:   d.MailboxCount,
			Created: displayTime(d.CreatedAt),
			RFC:     rfc3339(d.CreatedAt),
		})
	}
	s.render(w, "domains", view, status)
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
	raw := r.FormValue("domain")
	name, err := addr.NormalizeDomain(raw)
	if err != nil {
		s.domainsPage(w, r, raw, "域名格式不正确。示例：richrich.cc.cd", http.StatusBadRequest)
		return
	}
	d, err := s.store.CreateDomain(name, time.Now())
	if errors.Is(err, store.ErrExists) {
		s.domainsPage(w, r, name, "这个域名已经添加过了。", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("create domain: %v", err)
		s.domainsPage(w, r, name, "暂时没能保存域名。", http.StatusInternalServerError)
		return
	}
	log.Printf("admin added domain=%s", d.Name)
	http.Redirect(w, r, fmt.Sprintf("/admin/domains/%d", d.ID), http.StatusSeeOther)
}

type MailboxRow struct {
	ID       int64
	Address  string
	MagicURL string
	Created  string
	RFC      string
}

type DomainView struct {
	Base
	Domain            store.Domain
	Created           string
	RFC               string
	Mailboxes         []MailboxRow
	Guide             dnsinfo.Guide
	Verify            *DNSCheckView
	Worker            *WorkerCheckView
	RoutingConfigured bool
	Count             string
}

func (s *Server) domainPage(w http.ResponseWriter, r *http.Request) {
	s.renderDomain(w, r, r.FormValue("count"), "", http.StatusOK)
}

func (s *Server) renderDomain(w http.ResponseWriter, r *http.Request, count, errMsg string, status int) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	d, err := s.store.Domain(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		log.Printf("domain: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出这个域名。")
		return
	}
	boxes, err := s.store.ListMailboxes(d.ID)
	if err != nil {
		log.Printf("mailboxes: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出邮箱。")
		return
	}
	if count == "" {
		count = "10"
	}
	view := DomainView{
		Base:              s.adminView(d.Name, r),
		Domain:            d,
		Created:           displayTime(d.CreatedAt),
		RFC:               rfc3339(d.CreatedAt),
		Guide:             dnsinfo.GuideFor(d.Name, s.cfg.ServerPublicIP),
		Verify:            s.dnsCheckFor(r, d.ID),
		Worker:            s.workerCheckFor(r, d.ID),
		RoutingConfigured: strings.TrimSpace(s.cfg.CloudflareAPIToken) != "",
		Count:             count,
	}
	view.Error = errMsg
	view.Domain.MailboxCount = len(boxes)
	for _, mb := range boxes {
		view.Mailboxes = append(view.Mailboxes, MailboxRow{
			ID:       mb.ID,
			Address:  mb.Address,
			MagicURL: s.cfg.MagicLink(mb.Token),
			Created:  displayTime(mb.CreatedAt),
			RFC:      rfc3339(mb.CreatedAt),
		})
	}
	s.render(w, "domain", view, status)
}

func (s *Server) generateMailboxes(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	rawCount := r.FormValue("count")
	n, err := strconv.Atoi(rawCount)
	if err != nil || n < 1 || n > maxGenerate {
		s.renderDomain(w, r, rawCount, fmt.Sprintf("数量请填写 1 到 %d 的整数。", maxGenerate), http.StatusBadRequest)
		return
	}
	boxes, err := s.store.CreateMailboxes(id, n, time.Now())
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		log.Printf("generate mailboxes: %v", err)
		s.renderDomain(w, r, rawCount, "暂时没能生成邮箱。", http.StatusInternalServerError)
		return
	}
	log.Printf("admin generated domain_id=%d count=%d", id, len(boxes))
	http.Redirect(w, r, fmt.Sprintf("/admin/domains/%d?notice=generated&n=%d", id, len(boxes)), http.StatusSeeOther)
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	if r.FormValue("ack") != "1" {
		s.renderDomain(w, r, "", "删除域名需要勾选确认。", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteDomain(id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		log.Printf("delete domain: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能删除域名。")
		return
	}
	s.checks.drop(id)
	s.wchecks.drop(id)
	log.Printf("admin deleted domain_id=%d", id)
	http.Redirect(w, r, "/admin?notice=deleted-domain", http.StatusSeeOther)
}

func (s *Server) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	mb, err := s.store.Mailbox(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		log.Printf("mailbox: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出邮箱。")
		return
	}
	if err := s.store.DeleteMailbox(id); err != nil {
		log.Printf("delete mailbox: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能删除邮箱。")
		return
	}
	log.Printf("admin deleted mailbox_id=%d domain_id=%d", id, mb.DomainID)
	http.Redirect(w, r, fmt.Sprintf("/admin/domains/%d?notice=deleted-mailbox", mb.DomainID), http.StatusSeeOther)
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	s.writeCSV(w, 0)
}

func (s *Server) exportDomainCSV(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	s.writeCSV(w, id)
}

func (s *Server) writeCSV(w http.ResponseWriter, domainID int64) {
	var boxes []store.Mailbox
	var err error
	filename := "mailboxes.csv"
	if domainID == 0 {
		boxes, err = s.store.ListAllMailboxes()
	} else {
		d, derr := s.store.Domain(domainID)
		if errors.Is(derr, store.ErrNotFound) {
			http.Error(w, "找不到这个域名", http.StatusNotFound)
			return
		}
		if derr != nil {
			log.Printf("csv domain: %v", derr)
			http.Error(w, "暂时没能导出", http.StatusInternalServerError)
			return
		}
		filename = "mailboxes-" + d.Name + ".csv"
		boxes, err = s.store.ListMailboxes(domainID)
	}
	if err != nil {
		log.Printf("csv: %v", err)
		http.Error(w, "暂时没能导出", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"address", "magic_link", "created_at", "domain"})
	for _, mb := range boxes {
		_ = cw.Write([]string{
			mb.Address,
			s.cfg.MagicLink(mb.Token),
			mb.CreatedAt.UTC().Format(time.RFC3339),
			mb.DomainName,
		})
	}
	cw.Flush()
}
