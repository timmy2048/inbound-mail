package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "secret")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("SMTP_ADDR", "")
	t.Setenv("SMTP_DOMAIN", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("RETENTION_DAYS", "")
	t.Setenv("ATTACHMENT_MAX_BYTES", "")
	t.Setenv("MESSAGE_MAX_BYTES", "")
	t.Setenv("MAIL_DATA_DIR", "")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("SERVER_PUBLIC_IP", "")
	t.Setenv("SMTP_TLS_CERT", "")
	t.Setenv("SMTP_TLS_KEY", "")
	t.Setenv("INGEST_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionDays != 3 {
		t.Fatalf("retention = %d", cfg.RetentionDays)
	}
	if cfg.AttachmentMaxBytes != 1<<20 {
		t.Fatalf("attachment max = %d", cfg.AttachmentMaxBytes)
	}
	if cfg.PublicBaseURL != "http://127.0.0.1:8787" {
		t.Fatalf("base = %s", cfg.PublicBaseURL)
	}
	if cfg.SecureCookie {
		t.Fatal("http base should not set secure cookies")
	}
	if got := cfg.MagicLink("abc"); got != "http://127.0.0.1:8787/m/abc" {
		t.Fatal(got)
	}
	if cfg.IngestEnabled() {
		t.Fatal("empty INGEST_TOKEN should disable ingest")
	}
}

func TestIngestTokenTrimmed(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "secret")
	t.Setenv("RETENTION_DAYS", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("SMTP_TLS_CERT", "")
	t.Setenv("SMTP_TLS_KEY", "")
	t.Setenv("INGEST_TOKEN", "  abcdefghijklmnop  ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IngestToken != "abcdefghijklmnop" || !cfg.IngestEnabled() {
		t.Fatalf("token %q enabled %v", cfg.IngestToken, cfg.IngestEnabled())
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Setenv("SMTP_TLS_CERT", "")
	t.Setenv("SMTP_TLS_KEY", "")
	t.Setenv("ADMIN_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing password")
	}

	t.Setenv("ADMIN_PASSWORD", "secret")
	t.Setenv("RETENTION_DAYS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected bad retention")
	}

	t.Setenv("RETENTION_DAYS", "3")
	t.Setenv("PUBLIC_BASE_URL", "ftp://example.com")
	if _, err := Load(); err == nil {
		t.Fatal("expected bad base url")
	}
}
