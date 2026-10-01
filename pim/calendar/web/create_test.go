package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestCreateCalendarWithNoneYet(t *testing.T) {
	d := DAV(t)
	c := Alborz(t, WithDAV(d)).SignIn(t, User)

	page := c.Page(t, "calendar.event.create")
	if choice := regexp.MustCompile(`(?s)<select name="calendar"[^>]*>(.*?)</select>`).FindStringSubmatch(page); choice == nil || strings.Contains(choice[1], "<option") {
		t.Fatalf("the new-event form has no empty calendar choice")
	}
	resp := c.Post(t, "calendar.event.create", url.Values{"summary": {"Lunch"}, "start": {"2026-09-28T12:00"}, "end": {"2026-09-28T13:00"}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !Says(t, resp.Text, "form.destinationneeded") {
		t.Fatalf("saving an event with nowhere to save answered %s, want the form refused", resp.Status)
	}

	form := url.Values{"name": {"First"}, "color": {"#22aa55"}, "holds": {"both"}, "place": {"domain"}}
	if resp := c.Post(t, "calendar.calendars.create", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("creating the first calendar answered %s, want a redirect", resp.Status)
	}
	if n := len(d.Calendars()); n != 1 {
		t.Fatalf("the server was not asked to make the calendar: %d exist", n)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(c.Page(t, "calendar.month"), "First") {
			return
		}
	}
	t.Errorf("the created calendar never appeared in the rail")
}

// The guarded client refuses the loopback stub, which is the refusal under test.
func TestSubscribingToAnUnreadableFeedIsRefused(t *testing.T) {
	d := DAV(t)
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	if resp := c.Post(t, "calendar.subscribe", url.Values{"address": {d.URL + "/feed.ics"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unreadable feed answered %s, want the form back", resp.Status)
	}
	if n := len(d.Calendars()); n != 0 {
		t.Errorf("an address made a collection on the server")
	}
}
