package alborz_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// Every section's New opens on the account in view, as Compose does.
func TestToolbarNewKeepsTheAccount(t *testing.T) {
	a := Alborz(t)
	for _, account := range []string{User, User2} {
		c := a.SignIn(t, account)
		c.CreateCalendar(t, "Events", "")
		c.CreateBook(t, "People")
		c.Post(t, "calendar.calendars.create", url.Values{"account": {account}, "place": {"here"}, "name": {"Diary"}}, url.Values{"for": {"journal"}})
	}
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	for _, page := range []struct {
		route string
		args  []any
	}{
		{"mail.mailbox", []any{"INBOX"}}, {"calendar.month", nil}, {"calendar.tasks", nil}, {"calendar.journal", nil}, {"contacts.list", nil},
	} {
		for _, account := range []string{User, User2} {
			t.Run(page.route+"/"+account, func(t *testing.T) {
				doc := swipeDocument(t, c.Page(t, page.route, append(page.args, url.Values{"account": {account}})...))
				links := swipeElements(doc, "data-tour", "new")
				if len(links) != 1 {
					t.Fatalf("the toolbar has %d New links", len(links))
				}
				form := swipeDocument(t, c.Follow(t, swipeAttribute(links[0], "href")))
				for _, option := range swipeElements(form, "selected", "") {
					if v := swipeAttribute(option, "value"); option.Data == "option" && (v == account || strings.HasPrefix(v, account+"|")) {
						return
					}
				}
				t.Fatalf("New does not preselect %s", account)
			})
		}
	}
}

func TestDAVAccountRailsExpandWithoutChangingScope(t *testing.T) {
	a := Alborz(t)
	for _, account := range []string{User, User2} {
		c := a.SignIn(t, account)
		c.CreateCalendar(t, "Events", "")
		c.CreateCalendar(t, "Tasks", "tasks")
		c.CreateBook(t, "People")
		if resp := c.Post(t, "calendar.calendars.create", url.Values{"account": {account}, "place": {"here"}, "name": {"Diary"}}, url.Values{"for": {"journal"}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("creating journal: %s", resp.Status)
		}
	}
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	for _, route := range []string{"calendar.month", "calendar.tasks", "calendar.journal", "contacts.list"} {
		for _, scope := range []string{"", User, User2} {
			t.Run(route+"/"+scope, func(t *testing.T) {
				doc := swipeDocument(t, c.Page(t, route, url.Values{"account": {scope}}))
				for _, account := range []string{User, User2} {
					branches := swipeElements(doc, "data-rail-key", account)
					if len(branches) != 1 || branches[0].Data != "details" {
						t.Fatalf("%s has no native expandable collection branch", account)
					}
					branch := branches[0]
					_, open := swipeFindAttribute(branch, "open")
					if open != (account == scope) {
						t.Errorf("%s open = %t with scope %q", account, open, scope)
					}
					forms := swipeElements(branch, "id", "rail-"+account)
					if len(forms) != 1 || len(swipeElements(forms[0], "data-source-item", "")) == 0 {
						t.Fatalf("%s expansion would show no collection controls", account)
					}
					u, err := url.Parse(swipeAttribute(forms[0], "action"))
					if err != nil || u.Query().Get("account") != account {
						t.Errorf("%s collection controls lost their account: %v (%v)", account, u, err)
					}
				}
			})
		}
	}
}
