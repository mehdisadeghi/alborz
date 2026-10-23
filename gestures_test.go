package alborz_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"golang.org/x/net/html"
)

// Gesture capabilities describe the controls actually present on each rendered
// list, including row-specific forms and irreversible-delete confirmation.
func TestSwipeActionsFollowRenderedControls(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	events := c.CreateCalendar(t, "Plans", "")
	tasks := c.CreateCalendar(t, "Chores", "tasks")
	if resp := c.Post(t, "calendar.calendars.create", url.Values{"account": {User}, "place": {"here"}, "name": {"Diary"}}, url.Values{"for": {"journal"}}); resp.StatusCode != http.StatusFound {
		t.Fatal(resp.Status)
	}
	book := c.CreateBook(t, "People")
	a.Put(t, events+"visit.ics", "text/calendar", Event("visit", "Visit"))
	a.Put(t, tasks+"water.ics", "text/calendar", Todo("water", "Water"))
	a.Put(t, CalendarsOf(User)+"diary/day.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VJOURNAL\r\nUID:day\r\nDTSTART;VALUE=DATE:20260915\r\nSUMMARY:A day\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n")
	a.Put(t, book+"person.vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:person\r\nFN:Person\r\nEND:VCARD\r\n")
	for _, tc := range []struct {
		kind, route, form string
		args              []any
	}{
		{"mail", "mail.mailbox", "messages-form", []any{"INBOX"}},
		{"tasks", "calendar.tasks", "tasks-form", nil},
		{"contacts", "contacts.list", "address-book-form", nil},
		{"journal", "calendar.journal", "journal-form", nil},
		{"events", "calendar.month", "events-form", []any{url.Values{"month": {"2026-09"}, "view": {"list"}, "span": {"month"}}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			doc := swipeDocument(t, c.Page(t, tc.route, tc.args...))
			rows := swipeElements(doc, "data-swipe-kind", tc.kind)
			if len(rows) == 0 {
				t.Fatal("the populated list has no swipe rows")
			}
			for _, row := range rows {
				stars := swipeElements(row, "data-swipe-action", "star")
				if len(stars) != 1 {
					t.Error("row has no unique star control")
				} else {
					form := stars[0].Parent
					for form != nil && form.Data != "form" {
						form = form.Parent
					}
					if form == nil || swipeAttribute(form, "action") == "" {
						t.Error("row star has no action form")
					}
				}
				if tc.kind == "tasks" && len(swipeElements(row, "data-swipe-action", "complete")) != 1 {
					t.Error("task row has no completion control")
				}
			}
			var remove *html.Node
			for _, node := range swipeElements(doc, "data-swipe-action", "delete") {
				if swipeAttribute(node, "form") == tc.form {
					remove = node
				}
			}
			if remove == nil {
				t.Fatal("delete capability has no control bound to the list's form")
			}
			_, confirm := swipeFindAttribute(remove, "data-swipe-confirm")
			if confirm != (tc.kind != "mail") {
				t.Errorf("delete confirmation = %v", confirm)
			}
			if tc.kind == "mail" {
				for _, action := range []string{"archive", "junk"} {
					if len(swipeElements(doc, "data-swipe-action", action)) == 0 {
						t.Errorf("mail has no %s action", action)
					}
				}
			}
		})
	}
	trash := swipeDocument(t, c.Page(t, "mail.mailbox", "Trash"))
	if len(swipeElements(trash, "formaction", a.Path("mail.messages.delete", "Trash"))) == 0 {
		t.Fatal("the Trash page has no permanent-delete control; its exclusion is untested")
	}
	if nodes := swipeElements(trash, "data-swipe-action", "delete"); len(nodes) != 0 {
		t.Fatal("permanent mail deletion is exposed as a swipe action")
	}
}

func TestSwipePagerKeepsItsListScope(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	q := url.Values{"account": {User}, "ipp": {"1"}, "page": {"1"}, "sort": {"subject"}, "dir": {"desc"}}
	doc := swipeDocument(t, c.Page(t, "mail.mailbox", "INBOX", q))
	if len(swipeElements(doc, "data-swipe-nav", "")) == 0 {
		t.Fatal("list pager has no swipe surface")
	}
	for step, page := range map[string]string{"prev": "", "next": "2"} {
		links := swipeElements(doc, "data-swipe-step", step)
		if len(links) != 1 {
			t.Fatalf("%s has %d pager links", step, len(links))
		}
		u, err := url.Parse(swipeAttribute(links[0], "href"))
		if err != nil || u.Query().Get("page") != page {
			t.Fatalf("%s pager URL = %v, %v", step, u, err)
		}
		for _, key := range []string{"account", "ipp", "sort", "dir"} {
			if u.Query().Get(key) != q.Get(key) {
				t.Errorf("%s pager lost %s", step, key)
			}
		}
	}
	first := swipeDocument(t, c.Page(t, "mail.mailbox", "INBOX", url.Values{"ipp": {"1"}}))
	previous := swipeElements(first, "data-swipe-step", "prev")
	if len(previous) != 1 || previous[0].Data != "span" || swipeAttribute(previous[0], "aria-disabled") != "true" || swipeAttribute(previous[0], "href") != "" {
		t.Fatal("the first page does not expose a disabled previous step")
	}
}

func TestCalendarSwipeNavigationKeepsItsScope(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	first, second := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Family", "")
	q := url.Values{"account": {User}, "cal": {Here + first, Here + second}}
	for _, tc := range []struct {
		route, field, value, previous, next string
	}{
		{"calendar.month", "month", "2026-09", "2026-08", "2026-10"},
		{"calendar.day", "date", "2026-09-15", "2026-09-14", "2026-09-16"},
	} {
		t.Run(tc.route, func(t *testing.T) {
			doc := swipeDocument(t, c.Page(t, tc.route, q, url.Values{tc.field: {tc.value}}))
			if len(swipeElements(doc, "data-swipe-calendar", "")) != 1 {
				t.Fatal("calendar period has no unique gesture surface")
			}
			if tc.route == "calendar.month" {
				days := swipeElements(doc, "class", "date-link")
				if len(days) == 0 {
					t.Fatal("month has no day links")
				}
				u, err := url.Parse(swipeAttribute(days[0], "href"))
				if err != nil || u.Query().Get("account") != User || strings.Join(u.Query()["cal"], "|") != strings.Join(q["cal"], "|") {
					t.Errorf("opening a day lost its month scope: %v (%v)", u, err)
				}
			}
			for step, period := range map[string]string{"prev": tc.previous, "next": tc.next} {
				links := swipeElements(doc, "data-swipe-step", step)
				if len(links) != 1 {
					t.Fatalf("%s has %d navigation links", step, len(links))
				}
				u, err := url.Parse(swipeAttribute(links[0], "href"))
				if err != nil || u.Query().Get(tc.field) != period || u.Query().Get("account") != User || strings.Join(u.Query()["cal"], "|") != strings.Join(q["cal"], "|") {
					t.Errorf("%s URL lost its period or scope: %v (%v)", step, u, err)
				}
			}
		})
	}
	list := swipeDocument(t, c.Page(t, "calendar.month", q, url.Values{"month": {"2026-09"}, "view": {"list"}}))
	if len(swipeElements(list, "data-swipe-calendar", "")) != 0 {
		t.Error("agenda background gestures would compete with event row gestures")
	}
}

func swipeDocument(t *testing.T, text string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func swipeFindAttribute(node *html.Node, key string) (string, bool) {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func swipeAttribute(node *html.Node, key string) string {
	value, _ := swipeFindAttribute(node, key)
	return value
}

func swipeElements(node *html.Node, key, value string) []*html.Node {
	var found []*html.Node
	if attr, ok := swipeFindAttribute(node, key); ok && attr == value {
		found = append(found, node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		found = append(found, swipeElements(child, key, value)...)
	}
	return found
}
