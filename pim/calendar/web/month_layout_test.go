package web

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"git.mehdix.org/alborz/pim/calendar"
)

func monthLayoutDates(n int) []time.Time {
	dates := make([]time.Time, n)
	for i := range dates {
		dates[i] = time.Date(2026, 11, 2+i, 0, 0, 0, 0, time.UTC)
	}
	return dates
}

func monthLayoutEvent(account, path string, start, end time.Time) calendar.Occurrence {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, path)
	event.Props.SetText(ical.PropSummary, path)
	event.Props.SetDateTime(ical.PropDateTimeStart, start)
	return calendar.Occurrence{
		Object: calendar.Object{Account: account, CalendarObject: &caldav.CalendarObject{Path: path}},
		Event:  event, Start: start, End: end,
	}
}

func TestMonthWeeksSpanAndContinueAcrossWeeks(t *testing.T) {
	dates := monthLayoutDates(14)
	event := monthLayoutEvent("a", "/trip.ics", dates[4], dates[9])
	on := make(map[time.Time][]calendar.Occurrence)
	for _, date := range dates[4:9] {
		on[date] = []calendar.Occurrence{event}
	}
	weeks := monthWeeks(dates, on)
	if len(weeks) != 2 || len(weeks[0].Events) != 1 || len(weeks[1].Events) != 1 {
		t.Fatalf("trip should have two week segments: %#v", weeks)
	}
	first, second := weeks[0].Events[0], weeks[1].Events[0]
	if first.Column != 5 || first.Span != 3 || first.ContinuesBefore || !first.ContinuesAfter {
		t.Errorf("first segment: %#v", first)
	}
	if second.Column != 1 || second.Span != 2 || !second.ContinuesBefore || second.ContinuesAfter {
		t.Errorf("second segment: %#v", second)
	}
	if first.Row != second.Row {
		t.Errorf("continuation changed lane from %d to %d", first.Row, second.Row)
	}
}

func TestMonthWeeksKeepLongEventsVisibleAndCountHiddenDays(t *testing.T) {
	dates := monthLayoutDates(7)
	on := make(map[time.Time][]calendar.Occurrence)
	trip := monthLayoutEvent("a", "/trip.ics", dates[0], dates[6])
	for _, date := range dates[:6] {
		on[date] = []calendar.Occurrence{trip}
	}
	for _, path := range []string{"/a.ics", "/b.ics", "/c.ics", "/d.ics"} {
		on[dates[2]] = append(on[dates[2]], monthLayoutEvent("a", path, dates[2], dates[3]))
	}
	week := monthWeeks(dates, on)[0]
	if len(week.Events) != 3 || week.Events[0].Path != "/trip.ics" || week.Events[0].Span != 6 {
		t.Fatalf("long trip must stay visible above the crowded day: %#v", week.Events)
	}
	if len(week.Overflow) != 1 || week.Overflow[0].Column != 3 || week.Overflow[0].Count != 2 || !week.Overflow[0].Date.Equal(dates[2]) {
		t.Errorf("want two hidden events on Wednesday: %#v", week.Overflow)
	}
}

func TestMonthWeeksReuseLanesWithoutOverlaps(t *testing.T) {
	dates := monthLayoutDates(7)
	on := make(map[time.Time][]calendar.Occurrence)
	for _, bounds := range [][2]int{{0, 3}, {1, 4}, {4, 7}} {
		event := monthLayoutEvent("a", dates[bounds[0]].String(), dates[bounds[0]], dates[0].AddDate(0, 0, bounds[1]))
		for _, date := range dates[bounds[0]:bounds[1]] {
			on[date] = append(on[date], event)
		}
	}
	week := monthWeeks(dates, on)[0]
	if len(week.Events) != 3 || len(week.Overflow) != 0 {
		t.Fatalf("all three events should fit: %#v", week)
	}
	if week.Events[0].Row == week.Events[1].Row || week.Events[0].Row != week.Events[2].Row {
		t.Errorf("overlaps need different lanes; later events reuse them: %#v", week.Events)
	}
}

func TestMonthWeeksDoNotMergeAccountsOrRecurringInstances(t *testing.T) {
	dates := monthLayoutDates(7)
	first := monthLayoutEvent("a", "/same.ics", dates[0], dates[1])
	otherAccount := monthLayoutEvent("b", "/same.ics", dates[0], dates[1])
	later := monthLayoutEvent("a", "/same.ics", dates[2], dates[3])
	on := map[time.Time][]calendar.Occurrence{dates[0]: {otherAccount, first}, dates[2]: {later}}
	week := monthWeeks(dates, on)[0]
	if len(week.Events) != 3 {
		t.Fatalf("distinct account and recurrence occurrences were merged: %#v", week.Events)
	}
	if week.Events[0].Account != "a" || week.Events[1].Account != "b" || week.Events[2].Column != 3 {
		t.Errorf("equal starts should have stable account order: %#v", week.Events)
	}
}

func TestMonthWeeksMarkContinuationOutsideGrid(t *testing.T) {
	dates := monthLayoutDates(7)
	event := monthLayoutEvent("a", "/long.ics", dates[0].AddDate(0, 0, -3), dates[6].AddDate(0, 0, 3))
	on := make(map[time.Time][]calendar.Occurrence)
	for _, date := range dates {
		on[date] = []calendar.Occurrence{event}
	}
	segment := monthWeeks(dates, on)[0].Events[0]
	if segment.Column != 1 || segment.Span != 7 || !segment.ContinuesBefore || !segment.ContinuesAfter {
		t.Errorf("grid-clipped event should continue at both ends: %#v", segment)
	}
}

func TestMonthWeeksDoNotOverflowWhenThreeLanesFit(t *testing.T) {
	dates := monthLayoutDates(7)
	on := make(map[time.Time][]calendar.Occurrence)
	for _, bounds := range [][2]int{{0, 0}, {0, 1}, {0, 3}, {1, 2}, {2, 3}, {3, 5}} {
		event := monthLayoutEvent("a", dates[bounds[0]].String()+dates[bounds[1]].String(), dates[bounds[0]], dates[0].AddDate(0, 0, bounds[1]+1))
		for _, date := range dates[bounds[0] : bounds[1]+1] {
			on[date] = append(on[date], event)
		}
	}
	week := monthWeeks(dates, on)[0]
	if len(week.Events) != 6 || len(week.Overflow) != 0 {
		t.Fatalf("six events with at most three per day should fit: %#v", week)
	}
}

func TestMonthWeeksKeepCoincidentRecurrenceInstances(t *testing.T) {
	dates := monthLayoutDates(7)
	data, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:daily
DTSTART:20261102T090000Z
DTEND:20261102T100000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Ordinary
END:VEVENT
BEGIN:VEVENT
UID:daily
RECURRENCE-ID:20261103T090000Z
DTSTART:20261104T090000Z
DTEND:20261104T100000Z
SUMMARY:Moved
END:VEVENT
END:VCALENDAR
`, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	obj := calendar.Object{Account: "a", CalendarObject: &caldav.CalendarObject{Path: "/daily.ics", Data: data}}
	occurrences, err := calendar.Occurrences(obj, time.UTC, dates[0], dates[6].AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	on := make(map[time.Time][]calendar.Occurrence)
	for _, oc := range occurrences {
		first, last := occurrenceDays(oc, time.UTC)
		for date := first; !date.After(last); date = date.AddDate(0, 0, 1) {
			on[date] = append(on[date], oc)
		}
	}
	for _, reverse := range []bool{false, true} {
		if reverse {
			for date, events := range on {
				for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
					events[i], events[j] = events[j], events[i]
				}
				on[date] = events
			}
		}
		week := monthWeeks(dates, on)[0]
		if len(week.Events) != 3 || len(week.Overflow) != 0 {
			t.Fatalf("coincident instances must stay distinct: %#v", week)
		}
		if week.Events[1].Summary() != "Ordinary" || week.Events[2].Summary() != "Moved" || week.Events[1].Row == week.Events[2].Row {
			t.Errorf("coincident instances need stable, separate lanes: %#v", week.Events)
		}
	}
}

func TestOccurrenceDays(t *testing.T) {
	for _, tt := range []struct {
		name, zone, start, end, first, last string
	}{
		{"all-day civil date", "America/Los_Angeles", ";VALUE=DATE:20261104", ";VALUE=DATE:20261106", "2026-11-04", "2026-11-05"},
		{"exclusive midnight", "Europe/Berlin", ":20261104T220000Z", ":20261104T230000Z", "2026-11-04", "2026-11-04"},
		{"overnight user zone", "America/Los_Angeles", ":20261104T073000Z", ":20261104T083000Z", "2026-11-03", "2026-11-04"},
		{"DST spring forward", "Europe/Berlin", ":20260328T223000Z", ":20260329T220000Z", "2026-03-28", "2026-03-29"},
		{"DST fall back", "Europe/Berlin", ":20261024T213000Z", ":20261025T230000Z", "2026-10-24", "2026-10-25"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tt.zone)
			if err != nil {
				t.Fatal(err)
			}
			data, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VEVENT\r\nUID:days\r\nDTSTART" + tt.start + "\r\nDTEND" + tt.end + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")).Decode()
			if err != nil {
				t.Fatal(err)
			}
			event := data.Events()[0]
			start, err := event.DateTimeStart(time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			end, err := event.DateTimeEnd(time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			first, last := occurrenceDays(calendar.Occurrence{Event: &event, Start: start, End: end}, loc)
			if first.Format("2006-01-02") != tt.first || last.Format("2006-01-02") != tt.last {
				t.Errorf("covered days = %s through %s, want %s through %s", first, last, tt.first, tt.last)
			}
			if first.Location() != loc || last.Location() != loc || first.Hour() != 0 || last.Hour() != 0 {
				t.Errorf("covered days must be midnight in the user's zone: %s through %s", first, last)
			}
		})
	}
}
