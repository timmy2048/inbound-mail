package store

import (
	"database/sql"
	"errors"
)

// AdSettings is the single global inbox advertisement.
// HTML is administrator-authored markup and is stored unchanged.
type AdSettings struct {
	Enabled     bool
	ImageURL    string
	LinkURL     string
	Title       string
	Description string
	HTML        string
}

func (s *Store) GetAdSettings() (AdSettings, error) {
	var a AdSettings
	var enabled int
	err := s.db.QueryRow(`
		SELECT enabled, image_url, link_url, title, description, html
		FROM ad_settings WHERE id = 1`).Scan(
		&enabled, &a.ImageURL, &a.LinkURL, &a.Title, &a.Description, &a.HTML,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AdSettings{}, nil
	}
	if err != nil {
		return AdSettings{}, err
	}
	a.Enabled = enabled != 0
	return a, nil
}

func (s *Store) SaveAdSettings(a AdSettings) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO ad_settings (id, enabled, image_url, link_url, title, description, html)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			image_url = excluded.image_url,
			link_url = excluded.link_url,
			title = excluded.title,
			description = excluded.description,
			html = excluded.html`,
		enabled, a.ImageURL, a.LinkURL, a.Title, a.Description, a.HTML,
	)
	return err
}
