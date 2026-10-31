package web_test

import (
	"net/http"
	"net/url"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestEventDateModesWithoutJavaScript(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Plans", "")
	path := coll + "modes.ics"
	a.Put(t, path, "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:modes\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:20260928T120000Z\r\nDTEND:20260928T130000Z\r\n"+
		"SUMMARY:Modes\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for _, route := range []string{"calendar.event.create", "calendar.event.update"} {
		t.Run(route, func(t *testing.T) {
			var args []any
			if route == "calendar.event.update" {
				args = []any{Here + path}
			}
			for _, allDay := range []bool{false, true} {
				form := url.Values{"summary": {"Modes"}, "calendar": {User + "|" + Here + coll},
					"start": {"invalid inactive time"}, "end": {"invalid inactive time"},
					"start_date": {"invalid inactive date"}, "end_date": {"invalid inactive date"},
					"start-calendar": {"gregorian"}, "end-calendar": {"gregorian"},
					"start_date-calendar": {"gregorian"}, "end_date-calendar": {"gregorian"}}
				if allDay {
					form.Set("allday", "1")
					form.Set("start_date", "2026-09-28")
					form.Set("end_date", "2026-09-29")
				} else {
					form.Set("start", "2026-09-28T12:00")
					form.Set("end", "2026-09-28T13:00")
				}
				if resp := c.Post(t, route, form, args...); resp.StatusCode != http.StatusFound {
					t.Fatalf("all-day=%t submission with inactive invalid inputs answered %s", allDay, resp.Status)
				}
			}
		})
	}
}
