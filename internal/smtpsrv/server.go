package smtpsrv

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"laijian/internal/config"
	"laijian/internal/deliver"
	"laijian/internal/store"
)

// Server accepts inbound SMTP and stores mail only for generated mailboxes.
type Server struct {
	smtp *smtp.Server
}

func New(cfg config.Config, st *store.Store) (*Server, error) {
	s := smtp.NewServer(&backend{cfg: cfg, store: st})
	s.Addr = cfg.SMTPAddr
	s.Domain = cfg.SMTPDomain
	s.ReadTimeout = 2 * time.Minute
	s.WriteTimeout = 2 * time.Minute
	s.MaxMessageBytes = cfg.MessageMaxBytes
	s.MaxRecipients = 50
	s.MaxLineLength = 16_000
	s.AllowInsecureAuth = false
	if cfg.SMTPTLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.SMTPTLSCert, cfg.SMTPTLSKey)
		if err != nil {
			return nil, err
		}
		s.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}
	return &Server{smtp: s}, nil
}

func (s *Server) ListenAndServe() error { return s.smtp.ListenAndServe() }

func (s *Server) Serve(l net.Listener) error { return s.smtp.Serve(l) }

func (s *Server) Shutdown(ctx context.Context) error { return s.smtp.Shutdown(ctx) }

type backend struct {
	cfg   config.Config
	store *store.Store
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	remote := ""
	if c != nil && c.Conn() != nil && c.Conn().RemoteAddr() != nil {
		remote = c.Conn().RemoteAddr().String()
	}
	_, tlsOn := c.TLSConnectionState()
	return &session{
		store:      b.store,
		maxMessage: b.cfg.MessageMaxBytes,
		remote:     remote,
		tlsOn:      tlsOn,
	}, nil
}

type session struct {
	store      *store.Store
	maxMessage int64
	remote     string
	tlsOn      bool
	from       string
	rcpts      []string
	ids        []int64
}

func (s *session) Mail(from string, opts *smtp.MailOptions) error {
	if opts != nil && opts.RequireTLS && !s.tlsOn {
		return &smtp.SMTPError{
			Code:         530,
			EnhancedCode: smtp.EnhancedCode{5, 7, 0},
			Message:      "REQUIRETLS requested but TLS is not active",
		}
	}
	if opts != nil && opts.Size > s.maxMessage && s.maxMessage > 0 {
		return smtp.ErrDataTooLarge
	}
	s.from = strings.TrimSpace(from)
	s.rcpts = nil
	s.ids = nil
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	mb, err := deliver.Lookup(s.store, to)
	if errors.Is(err, deliver.ErrBadAddress) {
		log.Printf("smtp reject remote=%s reason=bad-address", s.remote)
		return errUnknown
	}
	if errors.Is(err, deliver.ErrUnknownRecipient) {
		log.Printf("smtp reject remote=%s recipient=%s", s.remote, strings.TrimSpace(to))
		return errUnknown
	}
	if err != nil {
		log.Printf("smtp lookup remote=%s err=%v", s.remote, err)
		return errTemporary
	}
	for _, existing := range s.rcpts {
		if existing == mb.Address {
			return nil
		}
	}
	s.rcpts = append(s.rcpts, mb.Address)
	s.ids = append(s.ids, mb.ID)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if len(s.ids) == 0 {
		return &smtp.SMTPError{
			Code:         554,
			EnhancedCode: smtp.EnhancedCode{5, 5, 1},
			Message:      "No valid recipients",
		}
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		if errors.Is(err, smtp.ErrDataTooLarge) {
			return smtp.ErrDataTooLarge
		}
		log.Printf("smtp read remote=%s err=%v", s.remote, err)
		return errTemporary
	}
	received := time.Now()
	for i, id := range s.ids {
		stored, err := deliver.StoreRaw(s.store, id, s.rcpts[i], s.from, raw, received)
		if err != nil {
			log.Printf("smtp store mailbox_id=%d err=%v", id, err)
			return errTemporary
		}
		log.Printf("smtp accepted mailbox_id=%d bytes=%d attachments=%d", id, len(raw), stored.Attachments)
	}
	return nil
}

func (s *session) Reset() {
	s.from = ""
	s.rcpts = nil
	s.ids = nil
}

func (s *session) Logout() error { return nil }

var (
	errUnknown = &smtp.SMTPError{
		Code:         550,
		EnhancedCode: smtp.EnhancedCode{5, 1, 1},
		Message:      "Recipient address rejected",
	}
	errTemporary = &smtp.SMTPError{
		Code:         451,
		EnhancedCode: smtp.EnhancedCode{4, 3, 0},
		Message:      "Temporary server failure",
	}
)
