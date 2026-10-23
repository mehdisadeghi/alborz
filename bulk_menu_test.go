package alborz_test

import (
	"net/http"
	"net/url"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestDAVListsOfferCopyForTheirSelection(t *testing.T) {
	a, c := SignedIn(t, User)
	events := c.CreateCalendar(t, "Plans", "")
	tasks := c.CreateCalendar(t, "Chores", "tasks")
	book := c.CreateBook(t, "People")
	if resp := c.Post(t, "calendar.calendars.create", url.Values{"account": {User}, "place": {"here"}, "name": {"Diary"}}, url.Values{"for": {"journal"}}); resp.StatusCode != http.StatusFound {
		t.Fatal(resp.Status)
	}
	a.Put(t, events+"visit.ics", "text/calendar", Event("visit", "Visit"))
	a.Put(t, tasks+"water.ics", "text/calendar", Todo("water", "Water"))
	a.Put(t, book+"person.vcf", "text/vcard", "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:person\r\nFN:Person\r\nEND:VCARD\r\n")
	for _, tc := range []struct {
		route, form, action string
		args                []any
	}{
		{"calendar.month", "events-form", "/calendar/copy", []any{url.Values{"month": {"2026-09"}, "view": {"list"}}}},
		{"calendar.search", "events-form", "/calendar/copy", []any{url.Values{"query": {"Visit"}}}},
		{"calendar.tasks", "tasks-form", "/tasks/copy", nil},
		{"calendar.journal", "journal-form", "/journal/copy", nil},
		{"contacts.list", "address-book-form", "/contacts/copy", nil},
	} {
		t.Run(tc.route, func(t *testing.T) {
			doc := swipeDocument(t, c.Page(t, tc.route, tc.args...))
			buttons := swipeElements(doc, "formaction", tc.action)
			if len(buttons) != 1 {
				t.Fatalf("list has %d bulk Copy controls, want 1", len(buttons))
			}
			button := buttons[0]
			if button.Data != "button" || swipeAttribute(button, "form") != tc.form || swipeAttribute(button, "formmethod") != "get" {
				t.Fatal("Copy does not open the destination chooser with the list selection")
			}
			menu := button.Parent
			for menu != nil && swipeAttribute(menu, "id") != "menu-more" {
				menu = menu.Parent
			}
			if menu == nil {
				t.Fatal("bulk Copy is missing from the list's More menu")
			}
		})
	}
}
