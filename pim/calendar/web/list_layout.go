package web

import (
	"cmp"
	"slices"
	"time"

	"github.com/emersion/go-ical"

	"git.mehdix.org/alborz/pim/calendar"
)

// ListEvent is one row of the calendar's list views: an occurrence marked
// with the days it covers and how it continues past the day shown, so a
// multi-day span can keep a row on each day without posing as new events.
type ListEvent struct {
	calendar.Occurrence
	// First and Last bound the days the occurrence occupies.
	First, Last time.Time
	// ContinuesBefore and ContinuesAfter mark the days a span only passes
	// through on its way from its first day to its last.
	ContinuesBefore, ContinuesAfter bool
}

// MultiDay reports whether the occurrence covers more than one day.
func (e ListEvent) MultiDay() bool { return e.Last.After(e.First) }

// newListEvent annotates an occurrence against the day it is shown on; a
// zero day skips the continuation marks, as the flat list needs.
func newListEvent(oc calendar.Occurrence, loc *time.Location, day time.Time) ListEvent {
	first, last := occurrenceDays(oc, loc)
	e := ListEvent{Occurrence: oc, First: first, Last: last}
	if !day.IsZero() {
		e.ContinuesBefore = first.Before(day)
		e.ContinuesAfter = last.After(day)
	}
	return e
}

// listEventOrder matches the month grid's mind: all-day and multi-day
// spans above timed events, continuing spans above fresh ones, soonest first.
func listEventOrder(a, b ListEvent) int {
	whole := func(e ListEvent) bool { return e.AllDay() || e.MultiDay() }
	if c := a.First.Compare(b.First); c != 0 {
		return c
	}
	if wa, wb := whole(a), whole(b); wa != wb {
		if wa {
			return -1
		}
		return 1
	}
	if c := a.Start.Compare(b.Start); c != 0 {
		return c
	}
	return cmp.Or(
		cmp.Compare(a.Path, b.Path),
		cmp.Compare(a.Account, b.Account),
	)
}

// dayListEvents rows the occurrences one day holds, each marked with how
// the multi-day spans around it continue.
func dayListEvents(ocs []calendar.Occurrence, day time.Time, loc *time.Location) []ListEvent {
	rows := make([]ListEvent, len(ocs))
	for i, oc := range ocs {
		rows[i] = newListEvent(oc, loc, day)
	}
	slices.SortStableFunc(rows, listEventOrder)
	return rows
}

// flatListEvents dedups the occurrences a stretch of days shares, by the
// same key the month grid groups them by, into one row per span.
func flatListEvents(dates []time.Time, on func(time.Time) []calendar.Occurrence, loc *time.Location) []ListEvent {
	var rows []ListEvent
	spans := make(map[monthOccurrenceKey]bool)
	for _, date := range dates {
		for _, oc := range on(date) {
			key := monthOccurrenceKey{account: oc.Account, path: oc.Path, uid: oc.UID(), start: oc.Start.UTC()}
			// A moved instance may share another instance's actual start.
			if prop := oc.Event.Props.Get(ical.PropRecurrenceID); prop != nil {
				key.recurrenceID = prop.Value
			}
			if spans[key] {
				continue
			}
			spans[key] = true
			rows = append(rows, newListEvent(oc, loc, time.Time{}))
		}
	}
	slices.SortStableFunc(rows, listEventOrder)
	return rows
}
