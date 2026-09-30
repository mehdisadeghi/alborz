package web_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// sections gives User an event, a task and a contact for each summary, in collections of their own,
// and is where each one lives as a page names it.
func sections(t *testing.T, a *App, c *Client, summaries ...string) map[string][3]string {
	t.Helper()
	calendar, tasks, book := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Chores", "tasks"), c.CreateBook(t, "People")
	out := map[string][3]string{}
	for i, summary := range summaries {
		uid := "s" + string(rune('a'+i))
		a.Put(t, calendar+uid+".ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
			"BEGIN:VEVENT\r\nUID:"+uid+"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260915T100000Z\r\nDTEND:20260915T110000Z\r\n"+
			"SUMMARY:"+summary+" event\r\nLOCATION:Hall "+summary+"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
		a.Put(t, tasks+uid+".ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
			"BEGIN:VTODO\r\nUID:"+uid+"\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:"+summary+" task\r\n"+
			"DESCRIPTION:About "+summary+"\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
		a.Put(t, book+uid+".vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:"+uid+"\r\nFN:"+summary+
			" Person\r\nN:Person;"+summary+";;;\r\nEND:VCARD\r\n")
		out[summary] = [3]string{Here + calendar + uid + ".ics", Here + tasks + uid + ".ics", Here + book + uid + ".vcf"}
	}
	return out
}

var foundLink = regexp.MustCompile(`<p class="list-option"><a href="([^"]+)">`)

// block is one section's part of the search page, and the address of that section's own list of it.
func block(t *testing.T, page, kind string) (string, string) {
	t.Helper()
	b := Between(page, `id="search-`+kind+`"`, "</section>")
	if b == "" {
		t.Fatalf("the search page has no %s block", kind)
	}
	all := ""
	if m := foundLink.FindStringSubmatch(b); m != nil {
		all = html.UnescapeString(m[1])
	}
	return b, all
}

var kinds = []string{"mail", "events", "tasks", "contacts"}

// One view, four stores that keep a star each their own way: flag bits, COLOR and X-ALBORZ-COLOR.
func TestSearchFindsARedStarInEverySection(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	made := sections(t, a, c, "Marked", "Plain")
	acct := url.Values{"account": {User}}
	for _, route := range []string{"calendar.event.color", "calendar.task.color", "contacts.contact.color"} {
		i := map[string]int{"calendar.event.color": 0, "calendar.task.color": 1, "contacts.contact.color": 2}[route]
		if resp := c.Post(t, route, url.Values{"color": {"red"}, "next": {"/"}}, made["Marked"][i], acct); resp.StatusCode != http.StatusFound {
			t.Fatalf("%s answered %s", route, resp.Status)
		}
	}
	inbox := c.Page(t, "mail.mailbox", "INBOX")
	uids := MessageUIDs(inbox)
	if len(uids) < 2 {
		t.Fatalf("the rig seeded %d messages, want two at least", len(uids))
	}
	c.Post(t, "mail.messages.flag", url.Values{"uids": {uids[0]}, "color": {"red"}, "next": {"/"}}, "INBOX")

	page := c.Page(t, "core.search", url.Values{"view": {"red"}})
	for _, kind := range kinds {
		b, all := block(t, page, kind)
		if all == "" || !strings.Contains(b, "1 result in") {
			t.Errorf("%s: want one red row and its list, got %q", kind, b)
			continue
		}
		if kind != "mail" && (!strings.Contains(b, "Marked") || strings.Contains(b, "Plain")) {
			t.Errorf("%s shows other than the red one: %q", kind, b)
		}
		if strings.Contains(b, `type="checkbox"`) {
			t.Errorf("%s's rows offer a selection: %q", kind, b)
		}
		if !strings.Contains(all, "view=red") {
			t.Errorf("%s's list leaves the view: %q", kind, all)
		}
		if list := c.Follow(t, all); !strings.Contains(list, "flag-red") {
			t.Errorf("%s's own list at %s shows no red star", kind, all)
		}
	}
	if mail, _ := block(t, page, "mail"); strings.Count(mail, `class="message-row`) != 1 {
		t.Errorf("mail shows %d rows for one red message", strings.Count(mail, `class="message-row`))
	}
}

// A word is matched by IMAP SEARCH, by the task and event fields, and by a CardDAV query.
func TestSearchFindsAWordInEverySection(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	sections(t, a, c, "Lantern", "Kettle")

	// Capitalised: alborz's own CardDAV store matches text case-sensitively.
	page := c.Page(t, "core.search", url.Values{"query": {"Lantern"}})
	for _, kind := range []string{"events", "tasks", "contacts"} {
		if b, _ := block(t, page, kind); !strings.Contains(b, "Lantern") || strings.Contains(b, "Kettle") {
			t.Errorf("%s does not show the one with the word alone: %q", kind, b)
		}
	}
	// Seeded mail says nothing of lanterns; Eve's message is found by her name.
	if b, _ := block(t, page, "mail"); !strings.Contains(b, "Nothing found") {
		t.Errorf("mail found lanterns: %q", b)
	}
	if b, _ := block(t, c.Page(t, "core.search", url.Values{"query": {"eve"}}), "mail"); !strings.Contains(b, "Ideas for the redesign") {
		t.Errorf("mail does not find Eve's message: %q", b)
	}
	// The words are in a task's description and an event's place, not their summaries.
	page = c.Page(t, "core.search", url.Values{"query": {"hall kettle"}})
	if b, _ := block(t, page, "events"); !strings.Contains(b, "Kettle event") {
		t.Errorf("an event is not found by its place: %q", b)
	}
	if b, _ := block(t, c.Page(t, "core.search", url.Values{"query": {"about kettle"}}), "tasks"); !strings.Contains(b, "Kettle task") {
		t.Errorf("a task is not found by its description: %q", b)
	}
}

// A task that says "from:eve" would match as text; a section that cannot read the key is not asked.
func TestSearchSaysWhereAKeyWasNotSearched(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	sections(t, a, c, "from:eve")

	page := c.Page(t, "core.search", url.Values{"query": {"from:eve"}})
	for _, kind := range []string{"events", "tasks", "contacts"} {
		b, _ := block(t, page, kind)
		if !strings.Contains(b, "Not searched:") || !strings.Contains(b, "from:</bdi>") || strings.Contains(b, "from:eve task") {
			t.Errorf("%s does not say it was not searched for from: %q", kind, b)
		}
	}
	b, all := block(t, page, "mail")
	if !strings.Contains(b, "Ideas for the redesign") {
		t.Fatalf("mail does not find Eve's message: %q", b)
	}
	// The mail search moved under /mail; the list the block names is it, and still answers.
	if !strings.HasPrefix(all, a.Path("mail.search")+"?") || a.Path("mail.search") == a.Path("core.search") {
		t.Errorf("mail's list is %q", all)
	}
	if list := c.Follow(t, all); !strings.Contains(list, "Ideas for the redesign") {
		t.Errorf("mail's own search at %s does not find Eve's message", all)
	}
}
