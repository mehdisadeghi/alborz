package web_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
)

func badge(t *testing.T, page string) int {
	t.Helper()
	_, rest, ok := strings.Cut(page, `data-unread="`)
	n, _, _ := strings.Cut(rest, `"`)
	count, err := strconv.Atoi(n)
	if !ok || err != nil {
		t.Fatalf("the rail carries no unread count: %q", n)
	}
	return count
}

// The rail carries the unread count of every signed-in account's inbox for the app's badge, and a
// refresh of it follows new mail; read mail does not count.
func TestTheRailCarriesTheUnreadCount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	one := badge(t, c.Page(t, "mail.mailbox", "INBOX"))
	c.SignIn(t, User2)
	before := badge(t, c.Page(t, "mail.mailbox", "INBOX"))
	if one == 0 || before <= one {
		t.Fatalf("one account counts %d, two %d: the seeded inboxes hold unread mail", one, before)
	}
	a.IMAP.Append(t, "INBOX", "Subject: Unread\r\n\r\nHello\r\n")
	a.IMAP.Append(t, "INBOX", "Subject: Read\r\n\r\nHello\r\n", imap.FlagSeen)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		now := badge(t, c.Page(t, "mail.sidebar"))
		if now == before+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rail counts %d unread, want %d", now, before+1)
		}
	}
}
