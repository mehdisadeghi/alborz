package web_test

import (
	"fmt"
	"html"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// landed is a list narrowed every way it can be, and the actions its page offers.
type landed struct {
	name, list, object string
	// held and other are the narrowed collection and one beside it, field the parameter naming
	// one, shown the name the rail marks it by.
	held, other, field, shown string
	ext, mime                 string
	body                      func(uid, title string) string
	query                     url.Values
	// actions are the toolbar's on the checked rows; settles are the object page's that leave it.
	actions []landing
	settles []string
	search  bool
}

// landing is a toolbar action by its route, or a form posted whole by its id; a GET one opens a
// page that posts.
type landing struct {
	route, id string
	get       bool
	form      url.Values
}

type pageForm struct {
	id, class, action string
	fields            url.Values
}

var (
	formTag   = regexp.MustCompile(`(?s)<form\b([^>]*)>(.*?)</form>`)
	idAttr    = regexp.MustCompile(`\bid="([^"]*)"`)
	actAttr   = regexp.MustCompile(`\baction="([^"]*)"`)
	classAttr = regexp.MustCompile(`\bclass="([^"]*)"`)
	fieldTag  = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`)
	buttonTag = regexp.MustCompile(`<button\b[^>]*\bform="([^"]+)"[^>]*\bformaction="([^"]+)"`)
	hrefAttr  = regexp.MustCompile(`<a\b[^>]*\bhref="([^"]+)"`)
)

// forms are a page's forms, with their hidden fields, as a browser would submit them.
func forms(page string) []pageForm {
	var out []pageForm
	for _, m := range formTag.FindAllStringSubmatch(page, -1) {
		f := pageForm{fields: url.Values{}}
		if a := idAttr.FindStringSubmatch(m[1]); a != nil {
			f.id = html.UnescapeString(a[1])
		}
		if a := actAttr.FindStringSubmatch(m[1]); a != nil {
			f.action = html.UnescapeString(a[1])
		}
		if a := classAttr.FindStringSubmatch(m[1]); a != nil {
			f.class = html.UnescapeString(a[1])
		}
		for _, in := range fieldTag.FindAllStringSubmatch(m[2], -1) {
			f.fields.Add(html.UnescapeString(in[1]), html.UnescapeString(in[2]))
		}
		out = append(out, f)
	}
	return out
}

// formWith is the page's first form that match accepts.
func formWith(page string, match func(pageForm) bool) (pageForm, bool) {
	for _, f := range forms(page) {
		if match(f) {
			return f, true
		}
	}
	return pageForm{}, false
}

func pathOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path
}

// sameAddress says whether two local addresses name the same page with the same query.
func sameAddress(t *testing.T, got, want string) bool {
	t.Helper()
	g, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := url.Parse(want)
	if err != nil {
		t.Fatal(err)
	}
	return g.Path == w.Path && g.Query().Encode() == w.Query().Encode()
}

// An action lands on the list it left, with the same query, except what the action itself changes:
// from the toolbar, a row, or the page of an object opened from the list; whether the list was
// loaded whole or reached by a swap into main, as the rail's links and the list's refinements reach
// it; in the merged view and in one account's.
func TestActionsLandOnTheListTheyLeft(t *testing.T) {
	a, c := SignedIn(t, User)
	c.SignIn(t, User2)
	month := time.Now().UTC().Format("2006-01")
	kinds := []landed{
		{name: "tasks", list: "calendar.tasks", object: "calendar.task", field: "cal", shown: "Chores",
			held: c.CreateCalendar(t, "Chores", "tasks"), other: c.CreateCalendar(t, "Errands", "tasks"),
			ext: ".ics", mime: "text/calendar", body: Todo,
			query: url.Values{"view": {"all"}, "sort": {"summary"}, "dir": {"desc"}, "ipp": {"1"}, "page": {"1"}, "query": {"Kept"}},
			actions: []landing{{route: "calendar.tasks.complete"}, {route: "calendar.tasks.move", get: true},
				{route: "calendar.tasks.copy", get: true}, {route: "calendar.tasks.delete"}, {id: "refresh-form"}},
			settles: []string{"calendar.task.delete"}, search: true},
		{name: "journal", list: "calendar.journal", object: "calendar.entry", field: "cal", shown: "Diary",
			held: c.CreateCalendar(t, "Diary", "journal"), other: c.CreateCalendar(t, "Notes", "journal"),
			ext: ".ics", mime: "text/calendar", body: entryToday,
			query:   url.Values{"sort": {"summary"}, "dir": {"desc"}, "ipp": {"1"}, "page": {"1"}, "query": {"Kept"}},
			actions: []landing{{route: "calendar.journal.copy", get: true}, {route: "calendar.journal.delete"}, {id: "refresh-form"}},
			settles: []string{"calendar.entry.delete"}, search: true},
		{name: "contacts", list: "contacts.list", object: "contacts.contact", field: "book", shown: "People",
			held: c.CreateBook(t, "People"), other: c.CreateBook(t, "Clubs"),
			ext: ".vcf", mime: "text/vcard", body: card,
			query: url.Values{"sort": {"name"}, "dir": {"desc"}, "ipp": {"1"}, "page": {"1"}, "query": {"Kept"}},
			actions: []landing{{route: "contacts.color", form: url.Values{"color": {"red"}}}, {route: "contacts.copy", get: true},
				{route: "contacts.delete"}, {id: "refresh-form"}},
			settles: []string{"contacts.contact.delete"}, search: true},
		{name: "agenda", list: "calendar.month", object: "calendar.event", field: "cal", shown: "Plans",
			held: c.CreateCalendar(t, "Plans", ""), other: c.CreateCalendar(t, "Trips", ""),
			ext: ".ics", mime: "text/calendar", body: eventToday,
			query:   url.Values{"view": {"list"}, "span": {"month"}, "month": {month}, "group": {"day"}},
			actions: []landing{{route: "calendar.events.copy", get: true}, {route: "calendar.events.delete"}, {id: "refresh-form"}},
			settles: []string{"calendar.event.delete"}},
	}
	for _, k := range kinds {
		k.held, k.other = Here+k.held, Here+k.other
		for _, scope := range []url.Values{nil, {"account": {User}}} {
			for _, swapped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/scope=%v/swapped=%v", k.name, scope, swapped), func(t *testing.T) {
					land(t, a, c, k, scope, swapped)
				})
			}
		}
	}
}

func land(t *testing.T, a *App, c *Client, k landed, scope url.Values, swapped bool) {
	put := func() {
		// A client reaches the collection by its path on alborz; pages hold it under Here.
		held, other := strings.TrimPrefix(k.held, Here), strings.TrimPrefix(k.other, Here)
		for _, uid := range []string{"one", "two", "three"} {
			a.Put(t, held+uid+k.ext, k.mime, k.body(uid, "Kept "+uid))
		}
		a.Put(t, other+"other"+k.ext, k.mime, k.body("other", "Kept other"))
	}
	put()
	narrowed := url.Values{k.field: {k.held}}
	maps.Copy(narrowed, k.query)
	address := a.Path(k.list, scope, narrowed)
	page := c.Follow(t, address)
	if swapped {
		// The rail narrowed the list from the page before it.
		page = Swapped(c.Page(t, k.list, scope, k.query), c.Swap(t, address).Text)
	}
	if !strings.Contains(page, "Kept") {
		t.Fatalf("the narrowed list shows none of its rows")
	}
	// Whatever on the page names where to come back to names the list it shows.
	for _, next := range Returns(page) {
		if !sameAddress(t, next, address) {
			t.Errorf("the page returns to %q, not to the list it shows, %q", next, address)
		}
	}
	lands := func(what string, resp *Response) {
		t.Helper()
		defer put()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("%s answered %s", what, resp.Status)
			return
		}
		got := resp.Header.Get("Location")
		if !sameAddress(t, got, address) {
			t.Errorf("%s lands on %q, not on the list it left, %q", what, got, address)
		} else if !strings.Contains(c.Follow(t, got), `title="`+k.shown+`" aria-current="true"`) {
			t.Errorf("%s lands on a list the rail does not mark as narrowed to %s", what, k.shown)
		}
	}

	// The page the list is on shows the third.
	acted := k.held + "three" + k.ext
	ref := User + "|" + acted
	for _, act := range k.actions {
		id, path := act.id, ""
		if act.route != "" {
			for _, m := range buttonTag.FindAllStringSubmatch(page, -1) {
				if action := html.UnescapeString(m[2]); pathOf(t, action) == a.Path(act.route) {
					id, path = html.UnescapeString(m[1]), action
				}
			}
		}
		what := act.route + act.id
		form, ok := formWith(page, func(f pageForm) bool { return id != "" && f.id == id })
		if !ok {
			t.Errorf("%s: the page holds no form %q", what, id)
			continue
		}
		if path == "" {
			path = form.action
		}
		form.fields.Set("paths", ref)
		maps.Copy(form.fields, act.form)
		if !act.get {
			lands(what, c.PostTo(t, path, form.fields))
			continue
		}
		// The destination page posts to itself with what it was opened with.
		opened := path + "?" + form.fields.Encode()
		dest, ok := formWith(c.Follow(t, opened), func(f pageForm) bool { return f.fields.Has("paths") })
		if !ok {
			t.Fatalf("%s: the destination page holds no form", what)
		}
		if dest.action == "" {
			dest.action = opened
		}
		dest.fields.Set("to", User+"|"+k.other)
		lands(what, c.PostTo(t, dest.action, dest.fields))
	}

	// A row's own forms: its star, and a task's box.
	for _, class := range []string{"row-star", "task-checkbox"} {
		row, ok := formWith(page, func(f pageForm) bool { return f.class == class && f.fields.Has("next") })
		if !ok {
			continue
		}
		row.fields.Set("color", "red")
		lands("the row's "+class, c.PostTo(t, row.action, row.fields))
	}

	// An object opened from the list goes back to it when an action settles it.
	object := pathOf(t, a.Path(k.object, acted))
	var opened string
	for _, m := range hrefAttr.FindAllStringSubmatch(page, -1) {
		if href := html.UnescapeString(m[1]); pathOf(t, href) == object {
			opened = href
			break
		}
	}
	if opened == "" {
		t.Fatalf("no row links to %s", object)
	}
	for _, route := range k.settles {
		form, ok := formWith(c.Follow(t, opened), func(f pageForm) bool { return f.action != "" && pathOf(t, f.action) == pathOf(t, a.Path(route, acted)) })
		if !ok {
			t.Errorf("the object's page has no form for %s", route)
			continue
		}
		lands("the object's "+route, c.PostTo(t, form.action, form.fields))
	}

	if !k.search {
		return
	}
	// A search keeps everything else the list was narrowed by, and starts at the first page.
	search := regexp.MustCompile(`(?s)<form method="get" class="actions-search[^"]*"([^>]*)>(.*?)</form>`).FindStringSubmatch(page)
	if search == nil {
		t.Fatalf("the list has no search")
	}
	fields := url.Values{}
	for _, f := range fieldTag.FindAllStringSubmatch(search[2], -1) {
		fields.Add(html.UnescapeString(f[1]), html.UnescapeString(f[2]))
	}
	fields.Set("query", "Kept")
	at := pathOf(t, address)
	if m := actAttr.FindStringSubmatch(search[1]); m != nil {
		at = html.UnescapeString(m[1])
	}
	want, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	q := want.Query()
	q.Del("page")
	want.RawQuery = q.Encode()
	if got := at + "?" + fields.Encode(); !sameAddress(t, got, want.String()) {
		t.Errorf("the search goes to %q, not to the list it searches, %q", got, want)
	}
}
