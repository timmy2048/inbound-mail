package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
)

// Domain is one receiving domain.
type Domain struct {
	ID           int64
	Name         string
	CreatedAt    time.Time
	MailboxCount int
}

// Mailbox is a generated address and its permanent view token.
type Mailbox struct {
	ID         int64
	DomainID   int64
	DomainName string
	LocalPart  string
	Address    string
	Token      string
	CreatedAt  time.Time
}

// Message is one delivered copy in a mailbox.
type Message struct {
	ID         int64
	MailboxID  int64
	Sender     string
	Recipient  string
	Subject    string
	TextBody   string
	HTMLBody   string
	ReceivedAt time.Time
}

// Attachment is a file saved next to the database.
type Attachment struct {
	ID          int64
	MessageID   int64
	Filename    string
	ContentType string
	SizeBytes   int64
	StoredBytes int64
	Truncated   bool
	RelPath     string
}

// Store is the SQLite database and the attachment directory.
type Store struct {
	db             *sql.DB
	dataDir        string
	attachmentMax  int64
}

// Open creates the data directory, the database, and the schema.
func Open(dbPath, dataDir string, attachmentMax int64) (*Store, error) {
	if attachmentMax < 1 {
		attachmentMax = 1 << 20
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, err
	}
	dsn, err := sqliteDSN(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db, dataDir: dataDir, attachmentMax: attachmentMax}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping() error { return s.db.Ping() }

func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{
		Scheme:   "file",
		Path:     abs,
		RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)",
	}
	return u.String(), nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`PRAGMA user_version = 1`)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS domains (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS mailboxes (
    id INTEGER PRIMARY KEY,
    domain_id INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    local_part TEXT NOT NULL,
    address TEXT NOT NULL UNIQUE,
    token TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    UNIQUE(domain_id, local_part)
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY,
    mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    sender TEXT NOT NULL,
    recipient TEXT NOT NULL,
    subject TEXT NOT NULL,
    text_body TEXT NOT NULL,
    html_body TEXT NOT NULL,
    received_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_mailbox_received
    ON messages(mailbox_id, received_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_received ON messages(received_at);

CREATE TABLE IF NOT EXISTS attachments (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    stored_bytes INTEGER NOT NULL,
    truncated INTEGER NOT NULL,
    rel_path TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_id);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    csrf TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
`

func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func millis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// Session is an admin login.
type Session struct {
	ID        string
	CSRF      string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(now time.Time, ttl time.Duration, id, csrf string) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, csrf, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		id, csrf, millis(now), millis(now.Add(ttl)),
	)
	return err
}

func (s *Store) Session(id string, now time.Time) (string, bool, error) {
	var csrf string
	var exp int64
	err := s.db.QueryRow(`SELECT csrf, expires_at FROM sessions WHERE id = ?`, id).Scan(&csrf, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if exp <= millis(now) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
		return "", false, nil
	}
	return csrf, true, nil
}

func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *Store) PruneSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, millis(now))
	return err
}

func (s *Store) safePath(rel string) (string, error) {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("bad attachment path")
	}
	full := filepath.Join(s.dataDir, rel)
	root := filepath.Clean(s.dataDir) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full), root) {
		return "", fmt.Errorf("bad attachment path")
	}
	return full, nil
}

func (s *Store) removeRel(paths []string) {
	for _, rel := range paths {
		full, err := s.safePath(rel)
		if err != nil {
			continue
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		_ = os.Remove(filepath.Dir(full))
	}
}
