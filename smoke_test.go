package alborz_test

import (
	"fmt"
	"html"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"github.com/emersion/go-imap/v2"
)

var crawledLink = regexp.MustCompile(`(?:href|action|src|hx-get)="(/[^"]*)"`)

// html/template writes #ZgotmplZ for a URL it refuses, a link that goes nowhere and says nothing;
// only drawing every page finds one. Each page shape, a route and its parameters' names, is drawn once.
func TestNoPageDrawsARefusedLink(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	calendar, tasks, book := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Chores", "tasks"), c.CreateBook(t, "People")
	a.Put(t, calendar+"crawled.ics", "text/calendar", Event("crawled", "Crawled event"))
	a.Put(t, tasks+"crawled.ics", "text/calendar", Todo("crawled", "Crawled task"))
	a.Put(t, book+"crawled.vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:crawled\r\nFN:Crawled Person\r\nN:Person;Crawled;;;\r\nEND:VCARD\r\n")

	queue := []string{
		a.Path("mail.mailbox", "INBOX"),
		a.Path("core.search", url.Values{"query": {"crawled"}}),
		a.Path("core.search", url.Values{"query": {"text:redesign"}}),
		a.Path("mail.search", url.Values{"query": {"text:redesign"}}),
		a.Path("core.search", url.Values{"view": {"red"}}),
	}
	shapes := map[string]bool{}
	drawn := 0
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		u, err := url.Parse(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		route, _, _ := a.Server.Match(u)
		shape := route + "?" + strings.Join(slices.Sorted(maps.Keys(u.Query())), "&")
		if route == "" || shapes[shape] || route == "auth.logout" {
			continue
		}
		shapes[shape] = true
		req, err := http.NewRequest(http.MethodGet, a.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		page := c.Do(t, req)
		if page.StatusCode != http.StatusOK || !strings.HasPrefix(page.Header.Get("Content-Type"), "text/html") {
			continue
		}
		drawn++
		if strings.Contains(page.Text, "ZgotmplZ") {
			i := strings.Index(page.Text, "ZgotmplZ")
			t.Errorf("%s draws a refused link: %q", path, page.Text[max(0, i-200):i+8])
		}
		for _, m := range crawledLink.FindAllStringSubmatch(page.Text, -1) {
			link := html.UnescapeString(m[1])
			// An empty path segment is a name the page did not have; the router cleans it into another route.
			if before, _, _ := strings.Cut(link, "?"); strings.Contains(before, "//") {
				t.Errorf("%s draws a link with an empty segment: %s", path, link)
			}
			queue = append(queue, link)
		}
	}
	// A crawl that stopped at the first page would pass by drawing nothing.
	if drawn < 40 {
		t.Errorf("the crawl drew %d pages, too few to have walked the sections", drawn)
	}
	t.Logf("drew %d page shapes", drawn)
}

// A page that 500s is invisible to every test that does not open it.
func TestPagesAnswer(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)

	for _, route := range []string{"mail.signatures", "mail.signatures.create"} {
		if path := a.Path(route); !strings.HasPrefix(path, a.Path("settings.profile")+"/") {
			t.Errorf("%s is outside settings: %s", route, path)
		}
		if page := c.Page(t, route); !strings.Contains(page, "<aside") {
			t.Errorf("%s has no settings sidebar", route)
		}
	}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX"))
	if len(uids) < 4 {
		t.Fatalf("expected the four seeded messages, found %d", len(uids))
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

// Reading the invitation only to file it must not mark the message read.
func TestAddToCalendarLeavesTheMessageUnread(t *testing.T) {
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
	c.Page(t, "mail.message", "INBOX", uid, url.Values{"part": {"1"}})
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

var (
	scriptTag     = regexp.MustCompile(`<script\b[^>]*>`)
	inlineHandler = regexp.MustCompile(`<[a-z][^>]*\son[a-z]+\s*=|javascript:`)
)

// The policy allows scripts from files only (default-src 'self'), so inline code would be dead.
func TestTemplatesHaveNoInlineScript(t *testing.T) {
	for _, root := range []string{"templates", "addons"} {
		err := filepath.WalkDir(root, func(f string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(f) != ".html" {
				return err
			}
			b, err := os.ReadFile(f)
			for _, tag := range scriptTag.FindAll(b, -1) {
				if !strings.Contains(string(tag), " src=") {
					t.Errorf("%s has an inline script: %s", f, tag)
				}
			}
			if m := inlineHandler.Find(b); m != nil {
				t.Errorf("%s has inline code: %s", f, m)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
