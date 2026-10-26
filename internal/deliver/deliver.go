package deliver

import (
	"errors"
	"strings"
	"time"

	"laijian/internal/addr"
	"laijian/internal/parse"
	"laijian/internal/store"
)

var (
	// ErrBadAddress means the recipient is not an address this process can look up.
	ErrBadAddress = errors.New("bad address")
	// ErrUnknownRecipient means the address is well formed but no mailbox was generated.
	ErrUnknownRecipient = errors.New("unknown recipient")
	// ErrEmpty means the RFC822 body had no bytes.
	ErrEmpty = errors.New("empty message")
	// ErrTooLarge means the raw message exceeds the configured maximum.
	ErrTooLarge = errors.New("message too large")
)

// Stored is one saved copy of an inbound message.
type Stored struct {
	MessageID   int64
	Attachments int
}

// Lookup resolves one recipient the same way SMTP RCPT does.
// Unknown and malformed addresses are rejected. Nothing is stored.
func Lookup(st *store.Store, recipient string) (store.Mailbox, error) {
	address, err := addr.NormalizeEmail(recipient)
	if err != nil {
		return store.Mailbox{}, ErrBadAddress
	}
	mb, err := st.MailboxByAddress(address)
	if errors.Is(err, store.ErrNotFound) {
		return store.Mailbox{}, ErrUnknownRecipient
	}
	if err != nil {
		return store.Mailbox{}, err
	}
	return mb, nil
}

// StoreRaw parses raw RFC822 and saves one copy into a mailbox that Lookup already accepted.
// envelopeFrom is the SMTP MAIL FROM value, used only when the message has no From header.
func StoreRaw(st *store.Store, mailboxID int64, recipient, envelopeFrom string, raw []byte, received time.Time) (Stored, error) {
	parsed := parse.Message(raw, strings.TrimSpace(envelopeFrom))
	id, err := st.SaveMessage(mailboxID, recipient, parsed, received)
	if err != nil {
		return Stored{}, err
	}
	return Stored{MessageID: id, Attachments: len(parsed.Attachments)}, nil
}

// Accept applies the single-recipient ingest rules: non-empty, within maxBytes,
// known mailbox, then the same parse and store path as SMTP DATA.
func Accept(st *store.Store, recipient, envelopeFrom string, raw []byte, maxBytes int64, received time.Time) (store.Mailbox, Stored, error) {
	if len(raw) == 0 {
		return store.Mailbox{}, Stored{}, ErrEmpty
	}
	if maxBytes > 0 && int64(len(raw)) > maxBytes {
		return store.Mailbox{}, Stored{}, ErrTooLarge
	}
	mb, err := Lookup(st, recipient)
	if err != nil {
		return store.Mailbox{}, Stored{}, err
	}
	stored, err := StoreRaw(st, mb.ID, mb.Address, envelopeFrom, raw, received)
	if err != nil {
		return store.Mailbox{}, Stored{}, err
	}
	return mb, stored, nil
}
