package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A DELETE carrying If-Match must match the object's tag (RFC 9110 13.1.1).
func TestADeleteWithAStaleTagKeepsTheObject(t *testing.T) {
	a := Alborz(t)
	cal := a.SignIn(t, User).CreateCalendar(t, "Plans", "")
	a.Put(t, cal+"dentist.ics", "text/calendar", Event("dentist", "Dentist"))

	if resp := a.DAV(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": `"stale"`}, ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("a delete with a stale tag answered %s", resp.Status)
	}
	resp := a.DAV(t, http.MethodGet, cal+"dentist.ics", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the object is gone after a refused delete: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": resp.Header.Get("ETag")}, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a delete with the object's own tag answered %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, cal+"dentist.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the deleted object is still there: %s", resp.Status)
	}
}

// One UID per calendar (RFC 4791 5.3.2.1).
func TestASecondObjectWithATakenUIDIsRefused(t *testing.T) {
	a := Alborz(t)
	cal := a.SignIn(t, User).CreateCalendar(t, "Work", "")
	a.Put(t, cal+"review.ics", "text/calendar", Event("review", "Review"))
	ics := map[string]string{"Content-Type": "text/calendar"}

	if resp := a.DAV(t, http.MethodPut, cal+"copy.ics", ics, Event("review", "Review again")); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second object with a taken UID answered %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, cal+"copy.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused object was written anyway: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodPut, cal+"review.ics", ics, Event("review", "Review, moved")); resp.StatusCode/100 != 2 {
		t.Fatalf("rewriting the object that holds the UID answered %s", resp.Status)
	}
	if kept := a.DAV(t, http.MethodGet, cal+"review.ics", nil, "").Text; !strings.Contains(kept, "Review, moved") {
		t.Errorf("the rewrite did not stick: %s", kept)
	}
}

// Force refresh lists what another client added on a remote server.
func TestACalendarCreatedElsewhereIsListedOnRefresh(t *testing.T) {
	d := DAV(t, Calendar(DAVHome+"personal/", "Personal"))
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	work := d.At() + DAVHome + "work/"
	if rail := c.Page(t, "calendar.calendars.create"); strings.Contains(rail, work) {
		t.Fatalf("%s is listed before it is made", work)
	}
	d.Apply(Calendar(DAVHome+"work/", "Work"))
	if resp := c.Post(t, "calendar.refresh", url.Values{}); resp.StatusCode != http.StatusFound {
		t.Fatalf("refreshing answered %s", resp.Status)
	}
	if rail := c.Page(t, "calendar.calendars.create"); !strings.Contains(rail, work) {
		t.Errorf("%s is not listed after asking the server again", work)
	}
}
