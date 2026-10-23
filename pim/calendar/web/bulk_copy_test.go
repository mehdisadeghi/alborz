package web_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

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
			chooser := html.UnescapeString(c.Page(t, tc.route, form))
			for _, value := range append(form["paths"], next) {
				if !strings.Contains(chooser, `value="`+value+`"`) {
					t.Errorf("chooser lost %q", value)
				}
			}
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
				if !strings.Contains(html.UnescapeString(resp.Text), `value="`+paths[0]+`"`) {
					t.Error("refused destination lost selection")
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
	if !Says(t, page, "notice.itemscopied", 1) {
		t.Error("partial copy does not report the completed copy")
	}
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
