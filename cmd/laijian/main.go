package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"laijian/internal/config"
	"laijian/internal/retention"
	"laijian/internal/smtpsrv"
	"laijian/internal/store"
	"laijian/internal/web"
)

func main() {
	log.SetFlags(log.LstdFlags)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	st, err := store.Open(cfg.DatabasePath, cfg.MailDataDir, cfg.AttachmentMaxBytes)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	retention.Start(ctx, st, cfg.RetentionDays)

	smtpServer, err := smtpsrv.New(cfg, st)
	if err != nil {
		log.Fatalf("smtp: %v", err)
	}
	httpServer, err := web.New(cfg, st)
	if err != nil {
		log.Fatalf("http: %v", err)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- smtpServer.ListenAndServe() }()
	go func() { errCh <- httpServer.ListenAndServe() }()
	log.Printf("http listening on %s", cfg.HTTPAddr)
	if cfg.IngestEnabled() {
		if len(cfg.IngestToken) < 16 {
			log.Printf("http ingest token is shorter than 16 characters")
		}
		log.Printf("http ingest enabled on POST /ingest")
	} else {
		log.Printf("http ingest disabled (INGEST_TOKEN empty)")
	}
	if cfg.CloudflareAPIToken == "" {
		log.Printf("worker routing check disabled (CLOUDFLARE_API_TOKEN empty)")
	} else {
		log.Printf("worker routing check enabled")
	}
	log.Printf("smtp listening on %s (inbound only)", cfg.SMTPAddr)
	log.Printf("public base %s", cfg.PublicBaseURL)
	log.Printf("retention %d days, attachment max %d bytes", cfg.RetentionDays, cfg.AttachmentMaxBytes)

	select {
	case <-ctx.Done():
		log.Printf("shutting down")
	case err := <-errCh:
		if !benign(err) {
			log.Printf("server stopped: %v", err)
		}
		stop()
	}

	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shCtx); err != nil && !benign(err) {
		log.Printf("http shutdown: %v", err)
	}
	if err := smtpServer.Shutdown(shCtx); err != nil && !benign(err) {
		log.Printf("smtp shutdown: %v", err)
	}
}

func benign(err error) bool {
	return err == nil ||
		errors.Is(err, http.ErrServerClosed) ||
		errors.Is(err, gosmtp.ErrServerClosed) ||
		errors.Is(err, net.ErrClosed)
}
