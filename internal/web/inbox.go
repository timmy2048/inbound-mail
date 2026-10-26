package web

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"laijian/internal/store"
)

type MessageRow struct {
	ID          int64
	Sender      string
	Subject     string
	Received    string
	ReceivedRFC string
}

type InboxView struct {
	Base
	Address string
	Token   string
	Rows    []MessageRow
	More    bool
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.mailboxFromToken(w, r)
	if !ok {
		return
	}
	msgs, err := s.store.ListMessages(mb.ID, messageLimit+1)
	if err != nil {
		log.Printf("list messages: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出信件。")
		return
	}
	view := InboxView{
		Base:    s.view(mb.Address),
		Address: mb.Address,
		Token:   mb.Token,
	}
	if len(msgs) > messageLimit {
		view.More = true
		msgs = msgs[:messageLimit]
	}
	for _, m := range msgs {
		view.Rows = append(view.Rows, MessageRow{
			ID:          m.ID,
			Sender:      m.Sender,
			Subject:     showSubject(m.Subject),
			Received:    displayTime(m.ReceivedAt),
			ReceivedRFC: rfc3339(m.ReceivedAt),
		})
	}
	s.render(w, "inbox", view, http.StatusOK)
}

type AttRow struct {
	ID          int64
	Filename    string
	ContentType string
	Size        string
	Stored      string
	Truncated   bool
}

type MessageView struct {
	Base
	Address     string
	Token       string
	Sender      string
	Subject     string
	Text        string
	HasText     bool
	HasHTML     bool
	Received    string
	ReceivedRFC string
	MessageID   int64
	Attachments []AttRow
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	mb, msg, ok := s.messageFromToken(w, r)
	if !ok {
		return
	}
	atts, err := s.store.Attachments(msg.ID)
	if err != nil {
		log.Printf("attachments: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出附件。")
		return
	}
	view := MessageView{
		Base:        s.view(showSubject(msg.Subject)),
		Address:     mb.Address,
		Token:       mb.Token,
		Sender:      msg.Sender,
		Subject:     showSubject(msg.Subject),
		Text:        msg.TextBody,
		HasText:     strings.TrimSpace(msg.TextBody) != "",
		HasHTML:     strings.TrimSpace(msg.HTMLBody) != "",
		Received:    displayTime(msg.ReceivedAt),
		ReceivedRFC: rfc3339(msg.ReceivedAt),
		MessageID:   msg.ID,
	}
	for _, att := range atts {
		view.Attachments = append(view.Attachments, AttRow{
			ID:          att.ID,
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        humanBytes(att.SizeBytes),
			Stored:      humanBytes(att.StoredBytes),
			Truncated:   att.Truncated,
		})
	}
	s.render(w, "message", view, http.StatusOK)
}

func (s *Server) messageHTML(w http.ResponseWriter, r *http.Request) {
	_, msg, ok := s.messageFromToken(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if strings.TrimSpace(msg.HTMLBody) == "" {
		_, _ = io.WriteString(w, "<!DOCTYPE html><meta charset=\"utf-8\"><p>这封信没有 HTML 正文。</p>")
		return
	}
	_, _ = io.WriteString(w, msg.HTMLBody)
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.mailboxFromToken(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	att, err := s.store.Attachment(mb.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		log.Printf("attachment: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出附件。")
		return
	}
	full, err := s.store.OpenAttachment(att.RelPath)
	if err != nil {
		s.notFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer f.Close()
	ctype := att.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", contentDisposition(att.Filename))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, att.Filename, time.Time{}, f)
}

func (s *Server) mailboxFromToken(w http.ResponseWriter, r *http.Request) (store.Mailbox, bool) {
	token := r.PathValue("token")
	if len(token) < 20 || len(token) > 128 {
		s.notFound(w, r)
		return store.Mailbox{}, false
	}
	mb, err := s.store.MailboxByToken(token)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return store.Mailbox{}, false
	}
	if err != nil {
		log.Printf("mailbox token: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能打开这个邮箱。")
		return store.Mailbox{}, false
	}
	return mb, true
}

func (s *Server) messageFromToken(w http.ResponseWriter, r *http.Request) (store.Mailbox, store.Message, bool) {
	mb, ok := s.mailboxFromToken(w, r)
	if !ok {
		return store.Mailbox{}, store.Message{}, false
	}
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return store.Mailbox{}, store.Message{}, false
	}
	msg, err := s.store.Message(mb.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return store.Mailbox{}, store.Message{}, false
	}
	if err != nil {
		log.Printf("message: %v", err)
		s.problem(w, http.StatusInternalServerError, "暂时没能读出这封信。")
		return store.Mailbox{}, store.Message{}, false
	}
	return mb, msg, true
}
