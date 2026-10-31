package web_test

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
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
		says := html.UnescapeString(Between(c.Page(t, "core.status", "mail", q), "<bdi>", "<"))
		n := 1
		if digits := regexp.MustCompile(`\d[\d,]*`).FindString(says); digits != "" {
			n, _ = strconv.Atoi(strings.ReplaceAll(digits, ",", ""))
		}
		if says != T(t, "mail.status.unread", n) {
			t.Fatalf("no count in %q", says)
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
