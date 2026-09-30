package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// walked is a kind of DAV object, by the routes of its pages.
type walked struct {
	name                   string
	list, object, edit     string
	create, bulkDelete     string
	importPage, refresh    string
	views                  [][]any
	sorts                  []string
	collField              string
	collection             string
	collName               string
	ext, mime              string
	body                   func(uid, title string) string
	titleField             string
	destField              string
	form                   url.Values
	bulkExport, listExport string
	exported               string
}

func card(uid, name string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\nN:" + name + ";;;;\r\nEND:VCARD\r\n"
}

func eventToday(uid, summary string) string {
	day := time.Now().UTC().Format("20060102")
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:" + day + "T100000Z\r\nDTEND:" + day + "T110000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func walkedKinds() []walked {
	today := time.Now().UTC().Format("2006-01-02")
	noon := today + "T12:00"
	return []walked{
		{name: "event", list: "calendar.month", object: "calendar.event", edit: "calendar.event.update",
			create: "calendar.event.create", bulkDelete: "calendar.events.delete",
			importPage: "calendar.import", refresh: "calendar.refresh",
			views: [][]any{
				{"calendar.month", url.Values{"view": {"list"}, "span": {"month"}}},
				{"calendar.month", url.Values{"view": {"list"}, "span": {"month"}, "group": {"day"}}},
				{"calendar.day", url.Values{"date": {today}}},
			},
			collField: "cal", collection: CalendarsOf(User) + "plans/", collName: "Plans", ext: ".ics", mime: "text/calendar", body: eventToday,
			titleField: "summary", destField: "calendar", form: url.Values{"start": {noon}, "end": {noon}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"}},
			listExport: "calendar.month.export", exported: "BEGIN:VEVENT"},
		{name: "task", list: "calendar.tasks", object: "calendar.task", edit: "calendar.task.edit",
			create: "calendar.task.create", bulkDelete: "calendar.tasks.delete",
			importPage: "calendar.tasks.import", refresh: "calendar.tasks.refresh",
			sorts:     []string{"summary", "status", "priority", "starred", "account", "calendar", "due", "added"},
			collField: "cal", collection: CalendarsOf(User) + "chores/", collName: "Chores", ext: ".ics", mime: "text/calendar", body: Todo,
			titleField: "summary", destField: "calendar", form: url.Values{},
			bulkExport: "calendar.tasks.export", exported: "BEGIN:VTODO"},
		{name: "contact", list: "contacts.list", object: "contacts.contact", edit: "contacts.contact.edit",
			create: "contacts.contact.create", bulkDelete: "contacts.delete",
			importPage: "contacts.import", refresh: "contacts.refresh",
			sorts:     []string{"name", "starred", "email", "phone", "account", "book", "changed"},
			collField: "book", collection: BooksOf(User) + "people/", collName: "People", ext: ".vcf", mime: "text/vcard", body: card,
			titleField: "fn", destField: "addressbook", form: url.Values{},
			bulkExport: "contacts.export", exported: "BEGIN:VCARD"},
	}
}

// postPiece posts form as a script does, asking for a piece of the page.
func postPiece(t *testing.T, a *App, c *Client, form url.Values, route string, args ...any) *Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path(route, args...), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	return c.Do(t, req)
}

// A template reads fields only as it renders, so a status alone proves nothing; a marker is looked for.
func TestEveryDAVObjectRouteAnswers(t *testing.T) {
	for _, scope := range []url.Values{nil, {"account": {User}}} {
		t.Run(fmt.Sprintf("scope=%v", scope), func(t *testing.T) {
			a := Alborz(t)
			c := a.SignIn(t, User)
			c.SignIn(t, User2)
			for _, made := range []struct {
				route string
				form  url.Values
			}{
				{"calendar.calendars.create", url.Values{"name": {"Plans"}, "color": {"#22aa55"}}},
				{"calendar.calendars.create", url.Values{"name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"calendar.calendars.create", url.Values{"name": {"Errands"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"contacts.books.create", url.Values{"name": {"People"}}},
			} {
				if !strings.Contains(c.Page(t, made.route, scope), `name="place"`) {
					t.Errorf("%s asks for no place", made.route)
				}
				made.form.Set("account", User)
				made.form.Set("place", "here")
				if resp := c.Post(t, made.route, made.form); resp.StatusCode != http.StatusFound {
					t.Fatalf("creating %s answered %s", made.form.Get("name"), resp.Status)
				}
			}
			for _, k := range walkedKinds() {
				t.Run(k.name, func(t *testing.T) { walk(t, a, c, k, scope) })
			}
		})
	}
}

func walk(t *testing.T, a *App, c *Client, k walked, scope url.Values) {
	acct := url.Values{"account": {User}}
	next := a.Path(k.list, scope)
	found := func(what, page, marker string) {
		t.Helper()
		if !strings.Contains(page, marker) {
			t.Errorf("%s: no %q on the page", what, marker)
		}
	}
	moved := func(what string, resp *Response) {
		t.Helper()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s answered %s", what, resp.Status)
		}
	}
	// held is the collection as pages hold it; Put and DAV reach it as any client does.
	held := Here + k.collection
	object := func(uid string) string { return held + uid + k.ext }
	ref := func(uid string) string { return User + "|" + held + uid + k.ext }
	uids := []string{"first", "second", "third"}
	for _, uid := range uids {
		a.Put(t, k.collection+uid+k.ext, k.mime, k.body(uid, "Walked "+uid))
	}
	page := c.Page(t, k.list, scope)
	for _, uid := range uids {
		found("the list", page, "Walked "+uid)
	}
	for i, key := range k.sorts {
		sorted := c.Page(t, k.list, scope, url.Values{"sort": {key}, "dir": {"desc"}})
		found("the list by "+key, sorted, "Walked first")
		if first, third := strings.Index(sorted, "Walked first"), strings.Index(sorted, "Walked third"); third > first {
			t.Errorf("sorted by %s, descending, the list still reads first to third", key)
		}
		if i == 0 && !strings.Contains(sorted, `aria-sort="descending"`) {
			t.Errorf("sorted by %s, no column header says so", key)
		}
	}
	if len(k.sorts) > 0 {
		if resp := c.Get(t, k.list, scope, url.Values{"sort": {"nothing"}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("an order the list does not have answered %s", resp.Status)
		}
	}
	for _, view := range k.views {
		found(fmt.Sprint(view), c.Page(t, view[0].(string), slices.Concat(view[1:], []any{scope})...), "Walked first")
	}

	found("the create form", c.Page(t, k.create, scope), `name="`+k.titleField+`"`)
	form := url.Values{k.titleField: {"Walked made"}}
	for field, values := range k.form {
		form[field] = values
	}
	form.Set(k.destField, User+"|"+Here+CalendarsOf("nowhere"))
	if resp := c.Post(t, k.create, form, scope); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a create into a collection the account has not answered %s", resp.Status)
	}
	form.Set(k.destField, User+"|"+held)
	moved("the create form", c.Post(t, k.create, form, scope))
	found("the list after a create", c.Page(t, k.list, scope), "Walked made")

	found("the object's page", c.Page(t, k.object, object("first"), acct), "Walked first")
	found("the edit form", c.Page(t, k.edit, object("first"), acct), "Walked first")
	form.Set(k.titleField, "Walked again")
	moved("the edit form", c.Post(t, k.edit, form, object("first"), acct))
	found("the object's page after an edit", c.Page(t, k.object, object("first"), acct), "Walked again")
	found("the raw object", c.Page(t, k.object+".raw", object("first"), acct), "Walked again")
	saved := c.Get(t, k.object+".raw", object("first"), acct, url.Values{"save": {"1"}})
	if got := saved.Header.Get("Content-Disposition"); got != "attachment; filename=first"+k.ext || !strings.HasPrefix(saved.Header.Get("Content-Type"), k.mime) {
		t.Errorf("the saved object came as %q, %q", got, saved.Header.Get("Content-Type"))
	}

	moved("the star", c.Post(t, k.object+".color", url.Values{"color": {"red"}, "next": {next}}, object("first"), acct))
	found("the starred object's page", c.Page(t, k.object, object("first"), acct), `data-current="red"`)
	starred := postPiece(t, a, c, url.Values{"color": {"blue"}, "next": {next}}, k.object+".color", object("first"), acct)
	if starred.StatusCode != http.StatusOK {
		t.Fatalf("the star, asked for as a piece, answered %s", starred.Status)
	}
	found("the star's piece", starred.Text, `data-current="blue"`)

	moved("the note", c.Post(t, k.object+".note", url.Values{"note": {"Walked note"}, "next": {next}}, object("first"), acct))
	found("the object's page after a note", c.Page(t, k.object, object("first"), acct), "Walked note")

	switch k.name {
	case "task":
		all := a.Path(k.list, url.Values{"view": {"all"}}, scope)
		marked := postPiece(t, a, c, url.Values{"next": {all}}, "calendar.task.complete", object("first"), acct)
		if marked.StatusCode != http.StatusOK {
			t.Fatalf("completing one, asked for as a piece, answered %s", marked.Status)
		}
		found("the completed task's row", marked.Text, "task-row list-row completed")
		moved("reopening one", c.Post(t, "calendar.task.complete", url.Values{"next": {all}, "reopen": {"1"}}, object("first"), acct))
		if page := c.Follow(t, all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after it was reopened")
		}
		moved("completing the checked", c.Post(t, "calendar.tasks.complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}}))
		if got := strings.Count(c.Follow(t, all), "task-row list-row completed"); got != 2 {
			t.Errorf("%d tasks are done after completing two", got)
		}
		moved("undoing the completion", c.Post(t, "calendar.tasks.complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}, "undo": {"1"}}))
		if page := c.Follow(t, all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after the undo")
		}
		where := c.Page(t, "calendar.tasks.move", url.Values{"paths": {ref("first")}, "next": {next}})
		found("the move page", where, `name="to"`)
		found("the move page", where, "Chores")
		moved("moving to the list it is in", c.Post(t, "calendar.tasks.move", url.Values{"paths": {ref("first")}, "to": {User + "|" + held}, "next": {next}}))
		errands := strings.Replace(held, "/chores/", "/errands/", 1)
		a.Put(t, k.collection+"carried"+k.ext, k.mime, k.body("carried", "Walked carried"))
		moved("moving the checked", c.Post(t, "calendar.tasks.move", url.Values{"paths": {ref("carried")}, "to": {User + "|" + errands}, "next": {next}}))
		found("the moved task", c.Page(t, k.object+".raw", errands+"carried"+k.ext, acct), "Walked carried")
		if resp := a.DAV(t, http.MethodGet, k.collection+"carried"+k.ext, nil, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("the moved task is still in the list it left: %s", resp.Status)
		}
	case "contact":
		moved("starring the checked", c.Post(t, "contacts.color", url.Values{"paths": {ref("first"), ref("second")}, "color": {"green"}, "next": {next}}))
		found("a contact starred from the list", c.Page(t, k.object, object("second"), acct), `data-current="green"`)
		moved("making a group", c.Post(t, "contacts.contact.group", url.Values{"newgroup": {"Walked group"}}, object("first"), acct))
		found("the grouped contact's page", c.Page(t, k.object, object("first"), acct), "Walked group")
		moved("removing the photo", c.Post(t, "contacts.contact.photo.delete", url.Values{"next": {next}}, object("first"), acct))
	}

	if k.bulkExport != "" {
		file := c.Post(t, k.bulkExport, url.Values{"paths": {ref("first"), ref("second")}, "next": {next}})
		if file.StatusCode != http.StatusOK || strings.Count(file.Text, k.exported) != 2 {
			t.Errorf("exporting two answered %s with %d of them", file.Status, strings.Count(file.Text, k.exported))
		}
	}
	if k.listExport != "" {
		found("the list's export", c.Page(t, k.listExport, scope), k.exported)
	}
	found("the import page's picker", c.Page(t, k.importPage, scope), k.collName+"</option>")

	chosen := c.Post(t, k.list, url.Values{k.collField: {held}, "next": {next}}, scope)
	moved("choosing the collections", chosen)
	if got := chosen.Header.Get("Location"); got != next {
		t.Errorf("choosing the collections lands on %q, not %q", got, next)
	}
	found("the list after choosing", c.Page(t, k.list, scope), "Walked second")
	refreshed := c.Post(t, k.refresh, nil, scope)
	moved("the refresh", refreshed)
	if got := refreshed.Header.Get("Location"); got != next {
		t.Errorf("the refresh lands on %q, not %q", got, next)
	}

	single := c.Post(t, k.object+".delete", url.Values{"next": {next}}, object("second"), acct)
	moved("deleting one", single)
	if got := single.Header.Get("Location"); got != next {
		t.Errorf("deleting one lands on %q, not %q", got, next)
	}
	moved("deleting the checked", c.Post(t, k.bulkDelete, url.Values{"paths": {ref("third")}, "next": {next}}))
	page = c.Page(t, k.list, scope)
	found("the list after the deletes", page, "Walked again")
	for _, uid := range []string{"second", "third"} {
		if strings.Contains(page, "Walked "+uid) {
			t.Errorf("%s is still listed after its delete", uid)
		}
	}
	outside := c.Post(t, k.object+".delete", url.Values{"next": {"//elsewhere.example/"}}, object("first"), acct)
	moved("deleting the last", outside)
	if got := outside.Header.Get("Location"); strings.Contains(got, "elsewhere") {
		t.Errorf("a delete followed a next that leaves the site: %q", got)
	}
}
