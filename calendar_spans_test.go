package alborz_test

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestMonthEventsSpanDaysAndLinkBackToTheirScope(t *testing.T) {
	a, c := SignedIn(t, User)
	coll := c.CreateCalendar(t, "Trips", "")
	a.Put(t, coll+"trip.ics", "text/calendar", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:trip\r\nDTSTAMP:20260901T080000Z\r\nDTSTART;VALUE=DATE:20260904\r\nDTEND;VALUE=DATE:20260909\r\n"+
		"SUMMARY:Factory visit\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for _, account := range []string{"", User} {
		q := url.Values{"month": {"2026-09"}, "cal": {Here + coll}}
		if account != "" {
			q.Set("account", account)
		}
		page := c.Page(t, "calendar.month", q)
		doc := swipeDocument(t, page)
		var bars []*html.Node
		for _, link := range swipeElements(doc, "title", "Factory visit") {
			if strings.Contains(swipeAttribute(link, "class"), "month-event") {
				bars = append(bars, link)
			}
		}
		if len(bars) != 2 {
			t.Fatalf("account %q: got %d event bars, want two week segments", account, len(bars))
		}
		for i, want := range []string{"--column:5;--span:3;--row:1", "--column:1;--span:2;--row:1"} {
			if !strings.Contains(swipeAttribute(bars[i], "style"), want) {
				t.Errorf("segment %d style = %q, want %q", i, swipeAttribute(bars[i], "style"), want)
			}
			u, err := url.Parse(swipeAttribute(bars[i], "href"))
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("account") != User || u.Query().Get("from") == "" {
				t.Errorf("segment lost its owning account or return path: %s", u)
			}
			back, err := url.Parse(u.Query().Get("from"))
			if err != nil || back.Query().Get("month") != "2026-09" || back.Query().Get("cal") != Here+coll || back.Query().Get("account") != account {
				t.Errorf("event return path lost month scope: %v (%v)", back, err)
			}
		}
		if !strings.Contains(swipeAttribute(bars[0], "class"), "continues-after") || !strings.Contains(swipeAttribute(bars[1], "class"), "continues-before") {
			t.Error("week segments have no continuation edges")
		}
	}
}
