package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"laijian/internal/config"
	"laijian/internal/rfc822"
	"laijian/internal/store"
)

func TestIngestDisabledIsNotFound(t *testing.T) {
	st, ts, mb := openIngest(t, "", 1<<20, 32)
	status, body := postIngest(t, ts.URL+"/ingest", "", mb.Address, "from@example.com", rfc822.Text("from@example.com", mb.Address, "nope", "hidden"), false)
	if status != http.StatusNotFound || !strings.Contains(body, "找不到") {
		t.Fatalf("status=%d body=%s", status, body)
	}
	msgs, err := st.ListMessages(mb.ID, 10)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("stored while disabled: %+v %v", msgs, err)
	}
}

func TestIngestRawAndJSON(t *testing.T) {
	const token = "test-ingest-token-value"
	st, ts, mb := openIngest(t, token, 1<<20, 32)

	status, body := postIngest(t, ts.URL+"/ingest", "", mb.Address, "from@example.com", rfc822.Text("from@example.com", mb.Address, "missing-auth", "x"), false)
	if status != http.StatusUnauthorized || !strings.Contains(body, "unauthorized") {
		t.Fatalf("missing auth status=%d body=%s", status, body)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/ingest", bytes.NewReader(rfc822.Text("from@example.com", mb.Address, "wrong", "x")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("X-Ingest-Token", token)
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Envelope-To", mb.Address)
	status, body = do(t, req)
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong bearer must not fall through, status=%d body=%s", status, body)
	}

	raw := rfc822.Text("Header <from@example.com>", mb.Address, "ingest-subject", "hello mailbox")
	upper := strings.ToUpper(mb.LocalPart) + "@RichRich.CC.CD"
	req, err = http.NewRequest(http.MethodPost, ts.URL+"/ingest", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Envelope-To", upper)
	req.Header.Set("X-Envelope-From", "envelope@example.com")
	status, body = do(t, req)
	if status != http.StatusCreated {
		t.Fatalf("raw status=%d body=%s", status, body)
	}
	var created ingestOK
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	if created.Recipient != mb.Address || created.ID < 1 {
		t.Fatalf("created %+v", created)
	}
	msg, err := st.Message(mb.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "ingest-subject" || !strings.Contains(msg.Sender, "from@example.com") || !strings.Contains(msg.TextBody, "hello mailbox") {
		t.Fatalf("parsed %+v", msg)
	}

	noFrom := []byte("To: " + mb.Address + "\r\nSubject: fallback-subject\r\n\r\nfrom envelope\r\n")
	status, body = postIngest(t, ts.URL+"/ingest", token, mb.Address, "bounce@example.com", noFrom, true)
	if status != http.StatusCreated {
		t.Fatalf("header token status=%d body=%s", status, body)
	}
	var second ingestOK
	if err := json.Unmarshal([]byte(body), &second); err != nil {
		t.Fatal(err)
	}
	msg, err = st.Message(mb.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "fallback-subject" || msg.Sender != "bounce@example.com" {
		t.Fatalf("fallback sender %+v", msg)
	}

	fat, err := rfc822.WithAttachment("from@example.com", mb.Address, "fat-subject", "see file", "note.bin", "application/octet-stream", bytes.Repeat([]byte("Z"), 80))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(ingestJSON{
		To:        mb.Address,
		From:      "json@example.com",
		RawBase64: base64.StdEncoding.EncodeToString(fat),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequest(http.MethodPost, ts.URL+"/ingest", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Ingest-Token", token)
	req.Header.Set("Content-Type", "application/json")
	status, body = do(t, req)
	if status != http.StatusCreated {
		t.Fatalf("json status=%d body=%s", status, body)
	}
	var third ingestOK
	if err := json.Unmarshal([]byte(body), &third); err != nil {
		t.Fatal(err)
	}
	atts, err := st.Attachments(third.ID)
	if err != nil || len(atts) != 1 || !atts[0].Truncated || atts[0].SizeBytes != 80 || atts[0].StoredBytes != 32 {
		t.Fatalf("attachments %+v err=%v", atts, err)
	}

	inboxStatus, inbox := getText(t, ts.URL+"/m/"+mb.Token)
	if inboxStatus != http.StatusOK || !strings.Contains(inbox, "ingest-subject") || !strings.Contains(inbox, "fat-subject") {
		t.Fatalf("inbox status=%d body=%s", inboxStatus, inbox)
	}

	status, body = postIngest(t, ts.URL+"/ingest", token, "nobody@richrich.cc.cd", "from@example.com", rfc822.Text("from@example.com", "nobody@richrich.cc.cd", "nope", "no"), true)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "unknown_recipient") {
		t.Fatalf("unknown status=%d body=%s", status, body)
	}
	if err := st.DeleteMailbox(mb.ID); err != nil {
		t.Fatal(err)
	}
	status, body = postIngest(t, ts.URL+"/ingest", token, mb.Address, "from@example.com", raw, true)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "unknown_recipient") {
		t.Fatalf("deleted mailbox status=%d body=%s", status, body)
	}
}

func TestIngestRejectsBadInput(t *testing.T) {
	const token = "test-ingest-token-value"
	st, ts, mb := openIngest(t, token, 64, 32)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/ingest", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	status, body := do(t, req)
	if status != http.StatusNotFound {
		t.Fatalf("GET status=%d body=%s", status, body)
	}

	status, body = postIngest(t, ts.URL+"/ingest", token, "not-an-email", "from@example.com", []byte("Subject: x\r\n\r\nno\r\n"), true)
	if status != http.StatusBadRequest || !strings.Contains(body, "bad_address") {
		t.Fatalf("bad address status=%d body=%s", status, body)
	}

	req, err = http.NewRequest(http.MethodPost, ts.URL+"/ingest", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Envelope-To", mb.Address)
	status, body = do(t, req)
	if status != http.StatusBadRequest || !strings.Contains(body, "empty_message") {
		t.Fatalf("empty status=%d body=%s", status, body)
	}

	req, err = http.NewRequest(http.MethodPost, ts.URL+"/ingest", bytes.NewReader(bytes.Repeat([]byte("A"), 65)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Envelope-To", mb.Address)
	status, body = do(t, req)
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(body, "message_too_large") {
		t.Fatalf("size status=%d body=%s", status, body)
	}

	req, err = http.NewRequest(http.MethodPost, ts.URL+"/ingest", strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Ingest-Token", token)
	req.Header.Set("Content-Type", "application/json")
	status, body = do(t, req)
	if status != http.StatusBadRequest || !strings.Contains(body, "bad_request") {
		t.Fatalf("json status=%d body=%s", status, body)
	}

	msgs, err := st.ListMessages(mb.ID, 10)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("bad input stored %+v %v", msgs, err)
	}
}

func openIngest(t *testing.T, token string, messageMax, attachmentMax int64) (*store.Store, *httptest.Server, store.Mailbox) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		HTTPAddr:           "127.0.0.1:0",
		SMTPAddr:           "127.0.0.1:0",
		SMTPDomain:         "localhost",
		PublicBaseURL:      "http://127.0.0.1:8787",
		AdminPassword:      "test-password-please",
		RetentionDays:      3,
		AttachmentMaxBytes: attachmentMax,
		MessageMaxBytes:    messageMax,
		MailDataDir:        dir,
		DatabasePath:       filepath.Join(dir, "mail.db"),
		ServerPublicIP:     "203.0.113.10",
		IngestToken:        token,
	}
	st, err := store.Open(cfg.DatabasePath, cfg.MailDataDir, cfg.AttachmentMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	now := time.Now()
	d, err := st.CreateDomain("richrich.cc.cd", now)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := st.CreateMailboxes(d.ID, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	return st, ts, boxes[0]
}

func postIngest(t *testing.T, rawURL, token, to, from string, raw []byte, headerToken bool) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Envelope-To", to)
	req.Header.Set("X-Envelope-From", from)
	if token != "" {
		if headerToken {
			req.Header.Set("X-Ingest-Token", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

func getText(t *testing.T, rawURL string) (int, string) {
	t.Helper()
	res, err := http.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}
