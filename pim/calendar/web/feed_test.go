package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// feed publishes one event today under the summary it holds, with an ETag naming it; down makes it fail.
type feed struct {
	summary           atomic.Value
	down              atomic.Bool
	hits, notModified atomic.Int32
	*httptest.Server
}

func startFeed(t *testing.T, summary string) *feed {
	t.Helper()
	f := &feed{}
	f.summary.Store(summary)
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.down.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s := f.summary.Load().(string)
		if r.Header.Get("If-None-Match") == `"`+s+`"` {
			f.notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"`+s+`"`)
		w.Write([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nX-WR-CALNAME:Hols\r\n" +
			"BEGIN:VEVENT\r\nUID:" + s + "\r\nDTSTART;VALUE=DATE:" + time.Now().Format("20060102") +
			"\r\nSUMMARY:" + s + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	}))
	t.Cleanup(f.Close)
	return f
}

// subscriber serves an alborz whose outgoing client trusts the feed's certificate.
func subscriber(t *testing.T, f *httptest.Server) (*App, *Client) {
	t.Helper()
	a := Alborz(t, Guarded(f.Client().Transport))
	return a, a.SignIn(t, User)
}

// subscribe follows the feed and returns the calendar the rail then lists, the one it did not list before.
func subscribe(t *testing.T, c *Client, address string) string {
	t.Helper()
	before := c.Listed(t)
	if resp := c.Post(t, "calendar.subscribe", url.Values{"address": {address}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("subscribing answered %s", resp.Status)
	}
	var added []string
	for p := range c.Listed(t) {
		if !before[p] {
			added = append(added, p)
		}
	}
	if len(added) != 1 {
		t.Fatalf("subscribing added %v to the rail, want one calendar", added)
	}
	return added[0]
}

// events are the paths of the events the agenda links to.
func events(t *testing.T, a *App, c *Client) []string {
	t.Helper()
	var out []string
	link := regexp.MustCompile(`href="(` + a.Pattern("calendar.event", `%2F[^"?/]+`) + `)`)
	for _, m := range link.FindAllStringSubmatch(c.Page(t, "calendar.month", url.Values{"view": {"list"}}), -1) {
		u, err := url.Parse(m[1])
		if err != nil {
			t.Fatal(err)
		}
		_, values, ok := a.Server.Match(u)
		if !ok {
			t.Fatalf("the agenda's link %s is no route", m[1])
		}
		out = append(out, values["path"])
	}
	return out
}

// A subscription is a calendar kept here, so a phone sees it as one and cannot write to it.
func TestASubscriptionIsAReadOnlyCalendar(t *testing.T) {
	f := startFeed(t, "Feast")
	a, c := subscriber(t, f.Server)
	path := subscribe(t, c, f.URL+"/f.ics")
	home := Here + CalendarsOf(User)
	if !strings.HasPrefix(path, home) {
		t.Fatalf("the subscription is %s, not a calendar in %s", path, home)
	}
	if !strings.Contains(c.Page(t, "calendar.month"), "Feast") {
		t.Fatal("the month does not show the feed's event")
	}
	if page := c.Page(t, "calendar.collection", path); !strings.Contains(page, f.URL+"/f.ics") {
		t.Error("the calendar's page does not name its feed")
	}
	linked := events(t, a, c)
	if len(linked) != 1 {
		t.Fatalf("the agenda links %d events, want the feed's one", len(linked))
	}
	if page := c.Page(t, "calendar.event", linked[0]); !strings.Contains(page, "Feast") || strings.Contains(page, a.Path("calendar.event.update", linked[0])) {
		t.Error("the event page does not show Feast read-only")
	}

	onDAV := strings.TrimPrefix(path, Here)
	resp := a.DAV(t, "PROPFIND", onDAV, map[string]string{"Depth": "0", "Content-Type": "application/xml"},
		`<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop>`+
			`<D:displayname/><D:current-user-privilege-set/><CS:source/></D:prop></D:propfind>`)
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(resp.Text, "Hols") || !strings.Contains(resp.Text, f.URL+"/f.ics") {
		t.Fatalf("PROPFIND answered %s without the name or the feed:\n%s", resp.Status, resp.Text)
	}
	if strings.Contains(resp.Text, "write") {
		t.Errorf("a DAV client is offered to write the subscription:\n%s", resp.Text)
	}
	if resp := a.DAV(t, http.MethodPut, onDAV+"mine.ics", map[string]string{"Content-Type": "text/calendar"},
		Event("mine", "Mine")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT into the subscription answered %s, want 403", resp.Status)
	}
}

// Refreshing replaces what the feed dropped, keeps the copy while it is down, and unsubscribing takes both
// the calendar and the account's entry, so the feed can be followed afresh.
func TestASubscriptionFollowsItsFeed(t *testing.T) {
	f := startFeed(t, "Feast")
	a, c := subscriber(t, f.Server)
	path := subscribe(t, c, f.URL+"/f.ics")

	c.Post(t, "calendar.refresh", url.Values{})
	if f.hits.Load() != 2 || f.notModified.Load() != 1 {
		t.Fatalf("after one refresh the feed saw %d requests, %d answered not modified; want 2 and 1",
			f.hits.Load(), f.notModified.Load())
	}
	f.summary.Store("Fair")
	c.Post(t, "calendar.refresh", url.Values{})
	if month := c.Page(t, "calendar.month"); !strings.Contains(month, "Fair") || strings.Contains(month, "Feast") {
		t.Fatal("the refresh did not replace Feast with Fair")
	}
	f.down.Store(true)
	c.Post(t, "calendar.refresh", url.Values{})
	if f.hits.Load() != 4 || !strings.Contains(c.Page(t, "calendar.month"), "Fair") {
		t.Fatalf("a failed refresh (request %d) dropped the copy kept", f.hits.Load())
	}

	if resp := c.Post(t, "calendar.collection.delete", url.Values{}, path); resp.StatusCode != http.StatusFound {
		t.Fatalf("unsubscribing answered %s", resp.Status)
	}
	if c.Listed(t)[path] {
		t.Fatal("the calendar is still listed after unsubscribing")
	}
	if resp := a.DAV(t, "PROPFIND", strings.TrimPrefix(path, Here), map[string]string{"Depth": "0"}, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the calendar still answers over DAV: %s", resp.Status)
	}
	f.down.Store(false)
	if again := subscribe(t, c, f.URL+"/f.ics"); again != path {
		t.Errorf("following the feed again made %s, want %s", again, path)
	}
}

// A phone deleting the calendar is the reader unsubscribing: the next page does not make it again.
func TestAPhoneDeletingASubscriptionUnsubscribes(t *testing.T) {
	f := startFeed(t, "Feast")
	a, c := subscriber(t, f.Server)
	path := subscribe(t, c, f.URL+"/f.ics")
	if resp := a.DAV(t, http.MethodDelete, strings.TrimPrefix(path, Here), nil, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the phone's DELETE answered %s", resp.Status)
	}
	if c.Listed(t)[path] {
		t.Fatal("the page made the deleted calendar again")
	}
	if again := subscribe(t, c, f.URL+"/f.ics"); again != path {
		t.Errorf("following the feed again made %s, want %s", again, path)
	}
}
