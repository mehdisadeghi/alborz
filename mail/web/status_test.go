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
		says := shown[strings.Index(shown, "<bdi>")+len("<bdi>"):]
		if strings.HasPrefix(says, "One unread message<") {
			return 1
		}
		if _, err := fmt.Sscanf(says, "%d unread messages", &n); err != nil {
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

func TestTheUnreadSegmentReadsAsTheLanguageCounts(t *testing.T) {
	want := map[int]map[string]string{
		0:    {"en": "No unread messages", "es": "No hay mensajes sin leer", "fa": "نامهٔ خوانده‌نشده‌ای نیست"},
		1:    {"en": "One unread message", "es": "Un mensaje sin leer", "fa": "یک نامهٔ خوانده‌نشده"},
		2:    {"en": "2 unread messages", "es": "2 mensajes sin leer", "fa": "۲ نامهٔ خوانده‌نشده"},
		1234: {"en": "1,234 unread messages", "es": "1.234 mensajes sin leer", "fa": "۱٬۲۳۴ نامهٔ خوانده‌نشده"},
	}
	for unread, says := range want {
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
		for lang, text := range says {
			if shown := c.Page(t, "core.status", "mail", url.Values{"lang": {lang}}); !strings.Contains(shown, `title="`+text+`"`) {
				t.Errorf("%d unread in %s: the segment does not say %q:\n%s", unread, lang, text, shown)
			}
		}
	}
}
