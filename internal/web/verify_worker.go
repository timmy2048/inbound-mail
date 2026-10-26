package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"laijian/internal/store"
	"laijian/internal/workercheck"
)

const workerCheckTimeout = 15 * time.Second

// WorkerCheckView is one 验证 Worker result on the domain page.
type WorkerCheckView struct {
	workercheck.Result
	Checked string
	RFC     string
}

type workerVerifier interface {
	Verify(ctx context.Context, domain string) (workercheck.Result, error)
}

// SetWorkerVerifier replaces the checker used by 验证 Worker. Tests use this.
func (s *Server) SetWorkerVerifier(v workerVerifier) {
	s.workerMu.Lock()
	s.worker = v
	s.workerMu.Unlock()
}

func (s *Server) workerVerifier() workerVerifier {
	s.workerMu.RLock()
	v := s.worker
	s.workerMu.RUnlock()
	if v == nil {
		return workercheck.Service{Config: workercheck.Config{
			Token:     s.cfg.CloudflareAPIToken,
			AccountID: s.cfg.CloudflareAccountID,
			DiagURL:   s.cfg.WorkerDiagURL,
		}}
	}
	return v
}

type storedWorker struct {
	at     time.Time
	result workercheck.Result
}

type workerChecks struct {
	mu sync.Mutex
	by map[int64]storedWorker
}

func (c *workerChecks) put(id int64, at time.Time, result workercheck.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[int64]storedWorker{}
	}
	c.by[id] = storedWorker{at: at, result: result}
}

func (c *workerChecks) get(id int64) (storedWorker, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.by[id]
	return v, ok
}

func (c *workerChecks) drop(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.by, id)
}

func (s *Server) workerCheckFor(r *http.Request, id int64) *WorkerCheckView {
	if r.URL.Query().Get("checked") != "worker" {
		return nil
	}
	stored, ok := s.wchecks.get(id)
	if !ok {
		return nil
	}
	return &WorkerCheckView{
		Result:  stored.result,
		Checked: displayTime(stored.at),
		RFC:     rfc3339(stored.at),
	}
}

func (s *Server) verifyWorker(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := context.WithTimeout(r.Context(), workerCheckTimeout)
	defer cancel()
	result, err := s.workerVerifier().Verify(ctx, d.Name)
	if err != nil {
		log.Printf("admin worker verify domain=%s ok=%t err=%v", d.Name, result.OK, err)
	} else {
		log.Printf("admin worker verify domain=%s ok=%t", d.Name, result.OK)
	}
	s.wchecks.put(id, time.Now(), result)
	http.Redirect(w, r, fmt.Sprintf("/admin/domains/%d?checked=worker", id), http.StatusSeeOther)
}
