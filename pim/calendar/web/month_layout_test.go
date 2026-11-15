package web

import (
	"fmt"
	"slices"
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
		Account: account, CalendarObject: &caldav.CalendarObject{Path: path},
		Event: event, Start: start, End: end,
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
}

// A squeezed week plans each day on its own: a day with one event shows it
// even when another day of the same week needs a count.
func TestMonthWeeksSqueezeEachDayOnItsOwn(t *testing.T) {
	dates := monthLayoutDates(7)
	on := make(map[time.Time][]calendar.Occurrence)
	single := monthLayoutEvent("a", "/single.ics", dates[0].Add(9*time.Hour), dates[0].Add(10*time.Hour))
	on[dates[0]] = []calendar.Occurrence{single}
	for i := range 2 {
		start := dates[1].Add(time.Duration(9+i) * time.Hour)
		event := monthLayoutEvent("a", fmt.Sprintf("/crowded%d.ics", i), start, start.Add(30*time.Minute))
		on[dates[1]] = append(on[dates[1]], event)
	}
	week := monthWeeks(dates, on)[0]

	for _, e := range week.Events {
		if e.Path == "/single.ics" && e.Fit != 1 {
			t.Errorf("a lone event needs %d lines, want 1", e.Fit)
		}
	}
	counts := make(map[int]int)
	for _, o := range week.Counts() {
		if o.Fit == 1 {
			counts[o.Column] = o.Count
		}
	}
	if count, ok := counts[1]; ok {
		t.Errorf("a day with one event counts %d more", count)
	}
	if counts[2] != 2 {
		t.Errorf("the crowded day counts %d more on its one line, want 2", counts[2])
	}
}

// Whatever the lane count, a week shows each event only on its own days,
// never stacks two in one place, and counts on each day what it hides.
func TestMonthWeeksAccountForEveryEvent(t *testing.T) {
	dates := monthLayoutDates(7)
	on := make(map[time.Time][]calendar.Occurrence)
	add := func(name string, first, days int) {
		event := monthLayoutEvent("a", name, dates[first], dates[first].AddDate(0, 0, days))
		for _, date := range dates[first : first+days] {
			on[date] = append(on[date], event)
		}
	}
	add("/trip.ics", 0, 6)
	add("/workshop.ics", 1, 3)
	add("/review.ics", 2, 2)
	for i := range 2 * monthLanes {
		start := dates[2].Add(time.Duration(9+i) * time.Hour)
		event := monthLayoutEvent("a", fmt.Sprintf("/meeting%d.ics", i), start, start.Add(30*time.Minute))
		on[dates[2]] = append(on[dates[2]], event)
	}
	week := monthWeeks(dates, on)[0]

	shown := make([]map[string]bool, 7)
	var taken [monthLanes + 1][7]bool
	for _, e := range week.Events {
		if e.Row < 1 || e.Row > monthLanes {
			t.Fatalf("%s sits on lane %d", e.Path, e.Row)
		}
		for col := e.Column - 1; col < e.Column-1+e.Span; col++ {
			if !slices.ContainsFunc(on[dates[col]], func(oc calendar.Occurrence) bool { return oc.Path == e.Path }) {
				t.Errorf("%s shows on %s, a day it does not cover", e.Path, dates[col].Format(time.DateOnly))
			}
			if taken[e.Row][col] {
				t.Errorf("two events share lane %d on %s", e.Row, dates[col].Format(time.DateOnly))
			}
			taken[e.Row][col] = true
			if shown[col] == nil {
				shown[col] = map[string]bool{}
			}
			shown[col][e.Path] = true
		}
	}
	hidden := make([]int, 7)
	for _, o := range week.Overflow {
		hidden[o.Column-1] = o.Count
	}
	for col, date := range dates {
		if got := len(shown[col]) + hidden[col]; got != len(on[date]) {
			t.Errorf("%s shows %d and counts %d hidden of %d events", date.Format(time.DateOnly), len(shown[col]), hidden[col], len(on[date]))
		}
	}
	if !shown[2]["/trip.ics"] {
		t.Error("the long trip is hidden behind the crowded day")
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
		lanes := map[[2]int]string{}
		for _, e := range week.Events {
			at := [2]int{e.Column, e.Row}
			if other, ok := lanes[at]; ok {
				t.Errorf("%s and %s share a lane on day %d", other, e.Summary(), e.Column)
			}
			lanes[at] = e.Summary()
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

// On a window that fits one line, a day holding a single event shows the
// event itself rather than a count taking the same slot.
func TestSqueezeShowsLoneEventOnOneLine(t *testing.T) {
	var occupied [monthLanes][7]bool
	occupied[0][0] = true
	occupied[0][1] = true
	occupied[1][1] = true
	dates := monthLayoutDates(7)
	week := &MonthWeek{Dates: dates, Events: []MonthEvent{
		{Column: 1, Row: 1}, {Column: 2, Row: 1}, {Column: 2, Row: 2},
	}}
	squeeze(week, occupied, [7]int{})

	if week.Events[0].Fit != 1 {
		t.Errorf("a lone event keeps its lane on one line, got Fit %d", week.Events[0].Fit)
	}
	if week.Events[1].Fit != 2 || week.Events[2].Fit != 2 {
		t.Errorf("both events of a stacked day move below the count: %#v", week.Events[1:])
	}
	if week.Events[2].Fit != 2 {
		t.Errorf("the second event of a stacked day moves below the count, got Fit %d", week.Events[2].Fit)
	}
	if len(week.Squeezed) != 1 || week.Squeezed[0].Column != 2 || week.Squeezed[0].Fit != 1 {
		t.Errorf("only the stacked day counts on one line: %#v", week.Squeezed)
	}
	if len(week.Overflow) != 0 {
		t.Errorf("a single line hides nothing beyond itself: %#v", week.Overflow)
	}
}
