package web

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"laijian/internal/deliver"
)

const ingestTokenHeader = "X-Ingest-Token"

type ingestError struct {
	Error string `json:"error"`
}

type ingestOK struct {
	ID        int64  `json:"id"`
	Recipient string `json:"recipient"`
}

type ingestJSON struct {
	To        string `json:"to"`
	From      string `json:"from"`
	RawBase64 string `json:"raw_base64"`
}

// ingest accepts one raw RFC822 message for one recipient.
// The same mailbox lookup, parser, and store path as SMTP are used.
// When INGEST_TOKEN is empty the route answers like any other unknown URL.
func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.IngestEnabled() {
		s.notFound(w, r)
		return
	}
	if !secretEqual(presentedIngestToken(r), s.cfg.IngestToken) {
		writeIngest(w, http.StatusUnauthorized, ingestError{Error: "unauthorized"})
		return
	}

	to, from, raw, status, kind := s.readIngest(w, r)
	if kind != "" {
		writeIngest(w, status, ingestError{Error: kind})
		return
	}

	mb, stored, err := deliver.Accept(s.store, to, from, raw, s.cfg.MessageMaxBytes, time.Now())
	switch {
	case errors.Is(err, deliver.ErrEmpty):
		writeIngest(w, http.StatusBadRequest, ingestError{Error: "empty_message"})
	case errors.Is(err, deliver.ErrBadAddress):
		log.Printf("http ingest reject reason=bad-address")
		writeIngest(w, http.StatusBadRequest, ingestError{Error: "bad_address"})
	case errors.Is(err, deliver.ErrUnknownRecipient):
		log.Printf("http ingest reject recipient=%s", clipLog(to))
		writeIngest(w, http.StatusUnprocessableEntity, ingestError{Error: "unknown_recipient"})
	case errors.Is(err, deliver.ErrTooLarge):
		writeIngest(w, http.StatusRequestEntityTooLarge, ingestError{Error: "message_too_large"})
	case err != nil:
		log.Printf("http ingest store err=%v", err)
		writeIngest(w, http.StatusInternalServerError, ingestError{Error: "temporary_failure"})
	default:
		log.Printf("http ingest accepted mailbox_id=%d message_id=%d bytes=%d attachments=%d", mb.ID, stored.MessageID, len(raw), stored.Attachments)
		writeIngest(w, http.StatusCreated, ingestOK{ID: stored.MessageID, Recipient: mb.Address})
	}
}

func (s *Server) readIngest(w http.ResponseWriter, r *http.Request) (to, from string, raw []byte, status int, kind string) {
	if mediaTypeOf(r) == "application/json" {
		return s.readIngestJSON(w, r)
	}
	limit := s.cfg.MessageMaxBytes
	if limit < 1 {
		limit = 1
	}
	body, err := readLimited(w, r.Body, limit)
	if err != nil {
		return failRead(err)
	}
	to = strings.TrimSpace(r.Header.Get("X-Envelope-To"))
	from = strings.TrimSpace(r.Header.Get("X-Envelope-From"))
	if to == "" {
		return "", "", nil, http.StatusBadRequest, "bad_request"
	}
	return to, from, body, 0, ""
}

func (s *Server) readIngestJSON(w http.ResponseWriter, r *http.Request) (to, from string, raw []byte, status int, kind string) {
	limit := jsonBodyLimit(s.cfg.MessageMaxBytes)
	body, err := readLimited(w, r.Body, limit)
	if err != nil {
		return failRead(err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var payload ingestJSON
	if err := dec.Decode(&payload); err != nil {
		return "", "", nil, http.StatusBadRequest, "bad_request"
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", "", nil, http.StatusBadRequest, "bad_request"
	}
	to = strings.TrimSpace(payload.To)
	from = strings.TrimSpace(payload.From)
	if to == "" {
		to = strings.TrimSpace(r.Header.Get("X-Envelope-To"))
	}
	if from == "" {
		from = strings.TrimSpace(r.Header.Get("X-Envelope-From"))
	}
	if to == "" || strings.TrimSpace(payload.RawBase64) == "" {
		return "", "", nil, http.StatusBadRequest, "bad_request"
	}
	raw, err = decodeRawBase64(payload.RawBase64)
	if err != nil {
		return "", "", nil, http.StatusBadRequest, "bad_request"
	}
	return to, from, raw, 0, ""
}

func failRead(err error) (string, string, []byte, int, string) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return "", "", nil, http.StatusRequestEntityTooLarge, "message_too_large"
	}
	return "", "", nil, http.StatusBadRequest, "bad_request"
}

func readLimited(w http.ResponseWriter, body io.ReadCloser, n int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, body, n))
}

func jsonBodyLimit(maxMessage int64) int64 {
	if maxMessage < 1 {
		maxMessage = 1
	}
	// Standard base64 expands 3 bytes into 4, plus a short JSON envelope.
	return ((maxMessage+2)/3)*4 + 4096
}

func decodeRawBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, s)
	if s == "" {
		return nil, errors.New("empty")
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func mediaTypeOf(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

func presentedIngestToken(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		scheme, rest, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return ""
		}
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(r.Header.Get(ingestTokenHeader))
}

func secretEqual(got, want string) bool {
	if want == "" || got == "" {
		return false
	}
	sumGot := sha256.Sum256([]byte(got))
	sumWant := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(sumGot[:], sumWant[:]) == 1
}

func writeIngest(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func clipLog(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) && s != "" {
			s = s[:len(s)-1]
		}
	}
	return s
}
