package web_test

import (
	"fmt"
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

func TestTheUnreadSegmentReadsAsTheLanguageCounts(t *testing.T) {
	written := map[int]map[string]string{
		0:    {"en": "", "es": "", "fa": ""},
		1:    {"en": "", "es": "", "fa": ""},
		2:    {"en": "2", "es": "2", "fa": "۲"},
		1234: {"en": "1,234", "es": "1.234", "fa": "۱٬۲۳۴"},
	}
	for unread, numbers := range written {
		a := Alborz(t)
		ic := a.IMAP.Dial(t)
		if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
			t.Fatal(err)
		}
		var all imap.SeqSet
		all.AddRange(1, 0)
		if err := ic.Store(all, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
			t.Fatal(err)
		}
		for i := range unread {
			a.IMAP.Append(t, "INBOX", fmt.Sprintf("From: eve@example.org\r\nTo: %s\r\nSubject: %d\r\n\r\nHello.\r\n", User, i))
		}
		c := a.SignIn(t, User)
		for lang, number := range numbers {
			text := TIn(t, lang, "mail.status.none")
			if unread > 0 {
				text = TIn(t, lang, "mail.status.unread", unread)
			}
			if !strings.Contains(text, number) {
				t.Errorf("%d unread in %s: %q does not write the count as %s", unread, lang, text, number)
			}
			if shown := c.Page(t, "core.status", "mail", url.Values{"lang": {lang}}); !strings.Contains(html.UnescapeString(shown), `title="`+text+`"`) {
				t.Errorf("%d unread in %s: the segment does not say %q:\n%s", unread, lang, text, shown)
			}
		}
	}
}
