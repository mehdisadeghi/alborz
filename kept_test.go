package alborz_test

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	_ "git.mehdix.org/alborz/plugins/carddav"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

// startAlborzKept starts alborz with a data directory and no DAV server
// of the domain's: every calendar and book is one kept here.
func startAlborzKept(t *testing.T, imapAddr string) string {
	t.Helper()
	key := fernet.MustDecodeKeys("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")[0]
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	if _, err := alborz.New(e, &alborz.Options{
		Upstreams:  []string{"test.local=imap+insecure://" + imapAddr},
		Theme:      "alborz",
		ThemesPath: "./themes",
		LoginKey:   key,
		DataDir:    t.TempDir(),
	}); err != nil {
		t.Fatalf("start alborz: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go e.Server.Serve(ln)
	t.Cleanup(func() { e.Close() })
	return "http://" + ln.Addr().String()
}

// put writes one object through alborz's own DAV server, as a phone
// syncing the collection does.
func put(t *testing.T, base, path, contentType, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(smokeUser, smokePass)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s answered %s", path, resp.Status)
	}
}

// dav makes one request to alborz's own DAV server as the account, and
// returns what came back, body and all.
func dav(t *testing.T, method, url string, header map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(smokeUser, smokePass)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(answer)
}

// event is one calendar object, named by its UID.
func event(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260915T100000Z\r\nDTEND:20260915T110000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// keptCalendar makes a calendar of alborz's own and answers its path.
func keptCalendar(t *testing.T, base string, c *http.Client, name, id string) string {
	t.Helper()
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {name}, "color": {"#22aa55"}}
	if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the calendar answered %s", resp.Status)
	}
	return base + "/alborz/dav/calendars/" + smokeUser + "/" + id + "/"
}

// A DELETE carries If-Match when a client deletes what it believes it
// has (RFC 9110 13.1.1); go-webdav's server hands the backend the path
// alone, and the object went whatever tag was sent.
func TestADeleteWithAStaleTagKeepsTheObject(t *testing.T) {
	base := startAlborzKept(t, startIMAP(t))
	c := login(t, base)
	cal := keptCalendar(t, base, c, "Plans", "plans")
	put(t, base, strings.TrimPrefix(cal+"dentist.ics", base), "text/calendar", event("dentist", "Dentist"))

	if resp, _ := dav(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": `"stale"`}, ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("a delete with a stale tag answered %s", resp.Status)
	}
	resp, _ := dav(t, http.MethodGet, cal+"dentist.ics", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the object is gone after a refused delete: %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": resp.Header.Get("ETag")}, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a delete with the object's own tag answered %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodGet, cal+"dentist.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the deleted object is still there: %s", resp.Status)
	}
}

// One UID per calendar (RFC 4791 5.3.2.1): a second object holding it
// is refused, and a rewrite of the object that holds it is not.
func TestASecondObjectWithATakenUIDIsRefused(t *testing.T) {
	base := startAlborzKept(t, startIMAP(t))
	c := login(t, base)
	cal := keptCalendar(t, base, c, "Work", "work")
	put(t, base, strings.TrimPrefix(cal+"review.ics", base), "text/calendar", event("review", "Review"))

	resp, _ := dav(t, http.MethodPut, cal+"copy.ics", map[string]string{"Content-Type": "text/calendar"}, event("review", "Review again"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second object with a taken UID answered %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodGet, cal+"copy.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused object was written anyway: %s", resp.Status)
	}
	if resp, _ := dav(t, http.MethodPut, cal+"review.ics", map[string]string{"Content-Type": "text/calendar"}, event("review", "Review, moved")); resp.StatusCode/100 != 2 {
		t.Fatalf("rewriting the object that holds the UID answered %s", resp.Status)
	}
	if _, kept := dav(t, http.MethodGet, cal+"review.ics", nil, ""); !strings.Contains(kept, "Review, moved") {
		t.Errorf("the rewrite did not stick: %s", kept)
	}
}

// A task with no STATUS is open (RFC 5545 3.8.1.11), and phones write
// them so. go-webdav's client drops is-not-defined from a filter, which
// once turned "tasks without a STATUS" into "tasks with one", and such a
// task vanished from the list whenever the done ones were hidden.
func TestAnOpenTaskWithoutStatusIsListed(t *testing.T) {
	base := startAlborzKept(t, startIMAP(t))
	c := login(t, base)
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}
	if resp := postForm(t, c, base+"/calendars/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the task list answered %s", resp.Status)
	}
	put(t, base, "/alborz/dav/calendars/"+smokeUser+"/chores/water.ics", "text/calendar",
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\n"+
			"UID:water\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:Water the ferns\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
	if page := get(t, c, base+"/tasks"); !strings.Contains(page, "Water the ferns") {
		t.Errorf("the open task without a STATUS is not on the list")
	}
}

// An address book query with no filter asks for every card (RFC 6352
// 10.5). go-webdav's server matched none under its default anyof, which
// once left the list of a book kept here, and any client asking the
// same, empty.
func TestABookKeptHereListsItsCards(t *testing.T) {
	base := startAlborzKept(t, startIMAP(t))
	c := login(t, base)
	form := url.Values{"account": {smokeUser}, "place": {"here"}, "name": {"People"}}
	if resp := postForm(t, c, base+"/address-books/create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the address book answered %s", resp.Status)
	}
	put(t, base, "/alborz/dav/addressbooks/"+smokeUser+"/people/parvin.vcf", "text/vcard",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:parvin\r\nFN:Parvin Etesami\r\nN:Etesami;Parvin;;;\r\nEND:VCARD\r\n")
	if page := get(t, c, base+"/contacts"); !strings.Contains(page, "Parvin Etesami") {
		t.Errorf("the card in the book kept here is not on the list")
	}
}

// walked is one kind of DAV object as the route walk drives it: where
// its list and its objects answer, the collection its fixtures go into,
// and what its forms ask for.
type walked struct {
	name          string
	list, objects string
	// views are the other pages listing the same objects.
	views []string
	// sorts are the orders the list can be put in, the first by title.
	sorts      []string
	edit       string
	collField  string
	collection string
	collName   string
	ext, mime  string
	object     func(uid, title string) string
	titleField string
	destField  string
	form       url.Values
	bulkExport string
	listExport string
	exported   string
}

func todo(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:" + summary + "\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
}

func card(uid, name string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\nN:" + name + ";;;;\r\nEND:VCARD\r\n"
}

// eventToday is an event of this month, the one the calendar opens on.
func eventToday(uid, summary string) string {
	day := time.Now().UTC().Format("20060102")
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:" + day + "T100000Z\r\nDTEND:" + day + "T110000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func walkedKinds() []walked {
	noon := time.Now().UTC().Format("2006-01-02") + "T12:00"
	return []walked{
		{name: "event", list: "/calendar", objects: "/calendar/",
			views: []string{"/calendar?view=list&span=month", "/calendar?view=list&span=month&group=day", "/calendar/date?date=" + time.Now().UTC().Format("2006-01-02")}, edit: "/update", collField: "cal",
			collection: "/alborz/dav/calendars/" + smokeUser + "/plans/", collName: "Plans", ext: ".ics", mime: "text/calendar", object: eventToday,
			titleField: "summary", destField: "calendar", form: url.Values{"start": {noon}, "end": {noon}},
			listExport: "/calendar/export", exported: "BEGIN:VEVENT"},
		{name: "task", list: "/tasks", objects: "/tasks/", edit: "/edit", collField: "cal",
			sorts:      []string{"summary", "status", "priority", "starred", "account", "calendar", "due", "added"},
			collection: "/alborz/dav/calendars/" + smokeUser + "/chores/", collName: "Chores", ext: ".ics", mime: "text/calendar", object: todo,
			titleField: "summary", destField: "calendar", form: url.Values{},
			bulkExport: "/tasks/export", exported: "BEGIN:VTODO"},
		{name: "contact", list: "/contacts", objects: "/contacts/", edit: "/edit", collField: "book",
			sorts:      []string{"name", "starred", "email", "phone", "account", "book", "changed"},
			collection: "/alborz/dav/addressbooks/" + smokeUser + "/people/", collName: "People", ext: ".vcf", mime: "text/vcard", object: card,
			titleField: "fn", destField: "addressbook", form: url.Values{},
			bulkExport: "/contacts/export", exported: "BEGIN:VCARD"},
	}
}

// withQuery adds to an address that may carry a query already.
func withQuery(address, query string) string {
	if strings.Contains(address, "?") {
		return address + "&" + query
	}
	return address + "?" + query
}

// postBody submits a form and hands back the answer with its body, for
// the routes that answer with a page, a piece of one or a file.
func postBody(t *testing.T, c *http.Client, u string, form url.Values, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	stay := *c
	stay.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := stay.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// Every route an event, a task and a contact has, from a list scoped to
// one account and from the merged one. A template reads its fields only
// as it renders, so a page is asked for and a marker looked for in it:
// a status alone says nothing about a field that is no longer there.
func TestEveryDAVObjectRouteAnswers(t *testing.T) {
	for _, scope := range []string{"", "?account=" + smokeUser} {
		t.Run("scope="+scope, func(t *testing.T) {
			base := startAlborzKept(t, startIMAP(t))
			c := login(t, base)
			postForm(t, c, base+"/login", url.Values{"username": {smokeUser2}, "password": {smokePass}})
			for _, made := range []struct {
				route string
				form  url.Values
			}{
				{"/calendars/create", url.Values{"name": {"Plans"}, "color": {"#22aa55"}}},
				{"/calendars/create", url.Values{"name": {"Chores"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"/calendars/create", url.Values{"name": {"Errands"}, "color": {"#22aa55"}, "holds": {"tasks"}}},
				{"/address-books/create", url.Values{"name": {"People"}}},
			} {
				if !strings.Contains(get(t, c, base+made.route+scope), `name="place"`) {
					t.Errorf("%s asks for no place", made.route)
				}
				made.form.Set("account", smokeUser)
				made.form.Set("place", "here")
				if resp := postForm(t, c, base+made.route, made.form); resp.StatusCode != http.StatusFound {
					t.Fatalf("creating %s answered %s", made.form.Get("name"), resp.Status)
				}
			}
			for _, k := range walkedKinds() {
				t.Run(k.name, func(t *testing.T) { walk(t, base, c, k, scope) })
			}
		})
	}
}

func walk(t *testing.T, base string, c *http.Client, k walked, scope string) {
	acct := "?account=" + smokeUser
	list := base + k.list + scope
	next := k.list + scope
	found := func(what, page, marker string) {
		t.Helper()
		if !strings.Contains(page, marker) {
			t.Errorf("%s: no %q on the page", what, marker)
		}
	}
	moved := func(what string, resp *http.Response) {
		t.Helper()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s answered %s", what, resp.Status)
		}
	}
	object := func(uid string) string { return base + k.objects + url.PathEscape(k.collection+uid+k.ext) }
	ref := func(uid string) string { return smokeUser + "|" + k.collection + uid + k.ext }
	uids := []string{"first", "second", "third"}
	for _, uid := range uids {
		put(t, base, k.collection+uid+k.ext, k.mime, k.object(uid, "Walked "+uid))
	}
	page := get(t, c, list)
	for _, uid := range uids {
		found("the list", page, "Walked "+uid)
	}
	for i, key := range k.sorts {
		sorted := get(t, c, withQuery(list, "sort="+key+"&dir=desc"))
		found("the list by "+key, sorted, "Walked first")
		// Every row ties under a column the fixtures say nothing in, and
		// ties fall back to the title, in the direction asked for.
		if first, third := strings.Index(sorted, "Walked first"), strings.Index(sorted, "Walked third"); third > first {
			t.Errorf("sorted by %s, descending, the list still reads first to third", key)
		}
		if i == 0 && !strings.Contains(sorted, `aria-sort="descending"`) {
			t.Errorf("sorted by %s, no column header says so", key)
		}
	}
	if len(k.sorts) > 0 {
		resp, err := c.Get(withQuery(list, "sort=nothing"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("an order the list does not have answered %s", resp.Status)
		}
	}
	for _, view := range k.views {
		found(view, get(t, c, base+view+strings.Replace(scope, "?", "&", 1)), "Walked first")
	}

	found("the create form", get(t, c, base+k.objects+"create"+scope), `name="`+k.titleField+`"`)
	form := url.Values{k.titleField: {"Walked made"}, k.destField: {smokeUser + "|" + k.collection}}
	for field, values := range k.form {
		form[field] = values
	}
	form.Set(k.destField, smokeUser+"|/alborz/dav/nowhere/")
	if resp := postForm(t, c, base+k.objects+"create"+scope, form); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a create into a collection the account has not answered %s", resp.Status)
	}
	form.Set(k.destField, smokeUser+"|"+k.collection)
	moved("the create form", postForm(t, c, base+k.objects+"create"+scope, form))
	found("the list after a create", get(t, c, list), "Walked made")

	found("the object's page", get(t, c, object("first")+acct), "Walked first")
	found("the edit form", get(t, c, object("first")+k.edit+acct), "Walked first")
	form.Set(k.titleField, "Walked again")
	moved("the edit form", postForm(t, c, object("first")+k.edit+acct, form))
	found("the object's page after an edit", get(t, c, object("first")+acct), "Walked again")
	found("the raw object", get(t, c, object("first")+"/raw"+acct), "Walked again")
	saved, err := c.Get(object("first") + "/raw" + acct + "&save=1")
	if err != nil {
		t.Fatal(err)
	}
	saved.Body.Close()
	if got := saved.Header.Get("Content-Disposition"); got != "attachment; filename=first"+k.ext || !strings.HasPrefix(saved.Header.Get("Content-Type"), k.mime) {
		t.Errorf("the saved object came as %q, %q", got, saved.Header.Get("Content-Type"))
	}

	moved("the star", postForm(t, c, object("first")+"/color"+acct, url.Values{"color": {"red"}, "next": {next}}))
	found("the starred object's page", get(t, c, object("first")+acct), `data-current="red"`)
	starred, piece := postBody(t, c, object("first")+"/color"+acct, url.Values{"color": {"blue"}, "next": {next}}, map[string]string{"HX-Request": "true"})
	if starred.StatusCode != http.StatusOK {
		t.Fatalf("the star, asked for as a piece, answered %s", starred.Status)
	}
	found("the star's piece", piece, `data-current="blue"`)

	moved("the note", postForm(t, c, object("first")+"/note"+acct, url.Values{"note": {"Walked note"}, "next": {next}}))
	found("the object's page after a note", get(t, c, object("first")+acct), "Walked note")

	switch k.name {
	case "task":
		all := k.list + "?view=all" + strings.Replace(scope, "?", "&", 1)
		marked, row := postBody(t, c, object("first")+"/complete"+acct, url.Values{"next": {all}}, map[string]string{"HX-Request": "true"})
		if marked.StatusCode != http.StatusOK {
			t.Fatalf("completing one, asked for as a piece, answered %s", marked.Status)
		}
		found("the completed task's row", row, "task-row list-row completed")
		moved("reopening one", postForm(t, c, object("first")+"/complete"+acct, url.Values{"next": {all}, "reopen": {"1"}}))
		if page := get(t, c, base+all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after it was reopened")
		}
		moved("completing the checked", postForm(t, c, base+"/tasks/complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}}))
		page := get(t, c, base+all)
		if got := strings.Count(page, "task-row list-row completed"); got != 2 {
			t.Errorf("%d tasks are done after completing two", got)
		}
		moved("undoing the completion", postForm(t, c, base+"/tasks/complete", url.Values{"paths": {ref("first"), ref("second")}, "next": {all}, "undo": {"1"}}))
		if page := get(t, c, base+all); strings.Contains(page, "task-row list-row completed") {
			t.Errorf("a task is still done after the undo")
		}
		asked, where := postBody(t, c, base+"/tasks/move", url.Values{"paths": {ref("first")}, "next": {next}}, nil)
		if asked.StatusCode != http.StatusOK {
			t.Fatalf("asking where to move answered %s", asked.Status)
		}
		found("the move page", where, `name="to"`)
		found("the move page", where, "Chores")
		moved("moving to the list it is in", postForm(t, c, base+"/tasks/move", url.Values{"paths": {ref("first")}, "to": {smokeUser + "|" + k.collection}, "next": {next}}))
		errands := strings.Replace(k.collection, "/chores/", "/errands/", 1)
		put(t, base, k.collection+"carried"+k.ext, k.mime, k.object("carried", "Walked carried"))
		moved("moving the checked", postForm(t, c, base+"/tasks/move", url.Values{"paths": {ref("carried")}, "to": {smokeUser + "|" + errands}, "next": {next}}))
		found("the moved task", get(t, c, base+k.objects+url.PathEscape(errands+"carried"+k.ext)+"/raw"+acct), "Walked carried")
		if resp, _ := dav(t, http.MethodGet, base+k.collection+"carried"+k.ext, nil, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("the moved task is still in the list it left: %s", resp.Status)
		}
	case "contact":
		moved("starring the checked", postForm(t, c, base+"/contacts/color", url.Values{"paths": {ref("first"), ref("second")}, "color": {"green"}, "next": {next}}))
		found("a contact starred from the list", get(t, c, object("second")+acct), `data-current="green"`)
		moved("making a group", postForm(t, c, object("first")+"/group"+acct, url.Values{"newgroup": {"Walked group"}}))
		found("the grouped contact's page", get(t, c, object("first")+acct), "Walked group")
		moved("removing the photo", postForm(t, c, object("first")+"/photo/delete"+acct, url.Values{"next": {next}}))
	}

	if k.bulkExport != "" {
		file, body := postBody(t, c, base+k.bulkExport, url.Values{"paths": {ref("first"), ref("second")}, "next": {next}}, nil)
		if file.StatusCode != http.StatusOK || strings.Count(body, k.exported) != 2 {
			t.Errorf("exporting two answered %s with %d of them", file.Status, strings.Count(body, k.exported))
		}
	}
	if k.listExport != "" {
		found("the list's export", get(t, c, base+k.listExport+scope), k.exported)
	}
	found("the import page's picker", get(t, c, base+k.list+"/import"+scope), k.collName+"</option>")

	chosen := postForm(t, c, list, url.Values{k.collField: {k.collection}, "next": {next}})
	moved("choosing the collections", chosen)
	if got := chosen.Header.Get("Location"); got != next {
		t.Errorf("choosing the collections lands on %q, not %q", got, next)
	}
	found("the list after choosing", get(t, c, list), "Walked second")
	refreshed := postForm(t, c, base+k.list+"/refresh"+scope, nil)
	moved("the refresh", refreshed)
	if got := refreshed.Header.Get("Location"); got != next {
		t.Errorf("the refresh lands on %q, not %q", got, next)
	}

	single := postForm(t, c, object("second")+"/delete"+acct, url.Values{"next": {next}})
	moved("deleting one", single)
	if got := single.Header.Get("Location"); got != next {
		t.Errorf("deleting one lands on %q, not %q", got, next)
	}
	moved("deleting the checked", postForm(t, c, base+k.objects+"delete", url.Values{"paths": {ref("third")}, "next": {next}}))
	page = get(t, c, list)
	found("the list after the deletes", page, "Walked again")
	for _, uid := range []string{"second", "third"} {
		if strings.Contains(page, "Walked "+uid) {
			t.Errorf("%s is still listed after its delete", uid)
		}
	}
	outside := postForm(t, c, object("first")+"/delete"+acct, url.Values{"next": {"//elsewhere.example/"}})
	moved("deleting the last", outside)
	if got := outside.Header.Get("Location"); strings.Contains(got, "elsewhere") {
		t.Errorf("a delete followed a next that leaves the site: %q", got)
	}
}
