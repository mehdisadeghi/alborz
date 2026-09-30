package web_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestTheCalendarSegmentNamesOneEventAndCountsMore(t *testing.T) {
	now := time.Now().UTC()
	next := now.Truncate(time.Minute).Add(2 * time.Minute)
	if next.Day() != now.Day() {
		t.Skip("too close to midnight for an event later today")
	}
	a := Alborz(t)
	c := a.SignIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	says := func(want string) {
		t.Helper()
		if shown := c.Page(t, "core.status", "calendar"); !strings.Contains(shown, `title="`+want+`"`) {
			t.Errorf("the segment does not say %q:\n%s", want, shown)
		}
	}
	says("No events today")

	event := func(uid, summary, start, end string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
			"\r\nDTSTAMP:20260901T080000Z\r\n" + start + "\r\n" + end + "\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	clock := fmt.Sprintf("%02d:%02d", next.Hour(), next.Minute())
	a.Put(t, coll+"next.ics", "text/calendar", event("next", "Standup",
		"DTSTART:"+next.Format("20060102T150405Z"), "DTEND:"+next.Add(time.Minute).Format("20060102T150405Z")))
	a.Put(t, coll+"tomorrow.ics", "text/calendar", event("tomorrow", "Later",
		"DTSTART:"+now.AddDate(0, 0, 1).Format("20060102T150405Z"), "DTEND:"+now.AddDate(0, 0, 1).Add(time.Hour).Format("20060102T150405Z")))
	says("Today · " + clock + " · Standup")

	day := now.Format("20060102")
	a.Put(t, coll+"allday.ics", "text/calendar", event("allday", "Holiday",
		"DTSTART;VALUE=DATE:"+day, "DTEND;VALUE=DATE:"+now.AddDate(0, 0, 1).Format("20060102")))
	says("2 events today · Next at " + clock + " · Standup")

	a.Put(t, coll+"late.ics", "text/calendar", event("late", "Review",
		"DTSTART:"+next.Add(time.Minute).Format("20060102T150405Z"), "DTEND:"+next.Add(2*time.Minute).Format("20060102T150405Z")))
	says("3 events today · Next at " + clock + " · Standup")
}
