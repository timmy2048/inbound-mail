package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"laijian/internal/generate"
)

const maxGenerate = 500

func (s *Store) CreateDomain(name string, now time.Time) (Domain, error) {
	res, err := s.db.Exec(`INSERT INTO domains (name, created_at) VALUES (?, ?)`, name, millis(now))
	if isUnique(err) {
		return Domain{}, ErrExists
	}
	if err != nil {
		return Domain{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Domain{}, err
	}
	return Domain{ID: id, Name: name, CreatedAt: now.UTC()}, nil
}

func (s *Store) Domain(id int64) (Domain, error) {
	var d Domain
	var created int64
	err := s.db.QueryRow(`SELECT id, name, created_at FROM domains WHERE id = ?`, id).Scan(&d.ID, &d.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	if err != nil {
		return Domain{}, err
	}
	d.CreatedAt = fromMillis(created)
	return d, nil
}

func (s *Store) ListDomains() ([]Domain, error) {
	rows, err := s.db.Query(`
		SELECT d.id, d.name, d.created_at, COUNT(b.id)
		FROM domains d
		LEFT JOIN mailboxes b ON b.domain_id = d.id
		GROUP BY d.id
		ORDER BY d.created_at DESC, d.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		var created int64
		if err := rows.Scan(&d.ID, &d.Name, &created, &d.MailboxCount); err != nil {
			return nil, err
		}
		d.CreatedAt = fromMillis(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDomain(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	paths, err := pathsFor(tx, `
		SELECT a.rel_path
		FROM attachments a
		JOIN messages m ON m.id = a.message_id
		JOIN mailboxes b ON b.id = m.mailbox_id
		WHERE b.domain_id = ?`, id)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM domains WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.removeRel(paths)
	return nil
}

// CreateMailboxes allocates n random addresses on a domain.
// Each address gets one permanent token. Collisions are retried.
func (s *Store) CreateMailboxes(domainID int64, n int, now time.Time) ([]Mailbox, error) {
	if n < 1 || n > maxGenerate {
		return nil, fmt.Errorf("count must be 1..%d", maxGenerate)
	}
	domain, err := s.Domain(domainID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	created := make([]Mailbox, 0, n)
	stamp := millis(now)
	for i := 0; i < n; i++ {
		var mb Mailbox
		var lastErr error
		for attempt := 0; attempt < 8; attempt++ {
			local, err := generate.RandomLocalPart()
			if err != nil {
				return nil, err
			}
			token, err := generate.RandomToken()
			if err != nil {
				return nil, err
			}
			address := local + "@" + domain.Name
			res, err := tx.Exec(
				`INSERT INTO mailboxes (domain_id, local_part, address, token, created_at) VALUES (?, ?, ?, ?, ?)`,
				domain.ID, local, address, token, stamp,
			)
			if err != nil {
				if isUnique(err) {
					lastErr = err
					continue
				}
				return nil, err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return nil, err
			}
			mb = Mailbox{
				ID:         id,
				DomainID:   domain.ID,
				DomainName: domain.Name,
				LocalPart:  local,
				Address:    address,
				Token:      token,
				CreatedAt:  now.UTC(),
			}
			lastErr = nil
			break
		}
		if mb.ID == 0 {
			if lastErr != nil {
				return nil, fmt.Errorf("allocate mailbox: %w", lastErr)
			}
			return nil, errors.New("allocate mailbox")
		}
		created = append(created, mb)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Store) ListMailboxes(domainID int64) ([]Mailbox, error) {
	return s.listMailboxes(`WHERE m.domain_id = ?`, domainID)
}

func (s *Store) ListAllMailboxes() ([]Mailbox, error) {
	return s.listMailboxes(``)
}

func (s *Store) listMailboxes(where string, args ...any) ([]Mailbox, error) {
	q := `
		SELECT m.id, m.domain_id, d.name, m.local_part, m.address, m.token, m.created_at
		FROM mailboxes m
		JOIN domains d ON d.id = m.domain_id ` + where + `
		ORDER BY m.created_at DESC, m.id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mailbox
	for rows.Next() {
		mb, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mb)
	}
	return out, rows.Err()
}

func (s *Store) Mailbox(id int64) (Mailbox, error) {
	row := s.db.QueryRow(`
		SELECT m.id, m.domain_id, d.name, m.local_part, m.address, m.token, m.created_at
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.id = ?`, id)
	mb, err := scanMailbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Mailbox{}, ErrNotFound
	}
	return mb, err
}

func (s *Store) MailboxByAddress(address string) (Mailbox, error) {
	row := s.db.QueryRow(`
		SELECT m.id, m.domain_id, d.name, m.local_part, m.address, m.token, m.created_at
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.address = ?`, address)
	mb, err := scanMailbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Mailbox{}, ErrNotFound
	}
	return mb, err
}

func (s *Store) MailboxByToken(token string) (Mailbox, error) {
	row := s.db.QueryRow(`
		SELECT m.id, m.domain_id, d.name, m.local_part, m.address, m.token, m.created_at
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.token = ?`, token)
	mb, err := scanMailbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Mailbox{}, ErrNotFound
	}
	return mb, err
}

func (s *Store) DeleteMailbox(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	paths, err := pathsFor(tx, `
		SELECT a.rel_path
		FROM attachments a
		JOIN messages m ON m.id = a.message_id
		WHERE m.mailbox_id = ?`, id)
	if err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM mailboxes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.removeRel(paths)
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMailbox(sc scanner) (Mailbox, error) {
	var mb Mailbox
	var created int64
	err := sc.Scan(&mb.ID, &mb.DomainID, &mb.DomainName, &mb.LocalPart, &mb.Address, &mb.Token, &created)
	if err != nil {
		return Mailbox{}, err
	}
	mb.CreatedAt = fromMillis(created)
	return mb, nil
}

func pathsFor(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return nil, err
		}
		if rel != "" {
			paths = append(paths, rel)
		}
	}
	return paths, rows.Err()
}
