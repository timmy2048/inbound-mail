package store

import (
	"errors"
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
	boxes, err := st.CreateMailboxes(d.ID, 5, generate.DefaultLocalPartLength, now)
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

func TestCreateMailboxPrefixAndDuplicate(t *testing.T) {
	st := openTest(t, 32)
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	d, err := st.CreateDomain("richrich.cc.cd", now)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := st.CreateMailbox(d.ID, "  Hello.World ", now)
	if err != nil {
		t.Fatal(err)
	}
	if mb.LocalPart != "hello.world" || mb.Address != "hello.world@richrich.cc.cd" {
		t.Fatalf("mailbox %+v", mb)
	}
	if len(mb.Token) < 40 {
		t.Fatalf("token %q", mb.Token)
	}
	if _, err := st.CreateMailbox(d.ID, "HELLO.world", now.Add(time.Minute)); err != ErrExists {
		t.Fatalf("duplicate err = %v", err)
	}
	if _, err := st.CreateMailbox(d.ID, "a..b", now); err == nil {
		t.Fatal("invalid prefix was stored")
	}
	other, err := st.CreateDomain("other.test", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateMailbox(other.ID, "hello.world", now)
	if err != nil {
		t.Fatal(err)
	}
	if second.Address != "hello.world@other.test" {
		t.Fatalf("other domain %+v", second)
	}
	list, err := st.ListMailboxes(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Address != mb.Address || list[0].Token != mb.Token {
		t.Fatalf("list %+v", list)
	}
	all, err := st.ListAllMailboxes()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all %+v", all)
	}
}

func TestCreateMailboxesLength(t *testing.T) {
	st := openTest(t, 32)
	now := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	d, err := st.CreateDomain("richrich.cc.cd", now)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := st.CreateMailboxes(d.ID, 3, 8, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 3 {
		t.Fatalf("got %d", len(boxes))
	}
	seen := map[string]bool{}
	for _, mb := range boxes {
		if !generate.ValidLocalPartN(mb.LocalPart, 8) {
			t.Fatalf("local %q", mb.LocalPart)
		}
		if seen[mb.LocalPart] {
			t.Fatalf("duplicate %s", mb.LocalPart)
		}
		seen[mb.LocalPart] = true
	}
	if _, err := st.CreateMailboxes(d.ID, 1, 3, now); !errors.Is(err, generate.ErrInvalidLength) {
		t.Fatalf("short length err=%v", err)
	}
	if _, err := st.CreateMailboxes(d.ID, 1, 17, now); !errors.Is(err, generate.ErrInvalidLength) {
		t.Fatalf("long length err=%v", err)
	}
	left, err := st.ListMailboxes(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 3 {
		t.Fatalf("rejected length still inserted: %+v", left)
	}
}

func TestAdSettingsRoundTrip(t *testing.T) {
	st := openTest(t, 32)
	got, err := st.GetAdSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.ImageURL != "" || got.LinkURL != "" || got.Title != "" || got.Description != "" || got.HTML != "" {
		t.Fatalf("default %+v", got)
	}
	want := AdSettings{
		Enabled:     true,
		ImageURL:    "https://cdn.example.com/banner.png",
		LinkURL:     "https://example.com/go",
		Title:       "合作",
		Description: "说明",
		HTML:        `<script>ads()</script>`,
	}
	if err := st.SaveAdSettings(want); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAdSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAdSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v", got)
	}
	want.Enabled = false
	want.HTML = ""
	if err := st.SaveAdSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAdSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.HTML != "" || got.ImageURL != want.ImageURL {
		t.Fatalf("updated %+v", got)
	}
}

func bytesOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return b
}
