package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"laijian/internal/dnsinfo"
	"laijian/internal/store"
)

const dnsLookupTimeout = 8 * time.Second

// DNSCheckView is one verification result rendered on the domain page.
type DNSCheckView struct {
	dnsinfo.Result
	Checked string
	RFC     string
}

// SetDNSResolver replaces the resolver used by 验证 DNS. Tests use this.
func (s *Server) SetDNSResolver(r dnsinfo.Resolver) {
	s.dnsMu.Lock()
	s.dns = r
	s.dnsMu.Unlock()
}

func (s *Server) resolver() dnsinfo.Resolver {
	s.dnsMu.RLock()
	r := s.dns
	s.dnsMu.RUnlock()
	if r == nil {
		return dnsinfo.SystemResolver{}
	}
	return r
}

type storedDNS struct {
	at     time.Time
	result dnsinfo.Result
}

// dnsChecks keeps the latest result so a redirect can show it without another lookup.
type dnsChecks struct {
	mu sync.Mutex
	by map[int64]storedDNS
}

func (c *dnsChecks) put(id int64, at time.Time, result dnsinfo.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[int64]storedDNS{}
	}
	c.by[id] = storedDNS{at: at, result: result}
}

func (c *dnsChecks) get(id int64) (storedDNS, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.by[id]
	return v, ok
}

func (c *dnsChecks) drop(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.by, id)
}

func (s *Server) dnsCheckFor(r *http.Request, id int64) *DNSCheckView {
	want := r.URL.Query().Get("checked")
	if want != dnsinfo.PathCloudflare && want != dnsinfo.PathSMTP {
		return nil
	}
	stored, ok := s.checks.get(id)
	if !ok || stored.result.Path != want {
		return nil
	}
	return &DNSCheckView{
		Result:  stored.result,
		Checked: displayTime(stored.at),
		RFC:     rfc3339(stored.at),
	}
}

func (s *Server) verifyDNS(w http.ResponseWriter, r *http.Request) {
	if !s.parsePost(w, r) {
		return
	}
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
	path := r.FormValue("path")
	if path != dnsinfo.PathCloudflare && path != dnsinfo.PathSMTP {
		s.renderDomain(w, r, "", "请选择要验证的收信方式。", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dnsLookupTimeout)
	defer cancel()
	result, err := dnsinfo.Verify(ctx, path, d.Name, s.cfg.ServerPublicIP, s.resolver())
	if err != nil {
		log.Printf("admin dns verify domain=%s path=%s err=%v", d.Name, path, err)
	} else {
		log.Printf("admin dns verify domain=%s path=%s ok=%t", d.Name, path, result.OK)
	}
	s.checks.put(id, time.Now(), result)
	http.Redirect(w, r, fmt.Sprintf("/admin/domains/%d?checked=%s", id, path), http.StatusSeeOther)
}
