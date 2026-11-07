package web

import (
	"cmp"
	"slices"
	"time"

	"github.com/emersion/go-ical"

	"git.mehdix.org/alborz/pim/calendar"
)

type MonthWeek struct {
	Dates    []time.Time
	Events   []MonthEvent
	Overflow []MonthOverflow
}

type MonthEvent struct {
	calendar.Occurrence
	Column, Span, Row               int
	ContinuesBefore, ContinuesAfter bool
}

type MonthOverflow struct {
	Date          time.Time
	Column, Count int
}

type monthOccurrenceKey struct {
	account, path, uid, recurrenceID string
	start                            time.Time
}

// monthWeeks assigns one lane to every visible segment, so a bar never
// changes height partway through a week. Hidden segments count on each day.
func monthWeeks(dates []time.Time, on map[time.Time][]calendar.Occurrence) []MonthWeek {
	type span struct {
		key         monthOccurrenceKey
		occurrence  calendar.Occurrence
		first, last int
	}
	spans := make(map[monthOccurrenceKey]*span)
	for i, date := range dates {
		for _, oc := range on[date] {
			key := monthOccurrenceKey{account: oc.Account, path: oc.Path, uid: oc.UID(), start: oc.Start.UTC()}
			// A moved instance may share another instance's actual start.
			if prop := oc.Event.Props.Get(ical.PropRecurrenceID); prop != nil {
				key.recurrenceID = prop.Value
			}
			if s := spans[key]; s != nil {
				s.last = i
			} else {
				spans[key] = &span{key, oc, i, i}
			}
		}
	}
	var weeks []MonthWeek
	previous := make(map[monthOccurrenceKey]int)
	for start := 0; start < len(dates); start += 7 {
		end := min(start+7, len(dates))
		week := MonthWeek{Dates: dates[start:end]}
		var candidates []*span
		for _, s := range spans {
			if s.first < end && s.last >= start {
				candidates = append(candidates, s)
			}
		}
		slices.SortFunc(candidates, func(a, b *span) int {
			// Keep continuing bars in their previous lanes before placing new ones.
			if c := cmp.Compare(previous[b.key], previous[a.key]); c != 0 {
				return c
			}
			// Start order lets every non-overlapping interval reuse a lane;
			// length alone can fragment all three lanes unnecessarily.
			if c := cmp.Compare(a.first, b.first); c != 0 {
				return c
			}
			if c := cmp.Compare(b.last-b.first, a.last-a.first); c != 0 {
				return c
			}
			if c := a.occurrence.Start.Compare(b.occurrence.Start); c != 0 {
				return c
			}
			if c := cmp.Compare(a.key.account, b.key.account); c != 0 {
				return c
			}
			if c := cmp.Compare(a.key.path, b.key.path); c != 0 {
				return c
			}
			if c := cmp.Compare(a.key.uid, b.key.uid); c != 0 {
				return c
			}
			return cmp.Compare(a.key.recurrenceID, b.key.recurrenceID)
		})
		var occupied [3][7]bool
		var hidden [7]int
		next := make(map[monthOccurrenceKey]int)
		for _, s := range candidates {
			first, last := max(start, s.first)-start, min(end-1, s.last)-start
			free := func(row int) bool {
				for col := first; col <= last; col++ {
					if occupied[row][col] {
						return false
					}
				}
				return true
			}
			row := -1
			if preferred := previous[s.key]; preferred > 0 && free(preferred-1) {
				row = preferred - 1
			} else {
				for r := range occupied {
					if free(r) {
						row = r
						break
					}
				}
			}
			if row < 0 {
				for col := first; col <= last; col++ {
					hidden[col]++
				}
				continue
			}
			for col := first; col <= last; col++ {
				occupied[row][col] = true
			}
			firstDay, lastDay := occurrenceDays(s.occurrence, dates[start].Location())
			week.Events = append(week.Events, MonthEvent{
				Occurrence: s.occurrence, Column: first + 1, Span: last - first + 1, Row: row + 1,
				ContinuesBefore: firstDay.Before(dates[start+first]),
				ContinuesAfter:  lastDay.After(dates[start+last]),
			})
			if s.last >= end {
				next[s.key] = row + 1
			}
		}
		for col, count := range hidden {
			if count > 0 {
				week.Overflow = append(week.Overflow, MonthOverflow{dates[start+col], col + 1, count})
			}
		}
		weeks = append(weeks, week)
		previous = next
	}
	return weeks
}

// DTEND is exclusive. A DATE names a civil day, rather than an instant
// to convert to the reader's zone.
func occurrenceDays(oc calendar.Occurrence, loc *time.Location) (first, last time.Time) {
	day := func(t time.Time) time.Time {
		if !oc.AllDay() {
			t = t.In(loc)
		}
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	first = day(oc.Start)
	last = first
	if oc.AllDay() {
		last = day(oc.End).AddDate(0, 0, -1)
	} else if oc.End.After(oc.Start) {
		last = day(oc.End.Add(-time.Nanosecond))
	}
	if last.Before(first) {
		last = first
	}
	return first, last
}
