package parse

import (
	"bytes"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jhillyerd/enmime"
)

const (
	maxAttachments = 40
	maxHeaderRunes = 300
	maxBodyBytes   = 1_500_000
)

// Attachment is one decoded MIME part saved for download.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Parsed is the stored view of one inbound message.
type Parsed struct {
	Sender     string
	Subject    string
	Text       string
	HTML       string
	Attachments []Attachment
	ParseError bool
}

// Message parses a raw RFC822 message. A parse failure still returns a
// storable placeholder so the SMTP transaction can succeed without keeping
// the raw bytes.
func Message(raw []byte, fallbackSender string) Parsed {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil || env == nil {
		return Parsed{
			Sender:     fallbackSenderOr(fallbackSender),
			Subject:    "（无法解析的信件）",
			Text:       "这封信没法按 MIME 解析，正文没有保存。",
			ParseError: true,
		}
	}
	out := Parsed{
		Sender:  clipRunes(firstNonEmpty(env.GetHeader("From"), fallbackSender), maxHeaderRunes),
		Subject: clipRunes(env.GetHeader("Subject"), maxHeaderRunes),
		Text:    clipBytes(env.Text, maxBodyBytes),
		HTML:    clipBytes(env.HTML, maxBodyBytes),
	}
	if out.Sender == "" {
		out.Sender = "<>"
	}
	var atts []Attachment
	atts = collect(env.Attachments, atts)
	atts = collect(env.Inlines, atts)
	if len(atts) > maxAttachments {
		extra := len(atts) - maxAttachments
		atts = atts[:maxAttachments]
		out.Text += fmt.Sprintf("\n\n（还有 %d 个附件没有保存）", extra)
	}
	out.Attachments = atts
	return out
}

func collect(parts []*enmime.Part, out []Attachment) []Attachment {
	for _, p := range parts {
		if p == nil {
			continue
		}
		ctype := p.ContentType
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		if _, _, err := mime.ParseMediaType(ctype); err != nil && !strings.Contains(ctype, "/") {
			ctype = "application/octet-stream"
		}
		out = append(out, Attachment{
			Filename:    safeName(p.FileName),
			ContentType: ctype,
			Data:        append([]byte(nil), p.Content...),
		})
	}
	return out
}

func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" || name == "_" {
		return "attachment.bin"
	}
	rs := []rune(name)
	if len(rs) > 120 {
		name = string(rs[:120])
	}
	return name
}

func fallbackSenderOr(s string) string {
	s = clipRunes(s, maxHeaderRunes)
	if s == "" {
		return "<>"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func clipRunes(s string, n int) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) > n {
		return string(rs[:n])
	}
	return s
}

func clipBytes(s string, n int) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
