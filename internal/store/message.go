package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"database/sql"

	"laijian/internal/parse"
)

// SaveMessage stores one delivered copy and truncates each attachment to the
// configured maximum. The original size is kept so the inbox can say so.
func (s *Store) SaveMessage(mailboxID int64, recipient string, msg parse.Parsed, received time.Time) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO messages (mailbox_id, sender, recipient, subject, text_body, html_body, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mailboxID, msg.Sender, recipient, msg.Subject, msg.Text, msg.HTML, millis(received),
	)
	if err != nil {
		return 0, err
	}
	messageID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	var written []string
	for _, att := range msg.Attachments {
		data := att.Data
		size := int64(len(data))
		stored := data
		truncated := 0
		if size > s.attachmentMax {
			stored = data[:s.attachmentMax]
			truncated = 1
		}
		res, err := tx.Exec(`
			INSERT INTO attachments (message_id, filename, content_type, size_bytes, stored_bytes, truncated, rel_path)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			messageID, att.Filename, att.ContentType, size, int64(len(stored)), truncated, "",
		)
		if err != nil {
			s.removeRel(written)
			return 0, err
		}
		attID, err := res.LastInsertId()
		if err != nil {
			s.removeRel(written)
			return 0, err
		}
		rel := filepath.Join("attachments", strconv.FormatInt(messageID, 10), strconv.FormatInt(attID, 10))
		full, err := s.safePath(rel)
		if err != nil {
			s.removeRel(written)
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			s.removeRel(written)
			return 0, err
		}
		if err := os.WriteFile(full, stored, 0o640); err != nil {
			s.removeRel(written)
			return 0, err
		}
		written = append(written, rel)
		if _, err := tx.Exec(`UPDATE attachments SET rel_path = ? WHERE id = ?`, rel, attID); err != nil {
			s.removeRel(written)
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		s.removeRel(written)
		return 0, err
	}
	return messageID, nil
}

// ListMessages returns newest first, without bodies. limit must be positive.
func (s *Store) ListMessages(mailboxID int64, limit int) ([]Message, error) {
	if limit < 1 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT id, mailbox_id, sender, recipient, subject, received_at
		FROM messages
		WHERE mailbox_id = ?
		ORDER BY received_at DESC, id DESC
		LIMIT ?`, mailboxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var received int64
		if err := rows.Scan(&m.ID, &m.MailboxID, &m.Sender, &m.Recipient, &m.Subject, &received); err != nil {
			return nil, err
		}
		m.ReceivedAt = fromMillis(received)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Message(mailboxID, messageID int64) (Message, error) {
	var m Message
	var received int64
	err := s.db.QueryRow(`
		SELECT id, mailbox_id, sender, recipient, subject, text_body, html_body, received_at
		FROM messages WHERE id = ? AND mailbox_id = ?`, messageID, mailboxID).Scan(
		&m.ID, &m.MailboxID, &m.Sender, &m.Recipient, &m.Subject, &m.TextBody, &m.HTMLBody, &received,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	m.ReceivedAt = fromMillis(received)
	return m, nil
}

func (s *Store) Attachments(messageID int64) ([]Attachment, error) {
	rows, err := s.db.Query(`
		SELECT id, message_id, filename, content_type, size_bytes, stored_bytes, truncated, rel_path
		FROM attachments WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		att, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, att)
	}
	return out, rows.Err()
}

func (s *Store) Attachment(mailboxID, attachmentID int64) (Attachment, error) {
	row := s.db.QueryRow(`
		SELECT a.id, a.message_id, a.filename, a.content_type, a.size_bytes, a.stored_bytes, a.truncated, a.rel_path
		FROM attachments a
		JOIN messages m ON m.id = a.message_id
		WHERE a.id = ? AND m.mailbox_id = ?`, attachmentID, mailboxID)
	att, err := scanAttachment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	return att, err
}

func scanAttachment(sc scanner) (Attachment, error) {
	var att Attachment
	var truncated int
	err := sc.Scan(&att.ID, &att.MessageID, &att.Filename, &att.ContentType, &att.SizeBytes, &att.StoredBytes, &truncated, &att.RelPath)
	if err != nil {
		return Attachment{}, err
	}
	att.Truncated = truncated != 0
	return att, nil
}

// OpenAttachment returns the on-disk path for a stored attachment.
func (s *Store) OpenAttachment(rel string) (string, error) {
	return s.safePath(rel)
}

// DeleteReceivedBefore removes messages strictly older than cutoff and their files.
func (s *Store) DeleteReceivedBefore(cutoff time.Time) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	paths, err := pathsFor(tx, `
		SELECT a.rel_path
		FROM attachments a
		JOIN messages m ON m.id = a.message_id
		WHERE m.received_at < ?`, millis(cutoff))
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM messages WHERE received_at < ?`, millis(cutoff))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.removeRel(paths)
	return int(n), nil
}
