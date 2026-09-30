package web_test

import (
	"fmt"
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
	a := Alborz(t)
	c := a.SignIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	says := func(want map[string]string) {
		t.Helper()
		for lang, text := range want {
			if shown := c.Page(t, "core.status", "calendar", url.Values{"lang": {lang}}); !strings.Contains(shown, `title="`+text+`"`) {
				t.Errorf("in %s the segment does not say %q:\n%s", lang, text, shown)
			}
		}
	}
	says(map[string]string{"en": "No events today", "es": "Sin eventos hoy", "fa": "امروز رویدادی نیست"})

	event := func(uid, summary, start, end string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
			"\r\nDTSTAMP:20260901T080000Z\r\n" + start + "\r\n" + end + "\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	clock := fmt.Sprintf("%02d:%02d", next.Hour(), next.Minute())
	faClock := strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹").Replace(clock)
	a.Put(t, coll+"next.ics", "text/calendar", event("next", "Standup",
		"DTSTART:"+next.Format("20060102T150405Z"), "DTEND:"+next.Add(time.Minute).Format("20060102T150405Z")))
	a.Put(t, coll+"tomorrow.ics", "text/calendar", event("tomorrow", "Later",
		"DTSTART:"+now.AddDate(0, 0, 1).Format("20060102T150405Z"), "DTEND:"+now.AddDate(0, 0, 1).Add(time.Hour).Format("20060102T150405Z")))
	says(map[string]string{"en": "Today · " + clock + " · Standup", "es": "Hoy · " + clock + " · Standup", "fa": "امروز · " + faClock + " · Standup"})

	day := now.Format("20060102")
	a.Put(t, coll+"allday.ics", "text/calendar", event("allday", "Holiday",
		"DTSTART;VALUE=DATE:"+day, "DTEND;VALUE=DATE:"+now.AddDate(0, 0, 1).Format("20060102")))
	says(map[string]string{
		"en": "2 events today · Next at " + clock + " · Standup",
		"es": "2 eventos hoy · Siguiente a las " + clock + " · Standup",
		"fa": "۲ رویداد امروز · بعدی ساعت " + faClock + " · Standup",
	})

	a.Put(t, coll+"late.ics", "text/calendar", event("late", "Review",
		"DTSTART:"+next.Add(time.Minute).Format("20060102T150405Z"), "DTEND:"+next.Add(2*time.Minute).Format("20060102T150405Z")))
	says(map[string]string{"en": "3 events today · Next at " + clock + " · Standup"})

	a.Put(t, coll+"ticks.ics", "text/calendar", event("ticks", "Tick",
		"DTSTART:"+next.Add(2*time.Minute).Format("20060102T150405Z")+"\r\nRRULE:FREQ=SECONDLY;COUNT=1231",
		"DTEND:"+next.Add(2*time.Minute+time.Second).Format("20060102T150405Z")))
	says(map[string]string{
		"en": "1,234 events today · Next at " + clock + " · Standup",
		"es": "1.234 eventos hoy · Siguiente a las " + clock + " · Standup",
		"fa": "۱٬۲۳۴ رویداد امروز · بعدی ساعت " + faClock + " · Standup",
	})
}
