package web_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
)

func TestTheUnreadSegmentCountsTheInbox(t *testing.T) {
	a := Alborz(t)
	status, err := a.IMAP.Dial(t).Status("INBOX", &imap.StatusOptions{NumUnseen: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	unseen := int(*status.NumUnseen)
	if unseen == 0 {
		t.Fatal("the fixture's inbox holds no unread mail; the count proves nothing")
	}
	c := a.SignIn(t, User)
	count := func(q url.Values) int {
		t.Helper()
		var n int
		shown := c.Page(t, "core.status", "mail", q)
		if _, err := fmt.Sscanf(shown[strings.Index(shown, "<bdi>")+len("<bdi>"):], "%d unread", &n); err != nil {
			t.Fatalf("no count in %q: %v", shown, err)
		}
		return n
	}
	if got := count(nil); got != unseen {
		t.Errorf("one account: %d unread, the server says %d", got, unseen)
	}

	c.SignIn(t, User2)
	second := count(url.Values{"account": {User2}})
	if got := count(url.Values{"account": {User}}); got != unseen {
		t.Errorf("%s: %d unread, the server says %d", User, got, unseen)
	}
	if got := count(nil); got != unseen+second {
		t.Errorf("merged: %d unread, want %d and %d", got, unseen, second)
	}
}
