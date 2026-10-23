package web_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
)

func TestAnUncachedReadConfirmsSeenOnTheServer(t *testing.T) {
	a := Alborz(t, NoCache(), WithIMAP(IMAP(t, FetchKeepsUnread())))
	uid := a.IMAP.Append(t, "INBOX", "From: eve@example.org\r\nSubject: Read me\r\n\r\nHello.\r\n")
	c := a.SignIn(t, User)
	c.Page(t, "mail.message", "INBOX", uid)
	other := a.IMAP.Dial(t)
	if _, err := other.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := other.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{Flags: true}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("reading the flags: %v (%d messages)", err, len(msgs))
	}
	if !slices.Contains(msgs[0].Flags, imap.FlagSeen) {
		t.Fatal("opening the message left it unread on the mail server")
	}
}

func TestAReadOnlyMessageStaysReadableAndUnread(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) {
			opts := []Option{WithIMAP(IMAP(t, ReadOnlyMailboxes()))}
			if !cached {
				opts = append(opts, NoCache())
			}
			a := Alborz(t, opts...)
			uid := a.IMAP.Append(t, "INBOX", "From: eve@example.org\r\nSubject: Read-only message\r\n\r\nStill readable.\r\n")
			c := a.SignIn(t, User)
			c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"subject:Read-only message"}})
			if page := c.Page(t, "mail.message", "INBOX", uid); !strings.Contains(page, "Still readable.") {
				t.Fatal("the read-only message body was not shown")
			}
			other := a.IMAP.Dial(t)
			if _, err := other.Select("INBOX", nil).Wait(); err != nil {
				t.Fatal(err)
			}
			msgs, err := other.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{Flags: true}).Collect()
			if err != nil || len(msgs) != 1 {
				t.Fatalf("reading the flags: %v (%d messages)", err, len(msgs))
			}
			if slices.Contains(msgs[0].Flags, imap.FlagSeen) {
				t.Fatal("opening a read-only message marked it read on the server")
			}
			page := c.Page(t, "mail.mailbox", "INBOX", url.Values{"query": {"subject:Read-only message"}})
			if !strings.Contains(page, "message-list-unread") {
				t.Fatal("the listing incorrectly marks the read-only message read")
			}
		})
	}
}
