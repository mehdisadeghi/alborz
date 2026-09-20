package collections

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const event = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\n" +
	"DTSTAMP:20260101T100000Z\r\nDTSTART:20260102T100000Z\r\nSUMMARY:Lunch\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// open is a store in a data file of its own.
func open(t *testing.T) *Store {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "alborz.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// serve makes one request as an account and returns the status.
func serve(t *testing.T, h http.Handler, account, method, path, body string) int {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	if method == http.MethodPut {
		r.Header.Set("Content-Type", "text/calendar")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(As(r.Context(), account)))
	return w.Code
}

func TestAShareGivesWhatItSaysAndNoMore(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner, guest = "owner@example.org", "guest@example.org"
	ref := Ref{Owner: owner, ID: "work"}
	own := HomePath(Calendar, owner) + "work/"
	shared := HomePath(Calendar, guest) + "work" + aliasSep + owner + "/"

	steps := []struct {
		name    string
		before  func()
		account string
		method  string
		path    string
		body    string
		want    int
	}{
		{"the owner makes a calendar", nil, owner, "MKCALENDAR", own, "", http.StatusCreated},
		{"and writes to it", nil, owner, http.MethodPut, own + "e1.ics", event, http.StatusCreated},
		{"nobody else reads it by the owner's path", nil, guest, http.MethodGet, own + "e1.ics", "", http.StatusNotFound},
		{"an invitation not accepted opens nothing", func() {
			store.PutShare(Share{Owner: owner, Collection: "work", To: guest})
		}, guest, http.MethodGet, shared + "e1.ics", "", http.StatusNotFound},
		{"accepted, it reads", func() {
			store.PutShare(Share{Owner: owner, Collection: "work", To: guest, Accepted: true})
		}, guest, http.MethodGet, shared + "e1.ics", "", http.StatusOK},
		{"and does not write", nil, guest, http.MethodPut, shared + "e2.ics", event, http.StatusForbidden},
		{"nor delete an object", nil, guest, http.MethodDelete, shared + "e1.ics", "", http.StatusForbidden},
		{"nor rename the calendar", nil, guest, "PROPPATCH", shared, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Mine</D:displayname></D:prop></D:set></D:propertyupdate>`, http.StatusForbidden},
		{"with write, it writes", func() {
			store.PutShare(Share{Owner: owner, Collection: "work", To: guest, Accepted: true, Write: true})
		}, guest, http.MethodPut, shared + "e2.ics", strings.ReplaceAll(event, "e1", "e2"), http.StatusCreated},
		{"past its last day it is gone", func() {
			store.PutShare(Share{Owner: owner, Collection: "work", To: guest, Accepted: true, Write: true, Expires: time.Now().Add(-time.Minute)})
		}, guest, http.MethodGet, shared + "e1.ics", "", http.StatusNotFound},
		{"leaving deletes nothing of the owner's", func() {
			store.PutShare(Share{Owner: owner, Collection: "work", To: guest, Accepted: true, Write: true})
			if got := serve(t, h, guest, http.MethodDelete, shared, ""); got != http.StatusNoContent {
				t.Fatalf("leaving: got %d", got)
			}
		}, owner, http.MethodGet, own + "e1.ics", "", http.StatusOK},
		{"and the one who left is out", nil, guest, http.MethodGet, shared + "e1.ics", "", http.StatusNotFound},
	}
	for _, step := range steps {
		if step.before != nil {
			step.before()
		}
		if got := serve(t, h, step.account, step.method, step.path, step.body); got != step.want {
			t.Fatalf("%s: %s %s as %s: got %d, want %d", step.name, step.method, step.path, step.account, got, step.want)
		}
	}
	// The store asks again inside its own transaction, which is what
	// holds when a share ends between the handler's look and the write.
	if _, err := store.PutObject(ref, "e3.ics", "e3", []byte(event), nil, guest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a write by one who left: got %v, want %v", err, ErrNotFound)
	}
	if objects, _ := store.Objects(ref); len(objects) != 2 {
		t.Fatalf("the calendar holds %d objects, want the 2 written", len(objects))
	}
}

func TestACalendarTakesOnlyTheComponentsItNames(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	home := HomePath(Calendar, owner)
	mkcalendar := func(comp string) string {
		return `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop>` +
			`<C:supported-calendar-component-set><C:comp name="` + comp + `"/></C:supported-calendar-component-set>` +
			`</D:prop></D:set></C:mkcalendar>`
	}
	todo := strings.NewReplacer("VEVENT", "VTODO", "DTSTART", "DUE").Replace(event)
	for _, step := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"a component iCalendar does not have", "MKCALENDAR", home + "bad/", mkcalendar(`VEVENT&quot;/&gt;&lt;/broken&gt;`), http.StatusForbidden},
		{"events only", "MKCALENDAR", home + "events/", mkcalendar("VEVENT"), http.StatusCreated},
		{"an object named as a step up", http.MethodPut, home + "events/..", event, http.StatusBadRequest},
		{"a calendar named as a step up", "MKCALENDAR", home + "../", "", http.StatusBadRequest},
		{"an event into it", http.MethodPut, home + "events/e1.ics", event, http.StatusCreated},
		{"a task into it", http.MethodPut, home + "events/t1.ics", todo, http.StatusConflict},
	} {
		if got := serve(t, h, owner, step.method, step.path, step.body); got != step.want {
			t.Fatalf("%s: got %d, want %d", step.name, got, step.want)
		}
	}
}

func TestAPropertyAClientSetsIsHandedBack(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	book := HomePath(AddressBook, owner) + "people/"
	const mkcol = `<D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav"><D:set><D:prop>` +
		`<D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype></D:prop></D:set></D:mkcol>`
	const patch = `<D:propertyupdate xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/" xmlns:A="http://apple.com/ns/ical/">` +
		`<D:set><D:prop><I:addressbook-color>#336699</I:addressbook-color><A:calendar-order>7</A:calendar-order></D:prop></D:set></D:propertyupdate>`
	if got := serve(t, h, owner, "MKCOL", book, mkcol); got != http.StatusCreated {
		t.Fatalf("making the book: got %d", got)
	}
	if got := serve(t, h, owner, "PROPPATCH", book, patch); got != http.StatusMultiStatus {
		t.Fatalf("setting the properties: got %d", got)
	}
	r := httptest.NewRequest("PROPFIND", book, strings.NewReader(`<D:propfind xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/" `+
		`xmlns:A="http://apple.com/ns/ical/"><D:prop><I:addressbook-color/><A:calendar-order/></D:prop></D:propfind>`))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(As(r.Context(), owner)))
	for _, kept := range []string{"#336699", ">7<"} {
		if !strings.Contains(w.Body.String(), kept) {
			t.Fatalf("the book lost %q: %s", kept, w.Body.String())
		}
	}
}

func TestAWriteHoldsToItsConditions(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	object := HomePath(Calendar, owner) + "work/e1.ics"
	if got := serve(t, h, owner, "MKCALENDAR", HomePath(Calendar, owner)+"work/", ""); got != http.StatusCreated {
		t.Fatalf("making the calendar: got %d", got)
	}
	held := func() string {
		o, err := store.Object(Ref{Owner: owner, ID: "work"}, "e1.ics")
		if err != nil {
			t.Fatal(err)
		}
		return `"` + o.ETag + `"`
	}
	for _, step := range []struct {
		name, method, header string
		value                func() string
		want                 int
	}{
		{"an edit of an object deleted elsewhere", http.MethodPut, "If-Match", func() string { return "*" }, http.StatusPreconditionFailed},
		{"a first write", http.MethodPut, "If-None-Match", func() string { return "*" }, http.StatusCreated},
		{"a second first write", http.MethodPut, "If-None-Match", func() string { return "*" }, http.StatusPreconditionFailed},
		{"an edit of a version long gone", http.MethodPut, "If-Match", func() string { return `"gone"` }, http.StatusPreconditionFailed},
		{"an edit naming the version held among others", http.MethodPut, "If-Match", func() string { return `"gone", ` + held() }, http.StatusCreated},
		{"a write unless it is the version held", http.MethodPut, "If-None-Match", held, http.StatusPreconditionFailed},
		{"or one a weak tag was made from", http.MethodPut, "If-None-Match", func() string { return "W/" + held() }, http.StatusPreconditionFailed},
		{"an edit of a version named weakly", http.MethodPut, "If-Match", func() string { return "W/" + held() }, http.StatusPreconditionFailed},
		{"a stale delete", http.MethodDelete, "If-Match", func() string { return `"gone"` }, http.StatusPreconditionFailed},
		{"a delete of the version held", http.MethodDelete, "If-Match", held, http.StatusNoContent},
	} {
		r := httptest.NewRequest(step.method, object, strings.NewReader(event))
		r.Header.Set("Content-Type", "text/calendar")
		r.Header.Set(step.header, step.value())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(As(r.Context(), owner)))
		if w.Code != step.want {
			t.Fatalf("%s: got %d, want %d", step.name, w.Code, step.want)
		}
	}
}

func TestAPutKeepsTheOctetsItWasSentAndNamesThem(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	if got := serve(t, h, owner, "MKCALENDAR", HomePath(Calendar, owner)+"work/", ""); got != http.StatusCreated {
		t.Fatalf("making the calendar: got %d", got)
	}
	r := httptest.NewRequest(http.MethodPut, HomePath(Calendar, owner)+"work/e1.ics", strings.NewReader(event))
	r.Header.Set("Content-Type", "text/calendar")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(As(r.Context(), owner)))
	o, err := store.Object(Ref{Owner: owner, ID: "work"}, "e1.ics")
	if err != nil {
		t.Fatal(err)
	}
	if string(o.Data) != event {
		t.Fatalf("kept %q, sent %q", o.Data, event)
	}
	if tag, want := w.Header().Get("ETag"), `"`+ETagOf([]byte(event))+`"`; tag != want {
		t.Fatalf("the PUT answered ETag %q, want %q, the tag of what was sent", tag, want)
	}
	// The event names VERSION before PRODID, which an encoder would
	// turn round: what a read answers is what the tag names.
	read := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, HomePath(Calendar, owner)+"work/e1.ics", nil)
	h.ServeHTTP(read, get.WithContext(As(get.Context(), owner)))
	if read.Body.String() != event || read.Header().Get("ETag") != w.Header().Get("ETag") {
		t.Fatalf("a GET answered %q under ETag %q", read.Body.String(), read.Header().Get("ETag"))
	}
}

func TestListingACalendarParsesNothing(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	if got := serve(t, h, owner, "MKCALENDAR", HomePath(Calendar, owner)+"work/", ""); got != http.StatusCreated {
		t.Fatalf("making the calendar: got %d", got)
	}
	if _, err := store.PutObject(Ref{Owner: owner, ID: "work"}, "e1.ics", "e1", []byte("no parser reads this"), nil, owner); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PROPFIND", HomePath(Calendar, owner)+"work/", strings.NewReader(
		`<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(As(r.Context(), owner)))
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "e1.ics") {
		t.Fatalf("listing the tags: got %d %s", w.Code, w.Body.String())
	}
}

func TestAnObjectPastTheBoundIsNotKept(t *testing.T) {
	store := open(t)
	h := store.Handler()
	const owner = "owner@example.org"
	home := HomePath(Calendar, owner)
	if got := serve(t, h, owner, "MKCALENDAR", home+"work/", ""); got != http.StatusCreated {
		t.Fatalf("making the calendar: got %d", got)
	}
	large := strings.Replace(event, "Lunch", strings.Repeat("x", maxObject), 1)
	if got := serve(t, h, owner, http.MethodPut, home+"work/e1.ics", large); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("an object of %d bytes: got %d, want %d", len(large), got, http.StatusRequestEntityTooLarge)
	}
	if objects, _ := store.Objects(Ref{Owner: owner, ID: "work"}); len(objects) != 0 {
		t.Fatalf("the calendar holds %d objects, want none", len(objects))
	}
}

func TestAPublishedCalendarReadsWithoutAnAccountAndOnlyByItsSecret(t *testing.T) {
	store := open(t)
	c, err := store.Create(Collection{ID: "open", Owner: "owner@example.org", Kind: Calendar, Name: "Open", Public: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	guarded := "CLASS:PRIVATE\r\nSUMMARY:Surgery"
	invited := "ATTENDEE:mailto:guest@example.org\r\nORGANIZER:mailto:owner@example.org\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Soon\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nSUMMARY:Lunch"
	for name, data := range map[string]string{
		"e1.ics": strings.Replace(event, "SUMMARY:Lunch", invited, 1),
		"e2.ics": strings.NewReplacer("e1", "e2", "SUMMARY:Lunch", guarded).Replace(event),
	} {
		if _, err := store.PutObject(c.Ref(), name, strings.TrimSuffix(name, ".ics"), []byte(data), nil, c.Owner); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]int{
		PublicPath("s3cret"): http.StatusOK,
		PublicPath("guess"):  http.StatusNotFound,
		PublicPath(""):       http.StatusNotFound,
	} {
		w := httptest.NewRecorder()
		store.ServePublic(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d", path, w.Code, want)
		}
		if want == http.StatusOK && !strings.Contains(w.Body.String(), "SUMMARY:Lunch") {
			t.Fatalf("the feed lacks the event: %q", w.Body.String())
		}
		for _, kept := range []string{"Surgery", "guest@example.org", "owner@example.org", "VALARM"} {
			if strings.Contains(w.Body.String(), kept) {
				t.Fatalf("the feed tells anyone %q: %q", kept, w.Body.String())
			}
		}
	}
}
