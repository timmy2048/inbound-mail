package deliver

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"laijian/internal/rfc822"
	"laijian/internal/store"
)

func TestLookupAndStoreRaw(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mail.db"), dir, 32)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	d, err := st.CreateDomain("richrich.cc.cd", now)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := st.CreateMailboxes(d.ID, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	mb := boxes[0]

	if _, err := Lookup(st, "not an address"); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("bad address: %v", err)
	}
	if _, err := Lookup(st, "missing@richrich.cc.cd"); !errors.Is(err, ErrUnknownRecipient) {
		t.Fatalf("unknown: %v", err)
	}
	got, err := Lookup(st, "  <"+mb.LocalPart+"@RICHRICH.CC.CD>  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != mb.ID || got.Address != mb.Address {
		t.Fatalf("lookup %+v", got)
	}

	raw := rfc822.Text("Header <from@example.com>", mb.Address, "via-store", "hello")
	stored, err := StoreRaw(st, mb.ID, mb.Address, "envelope@example.com", raw, now)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := st.Message(mb.ID, stored.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "via-store" || msg.Recipient != mb.Address {
		t.Fatalf("stored %+v", msg)
	}
	if !bytes.Contains([]byte(msg.Sender), []byte("from@example.com")) {
		t.Fatalf("sender should come from the From header, got %q", msg.Sender)
	}

	_, _, err = Accept(st, "nobody@richrich.cc.cd", "a@b.c", raw, 1<<20, now)
	if !errors.Is(err, ErrUnknownRecipient) {
		t.Fatalf("accept unknown: %v", err)
	}
	_, _, err = Accept(st, mb.Address, "a@b.c", nil, 1<<20, now)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("accept empty: %v", err)
	}
	_, _, err = Accept(st, mb.Address, "a@b.c", raw, 8, now)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("accept size: %v", err)
	}
	if msgs, err := st.ListMessages(mb.ID, 10); err != nil || len(msgs) != 1 {
		t.Fatalf("rejected attempts must not store, msgs=%v err=%v", msgs, err)
	}
}
