package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// eventPage PUTs a VEVENT as a DAV client would and opens its edit form.
func eventPage(t *testing.T, a *App, c *Client, coll, name, extra string) string {
	t.Helper()
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + name +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20261005T100000Z\r\nDTEND:20261005T110000Z\r\nSUMMARY:Review\r\n" +
		extra + "END:VEVENT\r\nEND:VCALENDAR\r\n"
	a.Put(t, coll+name+".ics", "text/calendar", body)
	// Pages hold the /@host prefix a DAV path carries on an alborz with no public URL (ADR 28).
	return c.Page(t, "calendar.event.update", Here+coll+name+".ics")
}

// The event form folds repeat, attendees and description away: closed on an empty
// create form and when editing an event that fills none of them, auto-opened when
// editing one that fills any.
func TestEventFormMoreOptions(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")

	if page := c.Page(t, "calendar.event.create"); strings.Contains(page, `<details class="more-options" open`) {
		t.Errorf("an empty create form opens the more-options block")
	}

	for _, open := range []struct{ what, name, extra string }{
		{"a description", "open-description", "DESCRIPTION:Notes\r\n"},
		{"an attendee", "open-attendee", "ATTENDEE:mailto:b@example.org\r\n"},
		{"a repeat rule", "open-repeat", "RRULE:FREQ=DAILY\r\n"},
	} {
		page := eventPage(t, a, c, coll, open.name, open.extra)
		if !strings.Contains(page, `<details class="more-options" open`) {
			t.Errorf("editing an event with %s does not open the more-options block", open.what)
		}
	}

	page := eventPage(t, a, c, coll, "plain", "")
	if strings.Contains(page, `<details class="more-options" open`) {
		t.Errorf("editing an event with nothing to show still opens the more-options block")
	}
	if !strings.Contains(page, `<details class="more-options">`) {
		t.Errorf("the more-options block is missing from the edit form")
	}
	a.DAV(t, http.MethodDelete, coll+"plain.ics", nil, "")
}

// A rule the form's selects cannot express, like every second day, still folds the block open:
// the form holds it as the custom choice rather than losing it.
func TestEventFormCustomRepeatOpens(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	page := eventPage(t, a, c, coll, "custom", "RRULE:FREQ=DAILY;INTERVAL=2\r\n")
	if !strings.Contains(page, `<details class="more-options" open`) {
		t.Errorf("editing an event with a custom repeat rule does not open the more-options block")
	}
	if !strings.Contains(page, `<option value="custom" selected>`) {
		t.Errorf("the custom repeat rule is not shown as the chosen rule")
	}
}

// A refused save keeps what the user typed in the folded block: the attendees and the
// description survive the round-trip and the fold re-opens to show them.
func TestEventFormRejectKeepsMoreOptions(t *testing.T) {
	_, c := SignedIn(t, User)
	resp := c.Post(t, "calendar.event.create", url.Values{
		"summary":     {""},
		"attendees":   {"b@example.org"},
		"description": {"Notes from the test"},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a form without a summary answered %s, want the form refused", resp.Status)
	}
	page := resp.Text
	if !strings.Contains(page, `<details class="more-options" open`) {
		t.Errorf("a refused form with folded input does not open the more-options block")
	}
	if !strings.Contains(page, ">Notes from the test</textarea>") {
		t.Errorf("the refused form loses the typed description")
	}
	if !strings.Contains(page, ">b@example.org</textarea>") {
		t.Errorf("the refused form loses the typed attendees")
	}
}

// The description reads as prose: a three-row textarea, like the attendees field, not a one-line input.
func TestEventFormDescriptionIsATextarea(t *testing.T) {
	_, c := SignedIn(t, User)
	page := c.Page(t, "calendar.event.create")
	if !strings.Contains(page, `<textarea name="description" id="description" dir="auto" rows="3">`) {
		t.Errorf("the description is not a three-row textarea")
	}
}
