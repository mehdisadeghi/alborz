package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/pim/store"
)

const (
	davProps = `<D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:resourcetype/></D:prop></D:propfind>`
	davNamed = `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Renamed</D:displayname></D:prop></D:set></D:propertyupdate>`
	calQuery = `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`
	bookQuery = `<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop></C:addressbook-query>`
	// Reports the server does not give anyone (ADR 24), so not a scoped password either.
	syncReport      = `<D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	freeBusyReport  = `<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="20260101T000000Z" end="20270101T000000Z"/></C:free-busy-query>`
	principalSearch = `<D:principal-property-search xmlns:D="DAV:"><D:property-search><D:prop><D:displayname/></D:prop><D:match>a</D:match></D:property-search>` +
		`<D:prop><D:displayname/></D:prop></D:principal-property-search>`
)

func multiget(kind string, hrefs ...string) string {
	ns, data := "caldav", "calendar-data"
	if kind == "addressbook" {
		ns, data = "carddav", "address-data"
	}
	return `<C:` + kind + `-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:` + ns + `"><D:prop><D:getetag/><C:` + data + `/></D:prop>` +
		`<D:href>` + strings.Join(hrefs, `</D:href><D:href>`) + `</D:href></C:` + kind + `-multiget>`
}

// devicePassword makes a device password with the form and is the password the page shows once.
func devicePassword(t *testing.T, c *Client, form url.Values) string {
	t.Helper()
	resp := c.Post(t, "settings.devices.create", form)
	m := regexp.MustCompile(`<code dir="ltr">([A-Z2-7]+)</code>`).FindStringSubmatch(resp.Text)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("making the device password %s answered %s with no password shown", form.Get("name"), resp.Status)
	}
	return m[1]
}

// asDevice sends a DAV request as User with a password, the body's type following what it is.
func asDevice(t *testing.T, a *App, password, method, path string, header map[string]string, body string) *Response {
	t.Helper()
	req, err := http.NewRequest(method, a.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(User, password)
	switch {
	case strings.HasPrefix(body, "BEGIN:VCALENDAR"):
		req.Header.Set("Content-Type", "text/calendar")
	case strings.HasPrefix(body, "BEGIN:VCARD"):
		req.Header.Set("Content-Type", "text/vcard")
	case body != "":
		req.Header.Set("Content-Type", "application/xml")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return a.Client().Do(t, req)
}

// Every route and method the DAV server answers, asked with a device password for every collection, one
// limited to two calendars and two address books for reading and one limited to them for writing (ADR 109).
// {who} in a path or a body is the password asking, so each has an object or a name of its own to make or
// delete.
func TestAScopedDevicePasswordReachesItsCollectionsOnly(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	plans, spare := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Spare", "")
	private, old, people := c.CreateCalendar(t, "Private", ""), c.CreateCalendar(t, "Old", ""), c.CreateBook(t, "People")
	shelf, extra := c.CreateBook(t, "Shelf"), c.CreateBook(t, "Extra")
	root, home, books := store.Prefix+"/", CalendarsOf(User), BooksOf(User)
	principal := root + "principals/" + User + "/"
	a.Put(t, plans+"dentist.ics", "text/calendar", Event("dentist", "Dentist"))
	a.Put(t, private+"secret.ics", "text/calendar", Event("secret", "Secret"))
	a.Put(t, people+"card.vcf", "text/vcard", card("card", "Hidden Person"))
	a.Put(t, shelf+"kept.vcf", "text/vcard", card("kept", "Kept Person"))
	whos := []string{"read", "write", "whole"}
	for _, who := range whos {
		a.Put(t, plans+"gone-"+who+".ics", "text/calendar", Event("gone-"+who, "Gone"))
		a.Put(t, private+"gone-"+who+".ics", "text/calendar", Event("private-gone-"+who, "Gone"))
		a.Put(t, shelf+"gone-"+who+".vcf", "text/vcard", card("gone-"+who, "Gone"))
	}
	scope := url.Values{"scope": {"some"}, "collection": {plans, spare, shelf, extra}}
	passwords := map[string]string{
		"whole": devicePassword(t, c, url.Values{"name": {"whole"}, "access": {"write"}, "scope": {"all"}, "collection": {plans}}),
		"read":  devicePassword(t, c, url.Values{"name": {"read"}, "access": {"read"}, "scope": scope["scope"], "collection": scope["collection"]}),
		"write": devicePassword(t, c, url.Values{"name": {"write"}, "access": {"write"}, "scope": scope["scope"], "collection": scope["collection"]}),
	}

	const (
		ok, made, gone, multi           = http.StatusOK, http.StatusCreated, http.StatusNoContent, http.StatusMultiStatus
		bad, refused, missing, unserved = http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed
		exists, unbuilt                 = http.StatusMethodNotAllowed, http.StatusNotImplemented
	)
	depth0, depth1, deep := map[string]string{"Depth": "0"}, map[string]string{"Depth": "1"}, map[string]string{"Depth": "infinity"}
	to := func(path string) map[string]string { return map[string]string{"Destination": a.URL + path} }
	for _, row := range []struct {
		name, method, path string
		header             map[string]string
		body               string
		whole, read, write int
	}{
		// Discovery (RFC 6764) is every password's.
		{"well-known CalDAV", http.MethodGet, "/.well-known/caldav", nil, "", 308, 308, 308},
		{"well-known CardDAV", "PROPFIND", "/.well-known/carddav", depth0, davProps, 308, 308, 308},
		{"the root", "PROPFIND", root, depth0, davProps, multi, multi, multi},
		{"the root's methods", http.MethodOptions, root, nil, "", gone, gone, gone},
		{"the principal", "PROPFIND", principal, depth0, davProps, multi, multi, multi},
		{"another principal's address answers with the account's own", "PROPFIND", root + "principals/" + User2 + "/", depth0, davProps, multi, multi, multi},
		{"a principal search", "REPORT", principal, nil, principalSearch, unserved, unserved, unserved},
		{"a write to the root", http.MethodPut, root, nil, Event("x", "X"), unserved, refused, unserved},
		{"a collection at the root", "MKCOL", root, nil, "", unserved, refused, unserved},
		{"a delete of the root", http.MethodDelete, root, nil, "", unserved, refused, unserved},
		{"a post to the root", http.MethodPost, root, nil, "", unserved, refused, unserved},
		{"the calendar home", "PROPFIND", home, depth1, davProps, multi, multi, multi},
		{"the address book home", "PROPFIND", books, depth1, davProps, multi, multi, multi},
		{"another account's home lists nothing", "PROPFIND", CalendarsOf(User2), depth1, davProps, multi, multi, multi},
		{"a query of the home", "REPORT", home, depth1, calQuery, missing, missing, missing},
		{"a collection in place of the home", "MKCOL", home, nil, "", refused, refused, refused},
		{"a delete of the home", http.MethodDelete, home, nil, "", missing, refused, missing},
		{"a rename of the home", "PROPPATCH", home, nil, davNamed, unbuilt, refused, unbuilt},

		// A calendar in scope.
		{"its methods", http.MethodOptions, plans, nil, "", gone, gone, gone},
		{"its properties", "PROPFIND", plans, depth0, davProps, multi, multi, multi},
		{"its objects", "PROPFIND", plans, depth1, davProps, multi, multi, multi},
		{"a calendar-query", "REPORT", plans, depth1, calQuery, multi, multi, multi},
		{"a calendar-multiget", "REPORT", plans, nil, multiget("calendar", plans+"dentist.ics"), multi, multi, multi},
		{"a sync-collection", "REPORT", plans, nil, syncReport, bad, bad, bad},
		{"a free-busy-query", "REPORT", plans, nil, freeBusyReport, bad, bad, bad},
		{"a principal-property-search", "REPORT", plans, nil, principalSearch, bad, bad, bad},
		{"a read of the collection itself", http.MethodGet, plans, nil, "", missing, missing, missing},
		{"a rename", "PROPPATCH", plans, nil, davNamed, multi, refused, multi},
		{"a calendar made over it", "MKCALENDAR", plans, nil, "", exists, refused, refused},
		{"a collection made over it", "MKCOL", plans, nil, "", exists, refused, refused},
		{"a post to it", http.MethodPost, plans, nil, "", unserved, refused, unserved},
		{"an object written as the collection", http.MethodPut, plans, nil, Event("x", "X"), refused, refused, refused},
		{"its deletion", http.MethodDelete, spare, nil, "", gone, refused, refused},

		// An object in scope.
		{"a read", http.MethodGet, plans + "dentist.ics", nil, "", ok, ok, ok},
		{"a head", http.MethodHead, plans + "dentist.ics", nil, "", ok, ok, ok},
		{"its methods", http.MethodOptions, plans + "dentist.ics", nil, "", gone, gone, gone},
		{"its properties", "PROPFIND", plans + "dentist.ics", depth0, davProps, multi, multi, multi},
		{"a change", http.MethodPut, plans + "dentist.ics", nil, Event("dentist", "Dentist"), made, refused, made},
		{"a new object", http.MethodPut, plans + "new-{who}.ics", nil, Event("new-{who}", "New"), made, refused, made},
		{"a property set on it", "PROPPATCH", plans + "dentist.ics", nil, davNamed, unbuilt, refused, unbuilt},
		{"its deletion", http.MethodDelete, plans + "gone-{who}.ics", nil, "", gone, refused, gone},
		{"a copy within scope", "COPY", plans + "dentist.ics", to(plans + "copy-{who}.ics"), "", unbuilt, refused, unbuilt},
		{"a move within scope", "MOVE", plans + "dentist.ics", to(plans + "moved-{who}.ics"), "", unbuilt, refused, unbuilt},
		{"a collection in its place", "MKCOL", plans + "dentist.ics", nil, "", refused, refused, refused},
		{"a post to it", http.MethodPost, plans + "dentist.ics", nil, "", unserved, refused, unserved},

		// A calendar out of scope is not there.
		{"its methods, which no collection's path decides", http.MethodOptions, private, nil, "", gone, gone, gone},
		{"its properties", "PROPFIND", private, depth0, davProps, multi, missing, missing},
		{"its objects", "PROPFIND", private, depth1, davProps, multi, missing, missing},
		{"a calendar-query", "REPORT", private, depth1, calQuery, multi, missing, missing},
		{"a sync-collection", "REPORT", private, nil, syncReport, bad, bad, bad},
		{"a rename", "PROPPATCH", private, nil, davNamed, multi, refused, missing},
		{"a calendar made over it", "MKCALENDAR", private, nil, "", exists, refused, refused},
		{"its deletion", http.MethodDelete, old, nil, "", gone, refused, missing},
		{"a read of its object", http.MethodGet, private + "secret.ics", nil, "", ok, missing, missing},
		{"a head of its object", http.MethodHead, private + "secret.ics", nil, "", ok, missing, missing},
		{"its object's methods", http.MethodOptions, private + "secret.ics", nil, "", gone, missing, missing},
		{"its object's properties", "PROPFIND", private + "secret.ics", depth0, davProps, multi, missing, missing},
		{"a change to its object", http.MethodPut, private + "secret.ics", nil, Event("secret", "Secret"), made, refused, missing},
		{"a new object in it", http.MethodPut, private + "new-{who}.ics", nil, Event("private-new-{who}", "New"), made, refused, missing},
		{"its object's deletion", http.MethodDelete, private + "gone-{who}.ics", nil, "", gone, refused, missing},
		{"a copy into it", "COPY", plans + "dentist.ics", to(private + "copy-{who}.ics"), "", unbuilt, refused, unbuilt},
		{"a move into it", "MOVE", plans + "dentist.ics", to(private + "moved-{who}.ics"), "", unbuilt, refused, unbuilt},
		{"a copy out of it", "COPY", private + "secret.ics", to(plans + "copied-{who}.ics"), "", unbuilt, refused, unbuilt},
		{"a move out of it", "MOVE", private + "secret.ics", to(plans + "taken-{who}.ics"), "", unbuilt, refused, unbuilt},

		// No scoped password makes a collection.
		{"a new calendar", "MKCALENDAR", home + "new-{who}/", nil, "", made, refused, refused},
		{"a new collection", "MKCOL", home + "col-{who}/", nil, "", made, refused, refused},
		{"a new address book", "MKCOL", books + "book-{who}/", nil, "", made, refused, refused},

		// An address book in scope.
		{"its properties", "PROPFIND", shelf, depth1, davProps, multi, multi, multi},
		{"an addressbook-query", "REPORT", shelf, depth1, bookQuery, multi, multi, multi},
		{"an addressbook-multiget", "REPORT", shelf, nil, multiget("addressbook", shelf+"kept.vcf"), multi, multi, multi},
		{"a read of its card", http.MethodGet, shelf + "kept.vcf", nil, "", ok, ok, ok},
		{"a new card", http.MethodPut, shelf + "new-{who}.vcf", nil, card("new-{who}", "New"), made, refused, made},
		{"a card's deletion", http.MethodDelete, shelf + "gone-{who}.vcf", nil, "", gone, refused, gone},
		{"a card's deletion unless changed", http.MethodDelete, shelf + "kept.vcf", map[string]string{"If-Match": `"x"`}, "", http.StatusPreconditionFailed, refused, http.StatusPreconditionFailed},
		{"a rename", "PROPPATCH", shelf, nil, davNamed, multi, refused, multi},
		{"a collection made over it", "MKCOL", shelf, nil, "", exists, refused, refused},
		{"its deletion", http.MethodDelete, extra, nil, "", gone, refused, refused},

		// An address book out of scope.
		{"its properties", "PROPFIND", people, depth0, davProps, multi, missing, missing},
		{"an addressbook-query", "REPORT", people, depth1, bookQuery, multi, missing, missing},
		{"an addressbook-multiget", "REPORT", books, nil, multiget("addressbook", people+"card.vcf"), multi, multi, multi},
		{"a read of its card", http.MethodGet, people + "card.vcf", nil, "", ok, missing, missing},
		{"a new card in it", http.MethodPut, people + "new-{who}.vcf", nil, card("new-{who}", "New"), made, refused, missing},
		{"a rename", "PROPPATCH", people, nil, davNamed, multi, refused, missing},

		// What the server keeps for nobody: scheduling (RFC 6638) and another account's home.
		{"the scheduling inbox", "PROPFIND", home + "inbox/", depth0, davProps, missing, missing, missing},
		{"the scheduling outbox", http.MethodPost, home + "outbox/", nil, Event("x", "X"), unserved, refused, unserved},
		{"another account's calendar", http.MethodGet, CalendarsOf(User2) + "plans/dentist.ics", nil, "", missing, missing, missing},
		{"a name no collection has", http.MethodGet, home + "bad%21/x.ics", nil, "", bad, bad, bad},
	} {
		for i, want := range []int{row.read, row.write, row.whole} {
			who := whos[i]
			named := strings.NewReplacer("{who}", who)
			header := map[string]string{}
			for k, v := range row.header {
				header[k] = named.Replace(v)
			}
			if got := asDevice(t, a, passwords[who], row.method, named.Replace(row.path), header, named.Replace(row.body)); got.StatusCode != want {
				t.Errorf("%s: %s %s with the %s password answered %d, want %d", row.name, row.method, row.path, who, got.StatusCode, want)
			}
		}
	}

	// What an answer lists is the scope's, whatever the address asked.
	for _, listed := range []struct {
		name, method, path string
		header             map[string]string
		body               string
		in, out            []string
	}{
		{"the calendar home", "PROPFIND", home, depth1, davProps, []string{plans}, []string{private}},
		{"the address book home", "PROPFIND", books, depth1, davProps, []string{shelf}, []string{people}},
		{"the calendar home, all the way down", "PROPFIND", home, deep, davProps, []string{plans + "dentist.ics"}, []string{private, "secret.ics"}},
		{"the address book home, all the way down", "PROPFIND", books, deep, davProps, []string{shelf + "kept.vcf"}, []string{people, "card.vcf"}},
		{"a calendar-multiget", "REPORT", plans, nil, multiget("calendar", plans+"dentist.ics", private+"secret.ics", CalendarsOf(User2)+"plans/x.ics"),
			[]string{"SUMMARY:Dentist"}, []string{"SUMMARY:Secret"}},
		{"a calendar-multiget asked of the home", "REPORT", home, nil, multiget("calendar", private+"secret.ics"), nil, []string{"SUMMARY:Secret"}},
		{"an addressbook-multiget", "REPORT", books, nil, multiget("addressbook", people+"card.vcf"), nil, []string{"Hidden Person"}},
	} {
		whole := asDevice(t, a, passwords["whole"], listed.method, listed.path, listed.header, listed.body).Text
		for _, want := range append(listed.in, listed.out...) {
			if !strings.Contains(whole, want) {
				t.Fatalf("%s holds no %q for a password of every collection", listed.name, want)
			}
		}
		for _, who := range []string{"read", "write"} {
			got := asDevice(t, a, passwords[who], listed.method, listed.path, listed.header, listed.body).Text
			for _, want := range listed.in {
				if !strings.Contains(got, want) {
					t.Errorf("%s holds no %q for the %s password", listed.name, want, who)
				}
			}
			for _, hidden := range listed.out {
				if strings.Contains(got, hidden) {
					t.Errorf("%s tells the %s password of %q", listed.name, who, hidden)
				}
			}
		}
	}

	// A collection out of scope answers every method as a name nothing has does, so no answer tells it is there.
	absent := strings.NewReplacer("/private/", "/nothing/", "/people/", "/nothing/", "secret.ics", "nothing.ics", "card.vcf", "nothing.vcf")
	for _, who := range []string{"read", "write"} {
		for _, at := range []struct{ path, object string }{{private, Event("secret", "Secret")}, {private + "secret.ics", Event("secret", "Secret")},
			{people, card("card", "Hidden Person")}, {people + "card.vcf", card("card", "Hidden Person")}} {
			for _, ask := range []struct {
				method string
				header map[string]string
				body   string
			}{
				{http.MethodOptions, nil, ""}, {http.MethodGet, nil, ""}, {http.MethodHead, nil, ""}, {http.MethodPost, nil, ""},
				{"PROPFIND", depth0, davProps}, {"PROPFIND", depth1, davProps}, {"PROPFIND", deep, davProps}, {"PROPPATCH", nil, davNamed},
				{"REPORT", depth1, calQuery}, {"REPORT", depth1, bookQuery}, {"REPORT", nil, syncReport}, {"REPORT", nil, freeBusyReport},
				{"REPORT", nil, principalSearch}, {"REPORT", nil, multiget("calendar", at.path)}, {"REPORT", nil, multiget("addressbook", at.path)},
				{http.MethodPut, nil, at.object}, {http.MethodDelete, nil, ""}, {http.MethodDelete, map[string]string{"If-Match": `"x"`}, ""},
				{"MKCOL", nil, ""}, {"MKCALENDAR", nil, ""}, {"COPY", to(plans + "from.ics"), ""}, {"MOVE", to(plans + "from.ics"), ""},
				{"LOCK", nil, ""}, {http.MethodPatch, nil, ""},
			} {
				there := asDevice(t, a, passwords[who], ask.method, at.path, ask.header, ask.body)
				not := asDevice(t, a, passwords[who], ask.method, absent.Replace(at.path), ask.header, absent.Replace(ask.body))
				if there.StatusCode != not.StatusCode || absent.Replace(there.Text) != absent.Replace(not.Text) {
					t.Errorf("%s %s with the %s password answered %d %q, and %d %q where nothing is",
						ask.method, at.path, who, there.StatusCode, there.Text, not.StatusCode, not.Text)
				}
			}
		}
	}
	for path, want := range map[string]int{private + "secret.ics": multi, people + "card.vcf": multi, home + "nothing/": missing, books + "nothing/": missing} {
		if got := asDevice(t, a, passwords["whole"], "PROPFIND", path, depth0, davProps).StatusCode; got != want {
			t.Errorf("after the scoped passwords asked, %s answers %d, want %d", path, got, want)
		}
	}

	// A calendar deleted and made again under its name is another: the scope named the one that is gone.
	for _, step := range []struct {
		method, path, body string
		want               int
	}{{http.MethodDelete, plans, "", gone}, {"MKCALENDAR", plans, "", made}, {http.MethodPut, plans + "later.ics", Event("later", "Later"), made}} {
		if got := asDevice(t, a, passwords["whole"], step.method, step.path, nil, step.body).StatusCode; got != step.want {
			t.Fatalf("%s %s answered %d", step.method, step.path, got)
		}
	}
	for _, who := range []string{"read", "write"} {
		if got := asDevice(t, a, passwords[who], http.MethodGet, plans+"later.ics", nil, "").StatusCode; got != missing {
			t.Errorf("a calendar made again under a scoped name answers the %s password %d", who, got)
		}
		if got := asDevice(t, a, passwords[who], "PROPFIND", home, depth1, davProps).Text; strings.Contains(got, plans) {
			t.Errorf("a calendar made again under a scoped name is listed to the %s password", who)
		}
	}
}

// A collection shared with the account is in scope as the account sees it: at its address there, for as long
// as the share gives it, and not to be left by the device.
func TestAScopedDevicePasswordReachesASharedCollectionAsItsAccountDoes(t *testing.T) {
	a := Alborz(t)
	owner, c := a.SignIn(t, User2), a.SignIn(t, User)
	if resp := owner.Post(t, "calendar.calendars.create", url.Values{"place": {User2 + "|here"}, "name": {"Team"}, "color": {"#22aa55"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the calendar answered %s", resp.Status)
	}
	own, shared := CalendarsOf(User2)+"team/", CalendarsOf(User)+"team~"+User2+"/"
	share := func() {
		t.Helper()
		if resp := owner.Post(t, "calendar.collection.share", url.Values{"to": {User}, "access": {"write"}}, Here+own); resp.StatusCode != http.StatusFound {
			t.Fatalf("sharing answered %s", resp.Status)
		}
		if resp := c.Post(t, "calendar.collection.accept", url.Values{}, Here+shared); resp.StatusCode != http.StatusFound {
			t.Fatalf("accepting answered %s", resp.Status)
		}
	}
	share()
	if page := c.Page(t, "settings.devices.create"); !strings.Contains(page, `value="`+shared+`"`) || strings.Contains(page, `value="`+own+`"`) {
		t.Fatal("the form does not offer the shared calendar at the account's own address for it")
	}
	password := devicePassword(t, c, url.Values{"name": {"team"}, "access": {"write"}, "scope": {"some"}, "collection": {shared}})
	status := func(method, path, body string) int {
		t.Helper()
		return asDevice(t, a, password, method, path, nil, body).StatusCode
	}
	if got := status(http.MethodPut, shared+"standup.ics", Event("standup", "Standup")); got != http.StatusCreated {
		t.Fatalf("a write to the shared calendar answered %d", got)
	}
	if got := status(http.MethodGet, own+"standup.ics", ""); got != http.StatusNotFound {
		t.Errorf("the owner's address answered %d", got)
	}
	if got := status(http.MethodDelete, shared, ""); got != http.StatusForbidden {
		t.Errorf("leaving the share answered %d", got)
	}
	if got := status(http.MethodGet, shared+"standup.ics", ""); got != http.StatusOK {
		t.Fatalf("after the refused leaving, a read answered %d", got)
	}
	if resp := owner.Post(t, "calendar.collection.unshare", url.Values{"to": {User}}, Here+own); resp.StatusCode != http.StatusFound {
		t.Fatalf("unsharing answered %s", resp.Status)
	}
	if got := status(http.MethodGet, shared+"standup.ics", ""); got != http.StatusNotFound {
		t.Errorf("unshared, a read answered %d", got)
	}
	// Shared again it is the collection the scope names, and the owner's to give.
	share()
	if got := status(http.MethodGet, shared+"standup.ics", ""); got != http.StatusOK {
		t.Errorf("shared again, a read answered %d", got)
	}
}

// The form makes a password for every collection or for those chosen, never for none. The list and the
// password's page say what it reaches, and that a collection of it no longer exists.
func TestTheDeviceFormLimitsAPasswordToChosenCollections(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	plans, chores, people := c.CreateCalendar(t, "Plans", ""), c.CreateCalendar(t, "Chores", "tasks"), c.CreateBook(t, "People")
	// Renamed to a name the account has, as a DAV client can.
	twin := c.CreateCalendar(t, "Twin", "")
	if resp := c.Post(t, "calendar.collection", url.Values{"name": {"Plans"}, "color": {"#22aa55"}}, Here+twin); resp.StatusCode != http.StatusFound {
		t.Fatalf("renaming the calendar answered %s", resp.Status)
	}
	page := c.Page(t, "settings.devices.create")
	for _, path := range []string{plans, twin, chores, people} {
		if !strings.Contains(page, `name="collection" id="collection-`) || !strings.Contains(page, `value="`+path+`"`) {
			t.Fatalf("the form does not offer %s", path)
		}
	}
	for _, want := range []string{"Plans · \u2066plans\u2069", "Plans · \u2066twin\u2069"} {
		if !strings.Contains(page, want) {
			t.Errorf("the form does not tell two calendars of one name apart as %q", want)
		}
	}
	if strings.Contains(page, "\u2066chores\u2069") {
		t.Error("the form adds its ID to a task list no other reads like")
	}
	for name, form := range map[string]url.Values{
		"none chosen":           {"name": {"x"}, "access": {"read"}, "scope": {"some"}},
		"one that is not there": {"name": {"x"}, "access": {"read"}, "scope": {"some"}, "collection": {plans, CalendarsOf(User) + "nothing/"}},
		"another account's":     {"name": {"x"}, "access": {"read"}, "scope": {"some"}, "collection": {CalendarsOf(User2) + "plans/"}},
	} {
		resp := c.Post(t, "settings.devices.create", form)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(resp.Text, `value="some" checked`) {
			t.Errorf("%s: the form answered %s", name, resp.Status)
		}
	}
	if devices, _, err := a.Server.Devices.List(User); err != nil || len(devices) != 0 {
		t.Fatalf("the refused forms left %d device passwords: %v", len(devices), err)
	}
	devicePassword(t, c, url.Values{"name": {"all"}, "access": {"write"}})
	devicePassword(t, c, url.Values{"name": {"sync"}, "access": {"read"}, "scope": {"some"}, "collection": {plans, chores, people}})
	devices, _, err := a.Server.Devices.List(User)
	if err != nil || len(devices) != 2 || devices[0].Name != "sync" {
		t.Fatalf("the account's device passwords are %+v: %v", devices, err)
	}
	// What the pages say, their markup's spacing aside.
	says := func(want string, route string, args ...any) {
		t.Helper()
		if got := strings.Join(strings.Fields(c.Page(t, route, args...)), " "); !strings.Contains(got, want) {
			t.Errorf("%s does not say %q", route, want)
		}
	}
	says("Read and write · All collections · Created", "settings.devices")
	says("Read only · 1 calendar · 1 task list · 1 address book · Created", "settings.devices")
	says("<dt>Access</dt> <dd>Read only</dd>", "settings.device", devices[0].ID)
	says("<dt>Plans · \u2066plans\u2069</dt> <dd>Calendar</dd>", "settings.device", devices[0].ID)
	says("<dt>Chores</dt> <dd>Task list</dd>", "settings.device", devices[0].ID)
	says("<dd>All collections</dd>", "settings.device", devices[1].ID)

	if got := a.DAV(t, http.MethodDelete, chores, nil, "").StatusCode; got != http.StatusNoContent {
		t.Fatalf("deleting the task list answered %d", got)
	}
	says("Read only · 1 calendar · 1 address book · 1 collection no longer exists · Created", "settings.devices")
	says("<dt>No longer exists</dt> <dd>Task list</dd>", "settings.device", devices[0].ID)
}
