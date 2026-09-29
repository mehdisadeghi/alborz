package alborz_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/web"
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

// startSubscriber serves an alborz whose outgoing client trusts the feed's certificate.
func startSubscriber(t *testing.T, f *feed) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	newAlborz(t, ln, &alborz.Options{
		Upstreams: []string{"test.local=imap+insecure://" + startIMAP(t)},
		LoginKey:  smokeKey, DataDir: t.TempDir(), CacheDir: t.TempDir(), Cache: true,
	}, func(s *web.Server) { s.Outgoing.Guarded = f.Client().Transport })
	return "http://" + ln.Addr().String()
}

// subscribe follows the feed and returns the calendar the rail then lists, the one it did not list before.
func subscribe(t *testing.T, c *http.Client, base, address string) string {
	t.Helper()
	before := listed(t, c, base)
	if resp := postForm(t, c, base+"/calendars/subscribe", url.Values{"address": {address}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("subscribing answered %s", resp.Status)
	}
	var added []string
	for p := range listed(t, c, base) {
		if !before[p] {
			added = append(added, p)
		}
	}
	if len(added) != 1 {
		t.Fatalf("subscribing added %v to the rail, want one calendar", added)
	}
	return added[0]
}

// A subscription is a calendar kept here, so a phone sees it as one and cannot write to it.
func TestASubscriptionIsAReadOnlyCalendar(t *testing.T) {
	f := startFeed(t, "Feast")
	base := startSubscriber(t, f)
	c := login(t, base)
	path := subscribe(t, c, base, f.URL+"/f.ics")
	home := hereAt + "/alborz/dav/calendars/" + smokeUser + "/"
	if !strings.HasPrefix(path, home) {
		t.Fatalf("the subscription is %s, not a calendar in %s", path, home)
	}
	if !strings.Contains(get(t, c, base+"/calendar"), "Feast") {
		t.Fatal("the month does not show the feed's event")
	}
	if page := get(t, c, base+"/calendars/"+url.PathEscape(path)); !strings.Contains(page, f.URL+"/f.ics") {
		t.Error("the calendar's page does not name its feed")
	}
	events := regexp.MustCompile(`href="(/calendar/%2F[^"?]+)`).FindAllStringSubmatch(get(t, c, base+"/calendar?view=list"), -1)
	if len(events) != 1 {
		t.Fatalf("the agenda links %d events, want the feed's one", len(events))
	}
	if page := get(t, c, base+events[0][1]); !strings.Contains(page, "Feast") || strings.Contains(page, "/update") {
		t.Error("the event page does not show Feast read-only")
	}

	onDAV := strings.TrimPrefix(path, hereAt)
	resp, body := dav(t, "PROPFIND", base+onDAV, map[string]string{"Depth": "0", "Content-Type": "application/xml"},
		`<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop>`+
			`<D:displayname/><D:current-user-privilege-set/><CS:source/></D:prop></D:propfind>`)
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(body, "Hols") || !strings.Contains(body, f.URL+"/f.ics") {
		t.Fatalf("PROPFIND answered %s without the name or the feed:\n%s", resp.Status, body)
	}
	if strings.Contains(body, "write") {
		t.Errorf("a DAV client is offered to write the subscription:\n%s", body)
	}
	if resp, _ := dav(t, http.MethodPut, base+onDAV+"mine.ics", map[string]string{"Content-Type": "text/calendar"},
		event("mine", "Mine")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT into the subscription answered %s, want 403", resp.Status)
	}
}

// Refreshing replaces what the feed dropped, keeps the copy while it is down, and unsubscribing takes both
// the calendar and the account's entry, so the feed can be followed afresh.
func TestASubscriptionFollowsItsFeed(t *testing.T) {
	f := startFeed(t, "Feast")
	base := startSubscriber(t, f)
	c := login(t, base)
	path := subscribe(t, c, base, f.URL+"/f.ics")

	postForm(t, c, base+"/calendar/refresh", url.Values{})
	if f.hits.Load() != 2 || f.notModified.Load() != 1 {
		t.Fatalf("after one refresh the feed saw %d requests, %d answered not modified; want 2 and 1",
			f.hits.Load(), f.notModified.Load())
	}
	f.summary.Store("Fair")
	postForm(t, c, base+"/calendar/refresh", url.Values{})
	if month := get(t, c, base+"/calendar"); !strings.Contains(month, "Fair") || strings.Contains(month, "Feast") {
		t.Fatal("the refresh did not replace Feast with Fair")
	}
	f.down.Store(true)
	postForm(t, c, base+"/calendar/refresh", url.Values{})
	if f.hits.Load() != 4 || !strings.Contains(get(t, c, base+"/calendar"), "Fair") {
		t.Fatalf("a failed refresh (request %d) dropped the copy kept", f.hits.Load())
	}

	if resp := postForm(t, c, base+"/calendars/"+url.PathEscape(path)+"/delete", url.Values{}); resp.StatusCode != http.StatusFound {
		t.Fatalf("unsubscribing answered %s", resp.Status)
	}
	if listed(t, c, base)[path] {
		t.Fatal("the calendar is still listed after unsubscribing")
	}
	if resp, _ := dav(t, "PROPFIND", base+strings.TrimPrefix(path, hereAt), map[string]string{"Depth": "0"}, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the calendar still answers over DAV: %s", resp.Status)
	}
	f.down.Store(false)
	if again := subscribe(t, c, base, f.URL+"/f.ics"); again != path {
		t.Errorf("following the feed again made %s, want %s", again, path)
	}
}

// A phone deleting the calendar is the reader unsubscribing: the next page does not make it again.
func TestAPhoneDeletingASubscriptionUnsubscribes(t *testing.T) {
	f := startFeed(t, "Feast")
	base := startSubscriber(t, f)
	c := login(t, base)
	path := subscribe(t, c, base, f.URL+"/f.ics")
	if resp, _ := dav(t, http.MethodDelete, base+strings.TrimPrefix(path, hereAt), nil, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the phone's DELETE answered %s", resp.Status)
	}
	if listed(t, c, base)[path] {
		t.Fatal("the page made the deleted calendar again")
	}
	if again := subscribe(t, c, base, f.URL+"/f.ics"); again != path {
		t.Errorf("following the feed again made %s, want %s", again, path)
	}
}
