package web_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestMovingCalendarObjectsPreservesTheirData(t *testing.T) {
	for _, tc := range []struct{ kind, component, data string }{
		{"event", "", strings.ReplaceAll(clubNight(), "METHOD:PUBLISH\r\n", "")},
		{"task", "tasks", Todo("water", "Water the ferns")},
		{"entry", "journal", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VJOURNAL\r\nUID:notes\r\nDTSTAMP:20260901T080000Z\r\nDTSTART;VALUE=DATE:20260901\r\nSUMMARY:Notes\r\nDESCRIPTION:First\r\nDESCRIPTION:Second\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			a, c := SignedIn(t, User)
			src := c.CreateCalendar(t, "Source", tc.component)
			dst := c.CreateCalendar(t, "Destination", tc.component)
			a.Put(t, src+"object.ics", "text/calendar", tc.data)
			resp := c.Post(t, "calendar."+tc.kind+".move", url.Values{"to": {User + "|" + Here + dst}}, Here+src+"object.ics")
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("move answered %s: %s", resp.Status, regexp.MustCompile(`class="notice-text"[^>]*>([^<]*)`).FindAllStringSubmatch(resp.Text, -1))
			}
			got := icsLines(raw(t, a, c, resp.Header.Get("Location")))
			for _, line := range icsLines(tc.data) {
				if line != "" && !strings.Contains(strings.Join(got, "\r\n"), line) {
					t.Errorf("move lost %q", line)
				}
			}
			if source := a.DAV(t, http.MethodGet, src+"object.ics", nil, ""); source.StatusCode != http.StatusNotFound {
				t.Errorf("source remains: %s", source.Status)
			}
		})
	}
}

func TestMovingAnEventNeverOverwritesADestination(t *testing.T) {
	a, c := SignedIn(t, User)
	src := c.CreateCalendar(t, "Source", "")
	dst := c.CreateCalendar(t, "Destination", "")
	a.Put(t, src+"same.ics", "text/calendar", Event("source", "Source"))
	a.Put(t, dst+"same.ics", "text/calendar", Event("destination", "Destination"))
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + dst}}, Here+src+"same.ics")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("collision answered %s", resp.Status)
	}
	for _, obj := range []struct{ path, summary string }{{src + "same.ics", "SUMMARY:Source"}, {dst + "same.ics", "SUMMARY:Destination"}} {
		if got := a.DAV(t, http.MethodGet, obj.path, nil, ""); got.StatusCode != http.StatusOK || !strings.Contains(got.Text, obj.summary) {
			t.Errorf("collision changed %s: %s", obj.path, got.Text)
		}
	}
}

func TestMovingAReadOnlySubscriptionIsRefusedBeforeCreating(t *testing.T) {
	f := servedFeed(t, clubNight())
	a, c := subscriber(t, f)
	subscribe(t, c, f.URL+"/club.ics")
	dst := c.CreateCalendar(t, "Destination", "")
	src := events(t, a, c)[0]
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + dst}}, src)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only move answered %s", resp.Status)
	}
	if got := a.DAV(t, http.MethodGet, dst+"club-night.ics", nil, ""); got.StatusCode != http.StatusNotFound {
		t.Error("read-only move created a destination")
	}
}

func TestMovingReadOnlySharedCalendarObjectsIsRefused(t *testing.T) {
	for _, tc := range []struct{ kind, holds, data string }{
		{"event", "", Event("original", "Original")},
		{"task", "tasks", Todo("original", "Original")},
		{"entry", "journal", strings.ReplaceAll(Event("original", "Original"), "VEVENT", "VJOURNAL")},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			a, owner := SignedIn(t, User)
			src := owner.CreateCalendar(t, "Source", tc.holds)
			a.Put(t, src+"object.ics", "text/calendar", tc.data)
			if resp := owner.Post(t, "calendar.collection.share", url.Values{"to": {User2}, "access": {"read"}}, Here+src); resp.StatusCode != http.StatusFound {
				t.Fatalf("sharing answered %s", resp.Status)
			}
			guest := a.SignIn(t, User2)
			shared := Here + CalendarsOf(User2) + "source~" + User + "/"
			if resp := guest.Post(t, "calendar.collection.accept", url.Values{}, shared); resp.StatusCode != http.StatusFound {
				t.Fatalf("accepting answered %s", resp.Status)
			}
			if resp := guest.Post(t, "calendar.calendars.create", url.Values{"account": {User2}, "place": {"here"}, "name": {"Target"}, "holds": {tc.holds}}); resp.StatusCode != http.StatusFound {
				t.Fatalf("creating destination answered %s", resp.Status)
			}
			to := User2 + "|" + Here + CalendarsOf(User2) + "target/"
			if resp := guest.Post(t, "calendar."+tc.kind+".move", url.Values{"to": {to}}, shared+"object.ics"); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("read-only shared move answered %s", resp.Status)
			}
			if resp := guest.Post(t, "calendar."+tc.kind+".copy", url.Values{"to": {to}}, shared+"object.ics"); resp.StatusCode != http.StatusFound {
				t.Fatalf("copy from read-only shared object answered %s", resp.Status)
			}
		})
	}
}

func TestMovingAnEventRollsBackWhenTheSourceRefusesDeletion(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"), Refusing(personal+"object.ics"), Object(personal+"object.ics", Event("original", "Original")))
	a, c := SignedIn(t, User, WithDAV(d))
	dst := c.CreateCalendar(t, "Destination", "")
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + dst}}, d.At()+personal+"object.ics")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refused deletion answered %s", resp.Status)
	}
	if got := a.DAV(t, http.MethodGet, dst+"object.ics", nil, ""); got.StatusCode != http.StatusNotFound {
		t.Error("failed move left a destination copy")
	}
}

func TestMovingAnEventAcrossSignedInAccounts(t *testing.T) {
	a, c := SignedIn(t, User)
	src := c.CreateCalendar(t, "Source", "")
	body := strings.Replace(Event("original", "Original"), "END:VEVENT", "ORGANIZER:mailto:host@example.org\r\nATTENDEE:mailto:guest@example.org\r\nEND:VEVENT", 1)
	a.Put(t, src+"object.ics", "text/calendar", body)
	c.SignIn(t, User2)
	if resp := c.Post(t, "calendar.calendars.create", url.Values{"account": {User2}, "place": {"here"}, "name": {"Destination"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("create destination: %s", resp.Status)
	}
	dst := Here + CalendarsOf(User2) + "destination/"
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User2 + "|" + dst}}, Here+src+"object.ics", url.Values{"account": {User}})
	landed, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || landed.Query().Get("account") != User2 {
		t.Fatalf("move answered %s to %s", resp.Status, resp.Header.Get("Location"))
	}
	got, want := icsLines(raw(t, a, c, resp.Header.Get("Location"))), icsLines(body)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("cross-account move changed properties: %v", got)
	}
	if got := a.DAV(t, http.MethodGet, src+"object.ics", nil, ""); got.StatusCode != http.StatusNotFound {
		t.Error("cross-account move kept the source")
	}
}

func TestMovingAnEventWithinItsCalendarDoesNothing(t *testing.T) {
	a, c := SignedIn(t, User)
	src := c.CreateCalendar(t, "Source", "")
	a.Put(t, src+"object.ics", "text/calendar", Event("original", "Original"))
	before := a.DAV(t, http.MethodGet, src+"object.ics", nil, "")
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + src}}, Here+src+"object.ics")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("move to the same calendar: %s", resp.Status)
	}
	after := a.DAV(t, http.MethodGet, src+"object.ics", nil, "")
	if after.StatusCode != http.StatusOK || after.Header.Get("ETag") != before.Header.Get("ETag") || after.Text != before.Text {
		t.Error("moving within the same calendar changed the object")
	}
}

func TestMovingAnEventRollsBackWhenTheSourceChanged(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"), Object(personal+"object.ics", Event("original", "Original")))
	d.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == personal+"object.ics" {
			if r.Header.Get("If-Match") != `"1"` {
				t.Errorf("source delete lost its original ETag: %v", r.Header)
			}
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		d.ServeHTTP(w, r)
	})
	a, c := SignedIn(t, User, WithDAV(d))
	dst := c.CreateCalendar(t, "Destination", "")
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + dst}}, d.At()+personal+"object.ics")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("stale source move answered %s", resp.Status)
	}
	if got := a.DAV(t, http.MethodGet, dst+"object.ics", nil, ""); got.StatusCode != http.StatusNotFound {
		t.Error("stale-source move left a destination copy")
	}
}

func TestMovingAnEventNeverRollsBackADestinationChangedElsewhere(t *testing.T) {
	personal := DAVHome + "personal/"
	d := DAV(t, Calendar(personal, "Personal"), Object(personal+"object.ics", Event("original", "Original")))
	var a *App
	var destination string
	d.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == personal+"object.ics" {
			// Another client changes the newly created destination before rollback.
			changed := a.DAV(t, http.MethodPut, destination, map[string]string{"Content-Type": "text/calendar"}, Event("original", "Changed elsewhere"))
			if changed.StatusCode/100 != 2 {
				t.Errorf("concurrent destination edit: %s", changed.Status)
			}
			w.WriteHeader(http.StatusForbidden)
			return
		}
		d.ServeHTTP(w, r)
	})
	var c *Client
	a, c = SignedIn(t, User, WithDAV(d))
	dst := c.CreateCalendar(t, "Destination", "")
	destination = dst + "object.ics"
	resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + dst}}, d.At()+personal+"object.ics")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refused deletion and rollback answered %s", resp.Status)
	}
	if got := a.DAV(t, http.MethodGet, destination, nil, ""); got.StatusCode != http.StatusOK || !strings.Contains(got.Text, "SUMMARY:Changed elsewhere") {
		t.Error("rollback deleted a destination changed elsewhere")
	}
	source := c.Get(t, "calendar.event.raw", d.At()+personal+"object.ics")
	if source.StatusCode != http.StatusOK || !strings.Contains(source.Text, "SUMMARY:Original") {
		t.Error("failed move lost the original source")
	}
}

func TestMovingAnEventPreservesTheDestinationWhenDeleteAppliedButReplyFailed(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusPreconditionFailed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			imap := IMAP(t)
			holder := Alborz(t, WithIMAP(imap))
			src := holder.SignIn(t, User).CreateCalendar(t, "Source", "")
			holder.Put(t, src+"object.ics", "text/calendar", Event("original", "Original"))
			upstream, err := url.Parse(holder.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(upstream) }}
			proxy.ModifyResponse = func(resp *http.Response) error {
				if resp.Request.Method == http.MethodDelete && resp.Request.URL.Path == src+"object.ics" && resp.StatusCode == http.StatusNoContent {
					// The upstream applied DELETE, but its proxy fails to deliver success.
					resp.StatusCode, resp.Status = status, http.StatusText(status)
				}
				return nil
			}
			between := httptest.NewServer(proxy)
			t.Cleanup(between.Close)
			a := Alborz(t, WithIMAP(imap), Upstream(Domain, "caldav+insecure://"+strings.TrimPrefix(between.URL, "http://")+holder.Path("dav.collections")))
			c := a.SignIn(t, User)
			dst := c.CreateCalendar(t, "Destination", "")
			resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + Here + dst}}, At(between.URL)+src+"object.ics")
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("ambiguous deletion answered %s", resp.Status)
			}
			if got := holder.DAV(t, http.MethodGet, src+"object.ics", nil, ""); got.StatusCode != http.StatusNotFound {
				t.Fatalf("test source DELETE was not applied: %s", got.Status)
			}
			if got := a.DAV(t, http.MethodGet, dst+"object.ics", nil, ""); got.StatusCode != http.StatusOK || !strings.Contains(got.Text, "UID:original") {
				t.Error("ambiguous DELETE rolled back the last copy")
			}
		})
	}
}

func TestMovingSchedulingObjectsRequiresASafeFallback(t *testing.T) {
	for _, property := range []string{"ORGANIZER:mailto:host@example.org", "ATTENDEE:mailto:guest@example.org", ""} {
		for _, capability := range []string{"source schedules", "destination schedules", "source capability fails", "destination capability fails"} {
			name, _, _ := strings.Cut(property, ":")
			if property == "" {
				if capability != "source schedules" {
					continue
				}
				name = "plain object"
			}
			t.Run(name+"/"+capability, func(t *testing.T) {
				imap := IMAP(t)
				holder := Alborz(t, WithIMAP(imap))
				owner := holder.SignIn(t, User)
				src, dst := owner.CreateCalendar(t, "Source", ""), owner.CreateCalendar(t, "Destination", "")
				body := strings.Replace(Event("original", "Original"), "END:VEVENT", property+"\r\nEND:VEVENT", 1)
				holder.Put(t, src+"object.ics", "text/calendar", body)
				upstream, err := url.Parse(holder.URL)
				if err != nil {
					t.Fatal(err)
				}
				var writes atomic.Int32
				proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(upstream) }}
				guarded := src
				if strings.HasPrefix(capability, "destination") {
					guarded = dst
				}
				proxy.ModifyResponse = func(resp *http.Response) error {
					if resp.Request.Method == http.MethodOptions && resp.Request.URL.Path == guarded {
						if strings.HasSuffix(capability, "fails") {
							resp.StatusCode, resp.Status = http.StatusServiceUnavailable, "503 Service Unavailable"
						} else {
							resp.Header.Set("DAV", "1, 3, calendar-access, calendar-auto-schedule")
						}
					}
					return nil
				}
				between := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "MOVE" {
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					if r.Method == http.MethodPut || r.Method == http.MethodDelete {
						writes.Add(1)
					}
					proxy.ServeHTTP(w, r)
				}))
				t.Cleanup(between.Close)
				a := Alborz(t, WithIMAP(imap), Upstream(Domain, "caldav+insecure://"+strings.TrimPrefix(between.URL, "http://")+holder.Path("dav.collections")))
				c := a.SignIn(t, User)
				resp := c.Post(t, "calendar.event.move", url.Values{"to": {User + "|" + At(between.URL) + dst}}, At(between.URL)+src+"object.ics")
				if property == "" {
					if resp.StatusCode != http.StatusFound || writes.Load() != 2 {
						t.Errorf("plain-object fallback answered %s, sent %d writes", resp.Status, writes.Load())
					}
					return
				}
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Errorf("unsafe fallback answered %s", resp.Status)
				}
				if writes.Load() != 0 {
					t.Errorf("unsafe fallback sent %d PUT/DELETE requests", writes.Load())
				}
			})
		}
	}
}

// servedFeed publishes body as a calendar feed.
func servedFeed(t *testing.T, body string) *httptest.Server {
	t.Helper()
	f := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

// clubNight repeats weekly from today in Berlin, with an exception, an extra date, a moved
// instance and an organizer, as a club's published calendar has them.
func clubNight() string {
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("20060102") }
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nMETHOD:PUBLISH\r\nX-WR-CALNAME:Club\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\n" +
		"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:club-night\r\nDTSTAMP:20260901T080000Z\r\n" +
		"DTSTART;TZID=Europe/Berlin:" + day(0) + "T190000\r\nDTEND;TZID=Europe/Berlin:" + day(0) + "T210000\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=10\r\nEXDATE;TZID=Europe/Berlin:" + day(14) + "T190000\r\n" +
		"RDATE;TZID=Europe/Berlin:" + day(3) + "T190000\r\nSUMMARY:Club night\r\n" +
		"ORGANIZER:mailto:host@example.org\r\nATTENDEE:mailto:guest@example.org\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:club-night\r\nDTSTAMP:20260901T080000Z\r\n" +
		"RECURRENCE-ID;TZID=Europe/Berlin:" + day(7) + "T190000\r\n" +
		"DTSTART;TZID=Europe/Berlin:" + day(7) + "T200000\r\nDTEND;TZID=Europe/Berlin:" + day(7) + "T220000\r\n" +
		"SUMMARY:Club night, late\r\nORGANIZER:mailto:host@example.org\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

// icsLines are an object's unfolded content lines.
func icsLines(raw string) []string {
	return strings.Split(strings.ReplaceAll(raw, "\r\n ", ""), "\r\n")
}

// withName are the lines of the property called name.
func withName(lines []string, name string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, name+":") || strings.HasPrefix(l, name+";") {
			out = append(out, l)
		}
	}
	return out
}

// via is the value of the LINK;LINKREL=via of the kind, or "".
func via(lines []string, kind string) string {
	for _, l := range withName(lines, "LINK") {
		params, value, _ := strings.Cut(l, ":")
		if strings.Contains(params, ";LINKREL=via") && strings.Contains(params, ";VALUE="+kind) {
			return value
		}
	}
	return ""
}

// copied posts the copy and returns where it landed.
func copied(t *testing.T, c *Client, to, route string, args ...any) string {
	t.Helper()
	resp := c.Post(t, route, url.Values{"to": {to}}, args...)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("copying answered %s", resp.Status)
	}
	return resp.Header.Get("Location")
}

// raw is an object's source, read through the raw route of the page at path.
func raw(t *testing.T, a *App, c *Client, path string) string {
	t.Helper()
	u, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	name, values, ok := a.Server.Match(u)
	if !ok {
		t.Fatalf("%s is no route", path)
	}
	return c.Page(t, name+".raw", values["path"], u.Query())
}

// A feed's event taken into one's own calendar is a new event there, no invitation, and says where it came from.
func TestCopyingAFeedEventMakesAnEventOfOurOwn(t *testing.T) {
	f := servedFeed(t, clubNight())
	a, c := subscriber(t, f)
	subscribe(t, c, f.URL+"/club.ics")
	plans := Here + c.CreateCalendar(t, "Plans", "")

	linked := events(t, a, c)
	if len(linked) == 0 {
		t.Fatal("the agenda links no event of the feed")
	}
	event := linked[0]
	landed := copied(t, c, User+"|"+plans, "calendar.event.copy", event)
	if !strings.Contains(landed, url.PathEscape(plans)) {
		t.Fatalf("the copy landed on %s, not an event in Plans", landed)
	}

	lines := icsLines(raw(t, a, c, landed))
	uids := withName(lines, "UID")
	if len(uids) != 2 || uids[0] != uids[1] || uids[0] == "UID:club-night" {
		t.Errorf("the copy's UIDs are %q, want one fresh UID on the series and its moved instance", uids)
	}
	for _, gone := range []string{"METHOD", "ORGANIZER", "ATTENDEE"} {
		if got := withName(lines, gone); len(got) > 0 {
			t.Errorf("the copy keeps %q", got)
		}
	}
	for _, kept := range []string{"RRULE", "EXDATE", "RDATE", "RECURRENCE-ID", "TZID"} {
		if len(withName(lines, kept)) == 0 {
			t.Errorf("the copy lost its %s", kept)
		}
	}
	if got := via(lines, "URI"); got != f.URL+"/club.ics" {
		t.Errorf("the copy links to %q, want the feed", got)
	}
	if got := via(lines, "UID"); got != "club-night" {
		t.Errorf("the copy names %q as its original, want club-night", got)
	}
}

// A task copies as an event does, and the notice's undo deletes the copy and nothing else.
func TestCopyingATaskIsUndoneByDeletingTheCopy(t *testing.T) {
	a, c := SignedIn(t, User)
	chores := c.CreateCalendar(t, "Chores", "tasks")
	errands := Here + c.CreateCalendar(t, "Errands", "tasks")
	a.Put(t, chores+"water.ics", "text/calendar", Todo("water", "Water the ferns"))

	landed := copied(t, c, User+"|"+errands, "calendar.task.copy", Here+chores+"water.ics", url.Values{"account": {User}})
	if !strings.Contains(landed, url.PathEscape(errands)) {
		t.Fatalf("the copy landed on %s, not a task in Errands", landed)
	}
	lines := icsLines(raw(t, a, c, landed))
	if uids := withName(lines, "UID"); len(uids) != 1 || uids[0] == "UID:water" || !slices.Equal(withName(lines, "SUMMARY"), []string{"SUMMARY:Water the ferns"}) {
		t.Errorf("the copy is %q, want Water the ferns under a fresh UID", lines)
	}
	if got := via(lines, "URI"); !strings.HasSuffix(got, chores) {
		t.Errorf("the copy links to %q, want the Chores list", got)
	}

	page := html.UnescapeString(c.Follow(t, landed))
	undo := regexp.MustCompile(`<form method="post" action="([^"]+/delete[^"]*)" class="notice-action">\s*((?:<input[^>]+>)+)`).FindStringSubmatch(page)
	if undo == nil {
		t.Fatal("the copy's notice offers no undo")
	}
	fields := url.Values{}
	for _, in := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(undo[2], -1) {
		fields.Add(in[1], in[2])
	}
	if resp := c.PostTo(t, undo[1], fields); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != fields.Get("next") {
		t.Fatalf("the undo answered %s to %s", resp.Status, resp.Header.Get("Location"))
	}
	u, err := url.Parse(landed)
	if err != nil {
		t.Fatal(err)
	}
	_, values, ok := a.Server.Match(u)
	if !ok {
		t.Fatalf("the copy's page %s is no route", landed)
	}
	if resp := a.DAV(t, http.MethodGet, strings.TrimPrefix(values["path"], Here), nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the copy is still there after the undo: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, chores+"water.ics", nil, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("the undo took the original: %s", resp.Status)
	}
}

// With nothing writable the form is offered all the same, and the server says why it did nothing.
func TestCopyingWithNoWritableCalendarIsRefused(t *testing.T) {
	f := servedFeed(t, clubNight())
	a, c := subscriber(t, f)
	subscribe(t, c, f.URL+"/club.ics")
	linked := events(t, a, c)
	if len(linked) == 0 {
		t.Fatal("the agenda links no event of the feed")
	}
	resp := c.Post(t, "calendar.event.copy", url.Values{}, linked[0])
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("copying to nowhere answered %s, want the form refused", resp.Status)
	}
	if got := c.Listed(t); len(got) != 1 {
		t.Errorf("the calendars are %v after a refused copy, want the feed alone", got)
	}
}

func TestBulkCopyCopiesOnlyCheckedCalendarObjects(t *testing.T) {
	for _, tc := range []struct{ route, holds, component string }{
		{"calendar.events.copy", "", "VEVENT"},
		{"calendar.tasks.copy", "tasks", "VTODO"},
		{"calendar.journal.copy", "journal", "VJOURNAL"},
	} {
		t.Run(tc.component, func(t *testing.T) {
			a, c := SignedIn(t, User)
			src := c.CreateCalendar(t, "Source", tc.holds)
			dst := c.CreateCalendar(t, "Destination", tc.holds)
			originals := map[string]string{}
			for _, id := range []string{"one", "two", "unchecked"} {
				body := strings.ReplaceAll(Event(id, id), "VEVENT", tc.component)
				originals[id] = body
				a.Put(t, src+id+".ics", "text/calendar", body)
			}
			next := "/calendar/search?query=checked&view=list"
			form := url.Values{"paths": {User + "|" + Here + src + "one.ics", User + "|" + Here + src + "two.ics"}, "next": {next}}
			form.Set("to", User+"|"+Here+dst)
			resp := c.Post(t, tc.route, form)
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != next {
				t.Fatalf("copy answered %s to %q", resp.Status, resp.Header.Get("Location"))
			}
			copied := c.Get(t, "calendar.collection.export", Here+dst).Text
			if len(withName(icsLines(copied), "UID")) != 2 || !strings.Contains(copied, "SUMMARY:one") || !strings.Contains(copied, "SUMMARY:two") || strings.Contains(copied, "SUMMARY:unchecked") {
				t.Fatalf("destination does not hold exactly the checked copies: %s", copied)
			}
			for id, body := range originals {
				if got := a.DAV(t, http.MethodGet, src+id+".ics", nil, ""); got.StatusCode != http.StatusOK || got.Text != body {
					t.Errorf("copy changed source %s", id)
				}
				if strings.Contains(copied, "\r\nUID:"+id+"\r\n") {
					t.Errorf("copy reused source UID %q", id)
				}
			}
		})
	}
}

func TestBulkCopyAcrossAccountsUsesEachSourcesCalendar(t *testing.T) {
	for _, tc := range []struct{ route, holds, component string }{
		{"calendar.events.copy", "", "VEVENT"},
		{"calendar.tasks.copy", "tasks", "VTODO"},
		{"calendar.journal.copy", "journal", "VJOURNAL"},
	} {
		t.Run(tc.component, func(t *testing.T) {
			a, c := SignedIn(t, User)
			first := c.CreateCalendar(t, "First", tc.holds)
			body := strings.ReplaceAll(Event("first", "First"), "VEVENT", tc.component)
			a.Put(t, first+"object.ics", "text/calendar", body)
			c.SignIn(t, User2)
			for _, name := range []string{"Second", "Target"} {
				if resp := c.Post(t, "calendar.calendars.create", url.Values{"account": {User2}, "place": {"here"}, "name": {name}, "holds": {tc.holds}}); resp.StatusCode != http.StatusFound {
					t.Fatalf("create %s: %s", name, resp.Status)
				}
			}
			second, target := CalendarsOf(User2)+"second/", CalendarsOf(User2)+"target/"
			body2 := strings.ReplaceAll(Event("second", "Second"), "VEVENT", tc.component)
			req, err := http.NewRequest(http.MethodPut, a.URL+second+"object.ics", strings.NewReader(body2))
			if err != nil {
				t.Fatal(err)
			}
			req.SetBasicAuth(User2, Password)
			req.Header.Set("Content-Type", "text/calendar")
			if resp := a.Client().Do(t, req); resp.StatusCode != http.StatusCreated {
				t.Fatalf("write second source: %s", resp.Status)
			}
			from := "/calendar?view=list&account=" + url.QueryEscape(User)
			form := url.Values{"paths": {User + "|" + Here + first + "object.ics", User2 + "|" + Here + second + "object.ics"}, "to": {User2 + "|" + Here + target}}
			// A direct POST also honors the originating list when next is absent.
			resp := c.Post(t, tc.route, form, url.Values{"from": {from}})
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != from {
				t.Fatalf("copy answered %s to %q, want %q", resp.Status, resp.Header.Get("Location"), from)
			}
			copied := c.Get(t, "calendar.collection.export", Here+target, url.Values{"account": {User2}}).Text
			lines := icsLines(copied)
			if len(withName(lines, "UID")) != 2 {
				t.Fatalf("expected two copies: %s", copied)
			}
			for _, source := range []string{first, second} {
				if !strings.Contains(strings.Join(withName(lines, "LINK"), "\r\n"), source) {
					t.Errorf("copy lost its source calendar %q: %s", source, copied)
				}
			}
			if got := a.DAV(t, http.MethodGet, first+"object.ics", nil, ""); got.Text != body {
				t.Error("first source changed")
			}
			if got := c.Get(t, "calendar.collection.export", Here+second, url.Values{"account": {User2}}).Text; !strings.Contains(got, "UID:second\r\n") {
				t.Error("second source changed")
			}
		})
	}
}

func TestBulkCopyFromReadOnlyCalendarsAndInvalidDestinations(t *testing.T) {
	for _, tc := range []struct{ route, holds, component string }{
		{"calendar.events.copy", "", "VEVENT"},
		{"calendar.tasks.copy", "tasks", "VTODO"},
		{"calendar.journal.copy", "journal", "VJOURNAL"},
	} {
		t.Run(tc.component, func(t *testing.T) {
			a, owner := SignedIn(t, User)
			src := owner.CreateCalendar(t, "Source", tc.holds)
			a.Put(t, src+"object.ics", "text/calendar", strings.ReplaceAll(Event("original", "Original"), "VEVENT", tc.component))
			if resp := owner.Post(t, "calendar.collection.share", url.Values{"to": {User2}, "access": {"read"}}, Here+src); resp.StatusCode != http.StatusFound {
				t.Fatalf("share: %s", resp.Status)
			}
			guest := a.SignIn(t, User2)
			shared := Here + CalendarsOf(User2) + "source~" + User + "/"
			guest.Post(t, "calendar.collection.accept", url.Values{}, shared)
			for _, cal := range []struct{ name, holds string }{{"Target", tc.holds}, {"Wrong", "tasks"}} {
				if tc.holds == "tasks" && cal.name == "Wrong" {
					cal.holds = "journal"
				}
				if resp := guest.Post(t, "calendar.calendars.create", url.Values{"account": {User2}, "place": {"here"}, "name": {cal.name}, "holds": {cal.holds}}); resp.StatusCode != http.StatusFound {
					t.Fatalf("create %s: %s", cal.name, resp.Status)
				}
			}
			paths := []string{User2 + "|" + shared + "object.ics"}
			for _, to := range []string{"", User2 + "|" + shared, User2 + "|" + Here + CalendarsOf(User2) + "wrong/"} {
				resp := guest.Post(t, tc.route, url.Values{"paths": paths, "to": {to}})
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Errorf("invalid destination %q answered %s", to, resp.Status)
				}
			}
			target := Here + CalendarsOf(User2) + "target/"
			resp := guest.Post(t, tc.route, url.Values{"paths": paths, "to": {User2 + "|" + target}})
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("copy from read-only calendar: %s", resp.Status)
			}
			copied := guest.Get(t, "calendar.collection.export", target).Text
			if !strings.Contains(copied, "SUMMARY:Original") || strings.Contains(copied, "\r\nUID:original\r\n") {
				t.Errorf("read-only source was not copied with a fresh UID: %s", copied)
			}
			if got := a.DAV(t, http.MethodGet, src+"object.ics", nil, ""); got.StatusCode != http.StatusOK {
				t.Error("copy removed the read-only source")
			}
		})
	}
}

func TestBulkCopyDeduplicatesRecurringEventSelections(t *testing.T) {
	a, c := SignedIn(t, User)
	src := c.CreateCalendar(t, "Source", "")
	dst := Here + c.CreateCalendar(t, "Target", "")
	a.Put(t, src+"series.ics", "text/calendar", strings.ReplaceAll(clubNight(), "METHOD:PUBLISH\r\n", ""))
	ref := User + "|" + Here + src + "series.ics"
	resp := c.Post(t, "calendar.events.copy", url.Values{"paths": {ref, ref}, "to": {User + "|" + dst}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("copy series: %s", resp.Status)
	}
	if uids := withName(icsLines(c.Get(t, "calendar.collection.export", dst).Text), "UID"); len(uids) != 2 {
		t.Errorf("the selected occurrences made %d components, want one series of two components", len(uids))
	}
}

func TestBulkCopyRetriesOnlyUnfinishedObjects(t *testing.T) {
	src, dst := DAVHome+"source/", DAVHome+"target/"
	d := DAV(t, Calendar(src, "Source"), Calendar(dst, "Target"), Object(src+"one.ics", Event("one", "One")), Object(src+"two.ics", Event("two", "Two")))
	var attempts atomic.Int32
	between := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, dst) && attempts.Add(1) == 2 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		d.ServeHTTP(w, r)
	}))
	t.Cleanup(between.Close)
	_, c := SignedIn(t, User, Upstream(Domain, "caldav+insecure://"+strings.TrimPrefix(between.URL, "http://")+"/dav"))
	base := At(between.URL)
	first, second := User+"|"+base+src+"one.ics", User+"|"+base+src+"two.ics"
	form := url.Values{"paths": {first, second}, "to": {User + "|" + base + dst}, "next": {"/calendar?view=list"}}
	resp := c.Post(t, "calendar.events.copy", form)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("partial copy answered %s, want a retry form", resp.Status)
	}
	page := html.UnescapeString(resp.Text)
	remaining := regexp.MustCompile(`name="paths" value="([^"]*)"`).FindAllStringSubmatch(page, -1)
	if len(remaining) != 1 || remaining[0][1] != second {
		t.Fatalf("retry selection is %v, want only %q", remaining, second)
	}
	if len(d.Puts()) != 1 || !strings.Contains(d.Puts()[0], "SUMMARY:One") {
		t.Fatalf("first copy did not finish: %v", d.Puts())
	}
	form["paths"] = []string{remaining[0][1]}
	if retried := c.Post(t, "calendar.events.copy", form); retried.StatusCode != http.StatusFound {
		t.Fatalf("retry answered %s", retried.Status)
	}
	if got := d.Puts(); len(got) != 2 || !strings.Contains(got[1], "SUMMARY:Two") {
		t.Errorf("retry copied the wrong objects: %v", got)
	}
}

func TestBulkCopyOfAFeedPreservesTheCompleteSeries(t *testing.T) {
	f := servedFeed(t, clubNight())
	a, c := subscriber(t, f)
	subscribe(t, c, f.URL+"/club.ics")
	dst := Here + c.CreateCalendar(t, "Plans", "")
	src := events(t, a, c)[0]
	resp := c.Post(t, "calendar.events.copy", url.Values{"paths": {User + "|" + src}, "to": {User + "|" + dst}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("copy feed: %s", resp.Status)
	}
	lines := icsLines(c.Get(t, "calendar.collection.export", dst).Text)
	if uids := withName(lines, "UID"); len(uids) != 2 || uids[0] != uids[1] || uids[0] == "UID:club-night" {
		t.Errorf("copy did not give the complete series a fresh UID: %v", uids)
	}
	for _, kept := range []string{"RRULE", "EXDATE", "RDATE", "RECURRENCE-ID", "TZID"} {
		if len(withName(lines, kept)) == 0 {
			t.Errorf("copy lost %s", kept)
		}
	}
	for _, removed := range []string{"METHOD", "ORGANIZER", "ATTENDEE"} {
		if len(withName(lines, removed)) > 0 {
			t.Errorf("copy kept scheduling property %s", removed)
		}
	}
	if via(lines, "URI") != f.URL+"/club.ics" || via(lines, "UID") != "club-night" {
		t.Errorf("copy lost feed provenance: %v", withName(lines, "LINK"))
	}
}

func TestBulkCopyWithoutSelectionReturnsToTheList(t *testing.T) {
	_, c := SignedIn(t, User)
	c.CreateCalendar(t, "Plans", "")
	for _, route := range []string{"calendar.events.copy", "calendar.tasks.copy", "calendar.journal.copy"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			form := url.Values{"next": {"/calendar?view=list"}}
			var resp *Response
			if method == http.MethodGet {
				resp = c.Get(t, route, form)
			} else {
				resp = c.Post(t, route, form)
			}
			if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != form.Get("next") {
				t.Errorf("%s %s without selection answered %s to %q", method, route, resp.Status, resp.Header.Get("Location"))
			}
		}
	}
}
