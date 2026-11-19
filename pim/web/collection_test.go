package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// railed are the collections a page's rail links to, by path.
func railed(t *testing.T, a *App, page string) map[string]bool {
	t.Helper()
	rail := Between(page, `<aside id="page-rail">`, `</aside>`)
	if rail == "" {
		t.Fatal("the page has no rail")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`href="`+a.Pattern("calendar.collection", `(%2F[^"?/]+)`)).FindAllStringSubmatch(rail, -1) {
		p, err := url.PathUnescape(m[1])
		if err != nil {
			t.Fatal(err)
		}
		out[p] = true
	}
	return out
}

// mixed makes User a calendar of events and tasks, a task list and a calendar of events alone, kept here.
func mixed(t *testing.T, c *Client) (personal, chores, work string) {
	t.Helper()
	return Here + c.CreateCalendar(t, "Personal", "both"), Here + c.CreateCalendar(t, "Chores", "tasks"), Here + c.CreateCalendar(t, "Work", "events")
}

// A collection of events and tasks opened from the tasks is a page of the tasks: their rail, their crumb.
func TestACollectionsPageShowsTheSectionItWasOpenedFrom(t *testing.T) {
	a, c := SignedIn(t, User)
	personal, chores, work := mixed(t, c)
	tasks := a.Path("calendar.tasks")

	edit := regexp.MustCompile(`href="(` + a.Pattern("calendar.collection", regexp.QuoteMeta(url.PathEscape(personal))) + `[^"]*)"`)
	link := edit.FindStringSubmatch(c.Page(t, "calendar.tasks"))
	if link == nil {
		t.Fatal("the tasks' rail has no link to the collection's page")
	}
	page := c.Follow(t, strings.ReplaceAll(link[1], "&amp;", "&"))
	if rail := railed(t, a, page); !rail[chores] || rail[work] || !rail[personal] {
		t.Errorf("opened from the tasks, the page's rail lists %v, want the task lists", rail)
	}
	if marked := regexp.MustCompile(`aria-current="true"`).FindAllStringIndex(Between(page, `<aside id="page-rail">`, `</aside>`), -1); len(marked) != 1 ||
		!strings.Contains(Between(page, `<div class="filter-row active">`, `</div>`), ">Personal<") {
		t.Errorf("the rail marks %d places, want the collection alone", len(marked))
	}
	if crumb := Between(page, `<div class="context-crumb">`, `</div>`); !strings.Contains(crumb, `<a href="`+tasks+`?`) {
		t.Errorf("opened from the tasks, the page's crumb is %q", crumb)
	}

	page = c.Page(t, "calendar.collection", personal)
	if rail := railed(t, a, page); rail[chores] || !rail[work] || !rail[personal] {
		t.Errorf("opened from the calendar, the page's rail lists %v, want the calendars", rail)
	}
	// The section is a choice among those the collection shows in, never a place of its own.
	for _, asked := range []string{"tasks", "journal", "//elsewhere.example"} {
		page = c.Page(t, "calendar.collection", work, url.Values{"for": {asked}})
		if rail := railed(t, a, page); rail[chores] || !rail[work] {
			t.Errorf("a calendar of events alone asked for %q lists %v, want the calendars", asked, rail)
		}
	}
}

// The rail of a collection's page leaves none of the section's out: not one unticked, not another account's.
func TestACollectionsPageRailListsEveryCollectionOfItsSection(t *testing.T) {
	a, c := SignedIn(t, User)
	personal, chores, _ := mixed(t, c)
	c.SignIn(t, User2)
	if resp := c.Post(t, "calendar.calendars.create", url.Values{"account": {User2}, "place": {"here"}, "name": {"Errands"}}, url.Values{"for": {"tasks"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the second account's list answered %s", resp.Status)
	}
	errands := Here + CalendarsOf(User2) + "errands/"
	// Chores is unticked: shown in the rail, hidden from the list.
	if resp := c.Post(t, "calendar.tasks", url.Values{"shown": {personal, chores}, "cal": {personal}}, url.Values{"account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("unticking answered %s", resp.Status)
	}
	page := c.Page(t, "calendar.collection", personal, url.Values{"account": {User}, "for": {"tasks"}})
	if rail := railed(t, a, page); !rail[personal] || !rail[chores] || !rail[errands] {
		t.Errorf("the rail lists %v, want every task list of both accounts", rail)
	}
}

// What a collection's page does lands in the section and the scope it was opened from.
func TestACollectionsActionsReturnToTheSectionTheyCameFrom(t *testing.T) {
	a, c := SignedIn(t, User)
	personal, chores, _ := mixed(t, c)
	c.SignIn(t, User2)
	tasks := a.Path("calendar.tasks")
	in := url.Values{"account": {User}, "for": {"tasks"}, "next": {tasks}}
	here := a.Path("calendar.collection", personal, in)

	for _, act := range []struct {
		name, route string
		form        url.Values
	}{
		{"saving", "calendar.collection", url.Values{"name": {"Personal"}, "color": {"#22aa55"}}},
		{"sharing", "calendar.collection.share", url.Values{"to": {User2}, "access": {"read"}}},
		{"changing access", "calendar.collection.access", url.Values{"to": {User2}}},
		{"unsharing", "calendar.collection.unshare", url.Values{"to": {User2}}},
		{"publishing", "calendar.collection.publish", nil},
		{"unpublishing", "calendar.collection.unpublish", nil},
	} {
		resp := c.Post(t, act.route, act.form, personal, in)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s answered %s", act.name, resp.Status)
		}
		if to, _, _ := strings.Cut(resp.Header.Get("Location"), "#"); to != here {
			t.Errorf("%s lands on %s, want the page as it was opened, %s", act.name, to, here)
		}
	}
	if resp := c.Upload(t, "calendar.collection.import", nil, []File{{Field: "file", Name: "plan.ics", Body: strings.NewReader(Event("plan", "Plan"))}}, personal, in); resp.Header.Get("Location") != here {
		t.Errorf("importing lands on %s, want %s", resp.Header.Get("Location"), here)
	}
	if resp := c.Get(t, "calendar.collection.export", personal, in, url.Values{"from": {"2031-01-01"}, "to": {"2031-01-02"}}); resp.Header.Get("Location") != here {
		t.Errorf("an export of nothing lands on %s (%s), want %s", resp.Header.Get("Location"), resp.Status, here)
	}

	// An invitation to a task list is a page of the tasks, and its answer lands there.
	if resp := c.Post(t, "calendar.collection.share", url.Values{"to": {User2}, "access": {"read"}}, chores, url.Values{"account": {User}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("sharing the task list answered %s", resp.Status)
	}
	offered, guest := Here+CalendarsOf(User2)+"chores~"+User+"/", url.Values{"account": {User2}}
	if page := c.Page(t, "calendar.collection", offered, guest); !strings.Contains(Between(page, `<div class="context-crumb">`, `</div>`), `<a href="`+tasks+`?`) {
		t.Error("the invitation to a task list is not a page of the tasks")
	}
	if resp := c.Post(t, "calendar.collection.accept", nil, offered, guest); resp.Header.Get("Location") != tasks+"?account="+User2 {
		t.Errorf("accepting a task list lands on %s (%s), want the tasks", resp.Header.Get("Location"), resp.Status)
	}

	for _, tc := range []struct{ name, next, want string }{
		{"the merged list", tasks, tasks},
		{"one account's list", tasks + "?account=" + User + "&sort=due", tasks + "?account=" + User + "&sort=due"},
		{"the list narrowed to it", tasks + "?cal=" + url.QueryEscape(personal), tasks},
		{"a task opened from the merged list", a.Path("calendar.task", personal+"x.ics") + "?account=" + User + "&from=" + url.QueryEscape(tasks), tasks},
		{"nowhere", "", tasks + "?account=" + User},
	} {
		t.Run("deleted from "+tc.name, func(t *testing.T) {
			doomed := Here + c.CreateCalendar(t, "Doomed", "both")
			q := url.Values{"account": {User}, "for": {"tasks"}}
			if tc.next != "" {
				q.Set("next", tc.next)
			}
			resp := c.Post(t, "calendar.collection.delete", nil, doomed, q)
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("deleting answered %s", resp.Status)
			}
			if to := resp.Header.Get("Location"); to != tc.want {
				t.Errorf("deleting lands on %s, want %s", to, tc.want)
			}
			if page := c.Follow(t, resp.Header.Get("Location")); !Says(t, page, "notice.collectiondeleted", "Doomed") {
				t.Errorf("the page after the delete does not say so: %q", NoticeBar(page))
			}
		})
	}
}
