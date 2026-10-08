package web_test

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestTheCalendarSegmentNamesOneEventAndCountsMore(t *testing.T) {
	now := time.Now().UTC()
	next := now.Truncate(time.Minute).Add(2 * time.Minute)
	if next.Add(time.Hour).Day() != now.Day() {
		t.Skip("too close to midnight for an event later today")
	}
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	says := func(want map[string]string) {
		t.Helper()
		for lang, text := range want {
			if shown := c.Page(t, "core.status", "calendar", url.Values{"lang": {lang}}); !strings.Contains(html.UnescapeString(shown), `title="`+text+`"`) {
				t.Errorf("in %s the segment does not say %q:\n%s", lang, text, shown)
			}
		}
	}
	says(map[string]string{"en": TIn(t, "en", "calendar.status.none"), "es": TIn(t, "es", "calendar.status.none"), "fa": TIn(t, "fa", "calendar.status.none")})

	event := func(uid, summary, start, end string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
			"\r\nDTSTAMP:20260901T080000Z\r\n" + start + "\r\n" + end + "\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	clock := fmt.Sprintf("%02d:%02d", next.Hour(), next.Minute())
	faClock := strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹").Replace(clock)
	// today is the segment for n events with Standup next at, its count written as number.
	today := func(lang string, n int, at, number string) string {
		t.Helper()
		text := TIn(t, lang, "calendar.status.today", n) + " · " + TIn(t, lang, "calendar.status.next", n, at) + " · Standup"
		if !strings.Contains(text, number) {
			t.Errorf("in %s %q does not write the count as %s", lang, text, number)
		}
		return text
	}
	a.Put(t, coll+"next.ics", "text/calendar", event("next", "Standup",
		"DTSTART:"+next.Format("20060102T150405Z"), "DTEND:"+next.Add(time.Minute).Format("20060102T150405Z")))
	a.Put(t, coll+"tomorrow.ics", "text/calendar", event("tomorrow", "Later",
		"DTSTART:"+now.AddDate(0, 0, 1).Format("20060102T150405Z"), "DTEND:"+now.AddDate(0, 0, 1).Add(time.Hour).Format("20060102T150405Z")))
	says(map[string]string{"en": today("en", 1, clock, ""), "es": today("es", 1, clock, ""), "fa": today("fa", 1, faClock, "")})

	day := now.Format("20060102")
	a.Put(t, coll+"allday.ics", "text/calendar", event("allday", "Holiday",
		"DTSTART;VALUE=DATE:"+day, "DTEND;VALUE=DATE:"+now.AddDate(0, 0, 1).Format("20060102")))
	says(map[string]string{
		"en": today("en", 2, clock, "2"),
		"es": today("es", 2, clock, "2"),
		"fa": today("fa", 2, faClock, "۲"),
	})

	a.Put(t, coll+"late.ics", "text/calendar", event("late", "Review",
		"DTSTART:"+next.Add(time.Minute).Format("20060102T150405Z"), "DTEND:"+next.Add(2*time.Minute).Format("20060102T150405Z")))
	says(map[string]string{"en": today("en", 3, clock, "3")})

	a.Put(t, coll+"ticks.ics", "text/calendar", event("ticks", "Tick",
		"DTSTART:"+next.Add(2*time.Minute).Format("20060102T150405Z")+"\r\nRRULE:FREQ=SECONDLY;COUNT=1231",
		"DTEND:"+next.Add(2*time.Minute+time.Second).Format("20060102T150405Z")))
	says(map[string]string{
		"en": today("en", 1234, clock, "1,234"),
		"es": today("es", 1234, clock, "1.234"),
		"fa": today("fa", 1234, faClock, "۱٬۲۳۴"),
	})
}
