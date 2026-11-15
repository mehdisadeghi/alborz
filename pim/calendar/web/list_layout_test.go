package web_test

import (
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A multi-day event keeps one row per day it spans in the day-grouped and
// day views, marked where it continues, and a single row dated start – end
// in the flat list.
func TestListMultiDayEvents(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "events")
	a.Put(t, coll+"retreat.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:retreat\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260914T220000Z\r\nDTEND:20260916T070000Z\r\n"+
		"SUMMARY:Retreat\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	a.Put(t, coll+"conference.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:conference\r\nDTSTAMP:20260901T080000Z\r\nDTSTART;VALUE=DATE:20260915\r\nDTEND;VALUE=DATE:20260917\r\n"+
		"SUMMARY:Conference\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	a.Put(t, coll+"standup.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:standup\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260915T100000Z\r\nDTEND:20260915T103000Z\r\n"+
		"SUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")

	// The select cell repeats the summary in its aria-label, so count the rows' links.
	rows := func(text, summary string) int { return strings.Count(text, "dir=\"auto\">"+summary+"</a>") }

	// The flat list shows each span once, dated first day – last day.
	page := c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}})
	if n := rows(page, "Retreat"); n != 1 {
		t.Errorf("the flat list shows Retreat %d times, want once:\n%s", n, page)
	}
	if !strings.Contains(page, "Sep 14 – Sep 16") {
		t.Errorf("the flat list does not date Retreat by its span:\n%s", page)
	}

	// The day-grouped list keeps a row per spanned day, marked where it
	// continues, the whole days above the timed events.
	page = c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}, "view": {"list"}, "group": {"day"}})
	for summary, want := range map[string]int{"Retreat": 3, "Conference": 2, "Standup": 1} {
		if n := rows(page, summary); n != want {
			t.Errorf("the day-grouped list shows %s %d times, want %d:\n%s", summary, n, want, page)
		}
	}
	if n := strings.Count(page, "continues-before"); n != 3 { // Retreat's middle and end, Conference's end.
		t.Errorf("the day-grouped list marks %d rows as continuing before, want 3:\n%s", n, page)
	}
	if n := strings.Count(page, "continues-after"); n != 3 { // Retreat's start and middle, Conference's start.
		t.Errorf("the day-grouped list marks %d rows as continuing after, want 3:\n%s", n, page)
	}
	if before, after := strings.Index(page, "Conference"), strings.Index(page, "Standup"); before > after {
		t.Errorf("the day-grouped list lists Standup above the whole-day Conference:\n%s", page)
	}

	// The day view marks the same way, a middle day by its pass-through alone.
	page = c.Page(t, "calendar.day", url.Values{"date": {"2026-09-15"}})
	if n := rows(page, "Retreat"); n != 1 {
		t.Errorf("the day view shows Retreat %d times, want once:\n%s", n, page)
	}
	if !strings.Contains(page, "continues-before") || !strings.Contains(page, "continues-after") {
		t.Errorf("the day view does not mark Retreat as passing through:\n%s", page)
	}
}
