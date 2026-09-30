package alborztest

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"git.mehdix.org/alborz/pim/store"
)

// CalendarsOf and BooksOf are user's homes on alborz, as a DAV client reaches them.
func CalendarsOf(user string) string { return store.HomePath(store.Calendar, user) }
func BooksOf(user string) string     { return store.HomePath(store.AddressBook, user) }

func Event(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260915T100000Z\r\nDTEND:20260915T110000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func Todo(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VTODO\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nSUMMARY:" + summary + "\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
}

// CreateCalendar makes User a calendar on alborz named name, holding what holds says (events
// when empty), and is its path as a DAV client reaches it.
func (c *Client) CreateCalendar(t *testing.T, name, holds string) string {
	t.Helper()
	form := url.Values{"account": {User}, "place": {"here"}, "name": {name}, "color": {"#22aa55"}}
	if holds != "" {
		form.Set("holds", holds)
	}
	if resp := c.Post(t, "calendar.calendars.create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the calendar %s answered %s", name, resp.Status)
	}
	return CalendarsOf(User) + strings.ToLower(name) + "/"
}

// CreateBook makes User an address book on alborz named name.
func (c *Client) CreateBook(t *testing.T, name string) string {
	t.Helper()
	form := url.Values{"account": {User}, "place": {"here"}, "name": {name}}
	if resp := c.Post(t, "contacts.books.create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the address book %s answered %s", name, resp.Status)
	}
	return BooksOf(User) + strings.ToLower(name) + "/"
}

// DAV sends a DAV request as User, as a phone would.
func (a *App) DAV(t *testing.T, method, path string, header map[string]string, body string) *Response {
	t.Helper()
	req, err := http.NewRequest(method, a.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(User, Password)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return a.Client().Do(t, req)
}

// Put writes an object over DAV, which must make it.
func (a *App) Put(t *testing.T, path, contentType, body string) {
	t.Helper()
	if resp := a.DAV(t, http.MethodPut, path, map[string]string{"Content-Type": contentType}, body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s answered %s", path, resp.Status)
	}
}

// Listed are the calendars the calendar page links to.
func (c *Client) Listed(t *testing.T) map[string]bool {
	t.Helper()
	link := regexp.MustCompile(`href="` + c.app.Pattern("calendar.collection", `(%2F[^"?/]+)`))
	out := map[string]bool{}
	for _, m := range link.FindAllStringSubmatch(c.Page(t, "calendar.month"), -1) {
		p, err := url.PathUnescape(m[1])
		if err != nil {
			t.Fatal(err)
		}
		out[p] = true
	}
	return out
}
