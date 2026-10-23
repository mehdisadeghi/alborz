package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

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
			page := c.Page(t, "calendar."+tc.kind, Here+src+"object.ics")
			for _, action := range []string{"copy", "move"} {
				if !strings.Contains(page, a.Path("calendar."+tc.kind+"."+action, Here+src+"object.ics")) {
					t.Errorf("object offers no %s action", action)
				}
			}
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
	page := c.Page(t, "calendar.event", src)
	if strings.Contains(page, a.Path("calendar.event.move", src)) || !strings.Contains(page, a.Path("calendar.event.copy", src)) {
		t.Error("read-only page should offer copy only")
	}
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
			page := guest.Page(t, "calendar."+tc.kind, shared+"object.ics")
			if strings.Contains(page, a.Path("calendar."+tc.kind+".move", shared+"object.ics")) || !strings.Contains(page, a.Path("calendar."+tc.kind+".copy", shared+"object.ics")) {
				t.Error("read-only shared object should offer copy only")
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
	chooser := c.Page(t, "calendar.event.move", Here+src+"object.ics", url.Values{"account": {User}})
	if !strings.Contains(chooser, `value="`+User2+`|`+dst+`"`) {
		t.Fatal("chooser offers no compatible calendar in the other account")
	}
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
			name := strings.Split(property, ":")[0]
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
