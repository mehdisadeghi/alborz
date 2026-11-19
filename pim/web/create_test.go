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

// places are the values of a create form's one picker.
func places(t *testing.T, form string) []string {
	t.Helper()
	if strings.Contains(form, `name="account"`) {
		t.Error("the form asks for the account apart from the place")
	}
	picker := regexp.MustCompile(`(?s)<select[^>]*name="place"[^>]*>(.*?)</select>`).FindStringSubmatch(form)
	if picker == nil {
		t.Fatal("the form has no picker of places")
	}
	var out []string
	for _, m := range regexp.MustCompile(`<option value="([^"]+)"`).FindAllStringSubmatch(picker[1], -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// A collection made from a rail goes where the form's one picker says and shows where the reader lands; a
// form sent without a place it had to ask for comes back saying so, the name kept.
func TestANewCollectionShowsWhereTheReaderLands(t *testing.T) {
	for _, k := range []struct {
		name, list, link, page string
		home, remote           string
		// shown is the list a collection the remote server made shows in: the test server makes every
		// calendar one of events and tasks, and lists no address book it was asked to make.
		shown string
	}{
		{"task list", "calendar.tasks", "tasks.newlist", "calendar.collection", CalendarsOf(User2), DAVHome, "calendar.tasks"},
		{"journal", "calendar.journal", "journal.newjournal", "calendar.collection", CalendarsOf(User2), DAVHome, "calendar.month"},
		{"calendar", "calendar.month", "calendar.newcalendar", "calendar.collection", CalendarsOf(User2), DAVHome, "calendar.month"},
		{"address book", "contacts.list", "contacts.newbook", "contacts.book", BooksOf(User2), DAVBooks, ""},
	} {
		t.Run(k.name, func(t *testing.T) {
			d := DAV(t, Calendar(DAVHome+"personal/", "Personal"), Book(DAVBooks+"people/", "People"))
			a, c := SignedIn(t, User, WithDAV(d))
			c.SignIn(t, User2)
			// The form is the one the rail's own link opens.
			open := func(scope url.Values) (list, form string) {
				t.Helper()
				list = a.Path(k.list, scope)
				link := regexp.MustCompile(`<a href="([^"]+)" class="aside-create[^"]*">` + regexp.QuoteMeta(T(t, k.link)) + `</a>`).FindStringSubmatch(c.Follow(t, list))
				if link == nil {
					t.Fatalf("the rail of %s has no link to the form", list)
				}
				return list, html.UnescapeString(link[1])
			}
			listed := func(page, path string) bool {
				return strings.Contains(Between(page, `<aside id="page-rail">`, `</aside>`), a.Path(k.page, path))
			}

			list, form := open(nil)
			page := c.Follow(t, form)
			offered := places(t, page)
			for _, want := range []string{User + "|here", User + "|domain", User2 + "|here", User2 + "|domain"} {
				if n := strings.Count(strings.Join(offered, "\n")+"\n", want+"\n"); n != 1 {
					t.Errorf("the picker offers %q %d times among %q", want, n, offered)
				}
			}
			if chosen := SelectedValue(page, "place"); chosen != "" {
				t.Errorf("among every account the picker starts on %q, want the question", chosen)
			}
			resp := c.PostTo(t, form, url.Values{"name": {"Unplaced"}, "color": {"#22aa55"}, "place": {""}})
			if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, NoticeBar(resp.Text), "form.destinationneeded") || !strings.Contains(resp.Text, `value="Unplaced"`) {
				t.Errorf("sent with no place the form answered %s, notice %q", resp.Status, NoticeBar(resp.Text))
			}
			resp = c.PostTo(t, form, url.Values{"name": {"Groceries"}, "color": {"#22aa55"}, "place": {User2 + "|here"}})
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != list {
				t.Fatalf("made among every account it answered %s to %s, want %s", resp.Status, resp.Header.Get("Location"), list)
			}
			if page := c.Follow(t, list); !strings.Contains(NoticeBar(page), "Groceries") || !listed(page, Here+k.home+"groceries/") {
				t.Errorf("the second account's new %s is not on the page the reader lands on: %q", k.name, NoticeBar(page))
			}

			// In one account's list the picker starts on that account's default place, and what is made lands there.
			list, form = open(url.Values{"account": {User2}})
			page = c.Follow(t, form)
			if chosen := SelectedValue(page, "place"); chosen != User2+"|domain" {
				t.Fatalf("in the second account's list the picker starts on %q", chosen)
			}
			resp = c.PostTo(t, form, url.Values{"name": {"Errands"}, "color": {"#22aa55"}, "place": {User2 + "|domain"}})
			if k.shown == "" {
				if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, NoticeBar(resp.Text), "form.collectionnotlisted", "Errands") || !strings.Contains(resp.Text, `value="Errands"`) {
					t.Errorf("a collection the server does not list answered %s, notice %q", resp.Status, NoticeBar(resp.Text))
				}
				return
			}
			want := a.Path(k.shown, url.Values{"account": {User2}})
			if k.shown == k.list {
				want = list
			}
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
				t.Fatalf("made in one account's list it answered %s to %s, want %s", resp.Status, resp.Header.Get("Location"), want)
			}
			if page := c.Follow(t, want); !strings.Contains(NoticeBar(page), "Errands") || !listed(page, d.At()+k.remote+"errands/") {
				t.Errorf("the new %s is not on the page the reader lands on: %q", k.name, NoticeBar(page))
			}

			// Another account's place chosen in one account's list lands in the other's, where it shows.
			resp = c.PostTo(t, form, url.Values{"name": {"Away"}, "color": {"#22aa55"}, "place": {User + "|here"}})
			if want := a.Path(k.list, url.Values{"account": {User}}); resp.Header.Get("Location") != want {
				t.Errorf("made in the other account it answered %s to %s, want %s", resp.Status, resp.Header.Get("Location"), want)
			}
		})
	}
}
