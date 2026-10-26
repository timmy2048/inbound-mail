package rfc822

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
)

// Text builds a UTF-8 plain-text message with CRLF line endings.
func Text(from, to, subject, body string) []byte {
	var msg bytes.Buffer
	writeHeaders(&msg, from, to, subject)
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	msg.WriteString(crlf(body))
	if !strings.HasSuffix(body, "\n") {
		msg.WriteString("\r\n")
	}
	return msg.Bytes()
}

// WithAttachment builds a multipart/mixed message.
func WithAttachment(from, to, subject, body, filename, contentType string, data []byte) ([]byte, error) {
	if filename == "" {
		filename = "attachment.bin"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var msg bytes.Buffer
	mw := multipart.NewWriter(&msg)
	writeHeaders(&msg, from, to, subject)
	fmt.Fprintf(&msg, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mw.Boundary())

	textPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return nil, err
	}
	if _, err := textPart.Write([]byte(crlf(body))); err != nil {
		return nil, err
	}
	filePart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {contentType},
		"Content-Disposition": {fmt.Sprintf("attachment; filename=%q", filename)},
	})
	if err != nil {
		return nil, err
	}
	if _, err := filePart.Write(data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return msg.Bytes(), nil
}

func writeHeaders(msg *bytes.Buffer, from, to, subject string) {
	fmt.Fprintf(msg, "From: %s\r\n", stripCRLF(from))
	fmt.Fprintf(msg, "To: %s\r\n", stripCRLF(to))
	fmt.Fprintf(msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", stripCRLF(subject)))
	fmt.Fprintf(msg, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	msg.WriteString("MIME-Version: 1.0\r\n")
}

func stripCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", "")
}

func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
