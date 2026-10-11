package alborz_test

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
	"golang.org/x/net/html"
)

// A page that 500s is invisible to every test that does not open it.
func TestPagesAnswer(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)

	body := c.Page(t, "mail.mailbox", "INBOX")
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var wrap, rail, content *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "aside" && rail == nil {
				rail = n
			}
			for _, a := range n.Attr {
				if a.Key == "class" {
					for _, class := range strings.Fields(a.Val) {
						if class == "page-wrap" {
							wrap = n
						}
						if class == "container" {
							content = n
						}
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if wrap == nil || rail == nil || content == nil || rail.Parent != wrap || content.Parent != wrap {
		t.Fatal("mail sidebar and message list must be siblings inside the page layout")
	}
	for _, route := range []string{"mail.signatures", "mail.signatures.create"} {
		if path := a.Path(route); !strings.HasPrefix(path, a.Path("settings.profile")+"/") {
			t.Errorf("%s is outside settings: %s", route, path)
		}
		if page := c.Page(t, route); !strings.Contains(page, "<aside") {
			t.Errorf("%s has no settings sidebar", route)
		}
	}
	if !strings.Contains(body, "message-row") {
		t.Fatalf("inbox has no messages; the rig seeded none and every other check is vacuous")
	}
	uids := MessageUIDs(body)
	if len(uids) < 4 {
		t.Fatalf("expected the four seeded messages, found %d", len(uids))
	}
	// Without the attachment row, the checks below pass without having run.
	if !strings.Contains(body, "mailbox.attachment") && !strings.Contains(body, "message-mark") {
		t.Fatalf("the seeded attachment leaves no mark on its row; that row is unchecked")
	}

	nestedUIDs := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX/Lists"))
	if len(nestedUIDs) == 0 {
		t.Fatal("the nested folder shows no messages; the escaping is unchecked")
	}
	var nestedParts []string
	for _, uid := range nestedUIDs {
		page := c.Page(t, "mail.message", "INBOX/Lists", uid)
		nestedParts = append(nestedParts, a.RawParts(page)...)
	}
	if len(nestedParts) == 0 {
		t.Fatal("no downloadable part in the nested folder; the escaping is unchecked")
	}
	for _, href := range nestedParts {
		t.Run(href, func(t *testing.T) { c.Follow(t, href) })
	}

	pages := [][]any{
		{"mail.mailbox", "INBOX"}, {"mail.mailbox", "INBOX", url.Values{"view": {"starred"}}},
		{"mail.mailbox", "INBOX", url.Values{"query": {"redesign"}}}, {"mail.mailbox", "INBOX/Lists"},
		{"mail.mailbox", "Drafts"}, {"mail.mailbox", "Junk"}, {"mail.mailbox", "Trash"},
		{"mail.compose"}, {"mail.mailbox.create"}, {"settings.profile"}, {"mail.account"},
		{"settings.servers"}, {"mail.signatures"}, {"mail.signatures.create"},
	}
	for _, uid := range uids {
		pages = append(pages,
			[]any{"mail.message", "INBOX", uid},
			[]any{"mail.message.reply", "INBOX", uid},
			[]any{"mail.message.forward", "INBOX", uid},
			[]any{"mail.messages.forward", "INBOX", url.Values{"uids": {uid}}},
		)
	}
	for _, p := range pages {
		path := a.Path(p[0].(string), p[1:]...)
		t.Run(path, func(t *testing.T) { c.Follow(t, path) })
	}
}

// Both read the message only to decide; opening it is what marks it read.
func TestUnsubscribeAndAddToCalendarLeaveTheMessageUnread(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	ic := a.IMAP.Dial(t)
	a.IMAP.Append(t, "INBOX", "From: <eve@example.org>\r\nTo: <"+User+">\r\nSubject: Planning\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/calendar; charset=utf-8; method=REQUEST\r\n\r\n"+
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//rig//EN\r\nMETHOD:REQUEST\r\n"+
		"BEGIN:VEVENT\r\nUID:planning@rig\r\nDTSTAMP:20260901T090000Z\r\nDTSTART:20261001T090000Z\r\n"+
		"SUMMARY:Planning\r\nORGANIZER:mailto:eve@example.org\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	uidOf := func(subject string) imap.UID {
		t.Helper()
		found, err := ic.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}}}, nil).Wait()
		if err != nil {
			t.Fatal(err)
		}
		uids := found.AllUIDs()
		if len(uids) != 1 {
			t.Fatalf("want one message about %q, found %d", subject, len(uids))
		}
		return uids[0]
	}

	for _, action := range []struct {
		subject, route string
		args           func(imap.UID) []any
	}{
		{"Planning", "calendar.frommessage", func(uid imap.UID) []any {
			return []any{url.Values{"mbox": {"INBOX"}, "uid": {fmt.Sprint(uid)}}}
		}},
		{"Ideas for the redesign", "mail.message.unsubscribe", func(uid imap.UID) []any { return []any{"INBOX", uid} }},
	} {
		uid := uidOf(action.subject)
		c.Post(t, action.route, nil, action.args(uid)...)
		msgs, err := ic.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{Flags: true}).Collect()
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("%s: want the message back, got %d", action.route, len(msgs))
		}
		if slices.Contains(msgs[0].Flags, imap.FlagSeen) {
			t.Errorf("%s marked the message read", action.route)
		}
	}
}

func TestCachingOffServesEveryPage(t *testing.T) {
	c := Alborz(t, NoCache(), WithDAV(DAV(t, Calendar(DAVHome+"personal/", "Personal")))).SignIn(t, User)

	if len(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))) == 0 {
		t.Fatal("the inbox listed no messages")
	}
	unread := url.Values{"view": {"unread"}}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", unread))
	if len(uids) == 0 {
		t.Fatal("no unread message to open")
	}
	uid := uids[0]
	if page := c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}}); !strings.Contains(page, "message-flag-form") {
		t.Fatal("the message page did not render")
	}
	if slices.Contains(MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", unread)), uid) {
		t.Errorf("message %s is still unread after it was opened", uid)
	}
	if !strings.Contains(c.Page(t, "calendar.month"), "Personal") {
		t.Error("the calendar page does not show the server's calendar")
	}
}

// ADR 13: compose, imports and file uploads post as plain forms, never through htmx.
func TestUploadFormsAreNotBoosted(t *testing.T) {
	a := Alborz(t, WithSieve(Sieve(t)))
	c := a.SignIn(t, User)
	book := c.CreateBook(t, "Friends")
	pages := [][]any{
		{"mail.compose"}, {"mail.import"}, {"filters.import"}, {"settings.data"},
		{"calendar.import"}, {"calendar.tasks.import"}, {"contacts.import"},
		{"contacts.contact.create"}, {"contacts.book", Here + book},
	}
	upload := regexp.MustCompile(`<form[^>]*multipart/form-data[^>]*>`)
	for _, p := range pages {
		path := a.Path(p[0].(string), p[1:]...)
		forms := upload.FindAllString(c.Follow(t, path), -1)
		if len(forms) == 0 {
			t.Errorf("%s has no upload form", path)
		}
		for _, form := range forms {
			if !strings.Contains(form, `hx-boost="false"`) {
				t.Errorf("%s has a boosted upload form: %s", path, form)
			}
		}
	}
}
