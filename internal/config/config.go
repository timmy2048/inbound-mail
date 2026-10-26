package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultRetentionDays      = 3
	DefaultAttachmentMaxBytes = 1 << 20
	DefaultMessageMaxBytes    = 8 << 20
)

// Config is the process configuration. Secrets come from the environment only.
type Config struct {
	HTTPAddr           string
	SMTPAddr           string
	SMTPDomain         string
	PublicBaseURL      string
	AdminPassword      string
	RetentionDays      int
	AttachmentMaxBytes int64
	MessageMaxBytes    int64
	MailDataDir        string
	DatabasePath       string
	ServerPublicIP     string
	SMTPTLSCert        string
	SMTPTLSKey         string
	SecureCookie       bool
	// IngestToken authenticates POST /ingest. Empty disables the endpoint.
	IngestToken string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:       getenv("HTTP_ADDR", ":8080"),
		SMTPAddr:       getenv("SMTP_ADDR", ":2525"),
		SMTPDomain:     getenv("SMTP_DOMAIN", "localhost"),
		PublicBaseURL:  getenv("PUBLIC_BASE_URL", "http://127.0.0.1:8787"),
		AdminPassword:  os.Getenv("ADMIN_PASSWORD"),
		ServerPublicIP: getenv("SERVER_PUBLIC_IP", "203.0.113.10"),
		SMTPTLSCert:    strings.TrimSpace(os.Getenv("SMTP_TLS_CERT")),
		SMTPTLSKey:     strings.TrimSpace(os.Getenv("SMTP_TLS_KEY")),
		MailDataDir:    getenv("MAIL_DATA_DIR", "./data"),
		DatabasePath:   strings.TrimSpace(os.Getenv("DATABASE_PATH")),
		IngestToken:    strings.TrimSpace(os.Getenv("INGEST_TOKEN")),
	}

	if strings.TrimSpace(cfg.AdminPassword) == "" {
		return Config{}, errors.New("需要设置 ADMIN_PASSWORD（明文或 bcrypt 哈希）。复制 .env.example 为 .env 后填写")
	}

	days, err := envInt("RETENTION_DAYS", DefaultRetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.RetentionDays = days

	att, err := envInt64("ATTACHMENT_MAX_BYTES", DefaultAttachmentMaxBytes, 1, 20<<20)
	if err != nil {
		return Config{}, err
	}
	cfg.AttachmentMaxBytes = att

	msg, err := envInt64("MESSAGE_MAX_BYTES", DefaultMessageMaxBytes, 1, 32<<20)
	if err != nil {
		return Config{}, err
	}
	if msg < att {
		return Config{}, fmt.Errorf("MESSAGE_MAX_BYTES（%d）不能小于 ATTACHMENT_MAX_BYTES（%d）", msg, att)
	}
	cfg.MessageMaxBytes = msg

	base, secure, err := normalizeBase(cfg.PublicBaseURL)
	if err != nil {
		return Config{}, err
	}
	cfg.PublicBaseURL = base
	cfg.SecureCookie = secure

	dataDir, err := filepath.Abs(cfg.MailDataDir)
	if err != nil {
		return Config{}, fmt.Errorf("MAIL_DATA_DIR: %w", err)
	}
	cfg.MailDataDir = dataDir
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = filepath.Join(dataDir, "mail.db")
	} else {
		dbPath, err := filepath.Abs(cfg.DatabasePath)
		if err != nil {
			return Config{}, fmt.Errorf("DATABASE_PATH: %w", err)
		}
		cfg.DatabasePath = dbPath
	}

	if (cfg.SMTPTLSCert == "") != (cfg.SMTPTLSKey == "") {
		return Config{}, errors.New("SMTP_TLS_CERT 和 SMTP_TLS_KEY 需要同时设置，或同时留空")
	}
	if strings.TrimSpace(cfg.SMTPDomain) == "" {
		return Config{}, errors.New("SMTP_DOMAIN 不能为空")
	}
	return cfg, nil
}

// IngestEnabled reports whether POST /ingest should accept mail.
func (c Config) IngestEnabled() bool {
	return c.IngestToken != ""
}

// MagicLink is the permanent inbox URL for a mailbox token.
func (c Config) MagicLink(token string) string {
	return c.PublicBaseURL + "/m/" + token
}

func normalizeBase(raw string) (string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false, fmt.Errorf("PUBLIC_BASE_URL 必须是 http 或 https 绝对地址，当前是 %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false, errors.New("PUBLIC_BASE_URL 不能带用户名、查询串或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), u.Scheme == "https", nil
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback, min, max int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s 必须是 %d 到 %d 的整数", key, min, max)
	}
	return n, nil
}

func envInt64(key string, fallback, min, max int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s 必须是 %d 到 %d 的整数", key, min, max)
	}
	return n, nil
}
