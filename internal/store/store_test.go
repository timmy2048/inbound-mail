package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"laijian/internal/generate"
	"laijian/internal/parse"
)

func openTest(t *testing.T, attachmentMax int64) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "mail.db"), dir, attachmentMax)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCreateMailboxesFormatAndRetention(t *testing.T) {
	st := openTest(t, 32)
	now := time.Date(2026, 10, 26, 5, 0, 0, 0, time.UTC)
	d, err := st.CreateDomain("richrich.cc.cd", now)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := st.CreateMailboxes(d.ID, 5, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 5 {
		t.Fatalf("got %d mailboxes", len(boxes))
	}
	for _, mb := range boxes {
		if !generate.ValidLocalPart(mb.LocalPart) {
			t.Fatalf("local part %q", mb.LocalPart)
		}
		if mb.Address != mb.LocalPart+"@richrich.cc.cd" {
			t.Fatalf("address %q", mb.Address)
		}
		if len(mb.Token) < 40 {
			t.Fatalf("token %q", mb.Token)
		}
	}

	fresh := parse.Parsed{Sender: "a@b.c", Subject: "留下", Text: "new"}
	old := parse.Parsed{
		Sender:  "a@b.c",
		Subject: "删掉",
		Text:    "old",
		Attachments: []parse.Attachment{{
			Filename:    "note.txt",
			ContentType: "text/plain",
			Data:        bytesOf(80),
		}},
	}
	if _, err := st.SaveMessage(boxes[0].ID, boxes[0].Address, fresh, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	oldID, err := st.SaveMessage(boxes[0].ID, boxes[0].Address, old, now.Add(-96*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.Attachments(oldID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || !atts[0].Truncated || atts[0].StoredBytes != 32 || atts[0].SizeBytes != 80 {
		t.Fatalf("attachment = %+v", atts)
	}
	full, err := st.OpenAttachment(atts[0].RelPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 32 {
		t.Fatalf("file size %d", info.Size())
	}

	n, err := st.DeleteReceivedBefore(now.Add(-72 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("attachment file still present: %v", err)
	}
	left, err := st.ListMessages(boxes[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Subject != "留下" {
		t.Fatalf("remaining %+v", left)
	}
	edge, err := st.SaveMessage(boxes[0].ID, boxes[0].Address, parse.Parsed{Sender: "a@b.c", Subject: "刚好", Text: "edge"}, now.Add(-72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteReceivedBefore(now.Add(-72 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Message(boxes[0].ID, edge); err != nil {
		t.Fatal("message exactly 3 days old should be kept")
	}

	if _, err := st.CreateDomain("richrich.cc.cd", now); err != ErrExists {
		t.Fatalf("duplicate domain err = %v", err)
	}
	if err := st.DeleteDomain(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MailboxByAddress(boxes[0].Address); err != ErrNotFound {
		t.Fatalf("mailbox remained: %v", err)
	}
}

func bytesOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return b
}
