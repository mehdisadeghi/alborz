package web

import (
	"cmp"
	"slices"
	"time"

	"github.com/emersion/go-ical"

	"git.mehdix.org/alborz/pim/calendar"
)

// monthLanes is how many bars a week holds before its days count the rest.
const monthLanes = 5

// MonthWeek's Overflow counts what its lanes cannot hold; Squeezed counts,
// for a window that fits fewer lanes, what a day hides at each Fit.
type MonthWeek struct {
	Dates    []time.Time
	Events   []MonthEvent
	Overflow []MonthOverflow
	Squeezed []MonthOverflow
}

// Counts is every "more" link the week may show, whatever the window fits.
func (w MonthWeek) Counts() []MonthOverflow {
	return append(slices.Clip(w.Overflow), w.Squeezed...)
}

// Fit is the fewest lines the week's lane area needs for the bar to show.
type MonthEvent struct {
	calendar.Occurrence
	Column, Span, Row, Fit          int
	ContinuesBefore, ContinuesAfter bool
	// Timed is an event within one day, shown as a time and title, not a bar.
	Timed bool
	// Day is the date the bar starts on in this week.
	Day time.Time
}

// Fit is the number of lines the lane area holds when the link shows, and
// the line it sits on; monthLanes+1 means that many or more.
type MonthOverflow struct {
	Date               time.Time
	Column, Count, Fit int
}

type monthOccurrenceKey struct {
	account, path, uid, recurrenceID string
	start                            time.Time
}

// monthWeeks lays out each week as Apple Calendar does. All-day and
// multi-day events are bars, placed first and longest first; a bar takes the
// lowest lane free across its days, or, on a crowded week, the lowest free
// lane on each day. Timed events of one day then fill each day's lowest free
// lanes in start order, so no lane is left empty above an item. What still
// does not fit counts on its day.
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
	tie := func(a, b *span) int {
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
	}
	var weeks []MonthWeek
	previous := make(map[monthOccurrenceKey]int)
	for start := 0; start < len(dates); start += 7 {
		end := min(start+7, len(dates))
		week := MonthWeek{Dates: dates[start:end]}
		var bars, items []*span
		for _, s := range spans {
			if s.first >= end || s.last < start {
				continue
			}
			firstDay, lastDay := occurrenceDays(s.occurrence, dates[0].Location())
			if s.occurrence.AllDay() || lastDay.After(firstDay) {
				bars = append(bars, s)
			} else {
				items = append(items, s)
			}
		}
		slices.SortFunc(bars, func(a, b *span) int {
			// Continuing bars come first, in the order they had last week.
			pa, pb := previous[a.key], previous[b.key]
			if (pa > 0) != (pb > 0) {
				if pa > 0 {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(pa, pb); c != 0 {
				return c
			}
			// Longest first, so a trip is not hidden behind a crowded day.
			la, lb := min(end-1, a.last)-max(start, a.first), min(end-1, b.last)-max(start, b.first)
			if c := cmp.Compare(lb, la); c != 0 {
				return c
			}
			if c := cmp.Compare(a.first, b.first); c != 0 {
				return c
			}
			return tie(a, b)
		})
		slices.SortFunc(items, tie)
		var occupied [monthLanes][7]bool
		var hidden [7]int
		lowest := func(first, last int) int {
			for r := range occupied {
				free := true
				for col := first; col <= last && free; col++ {
					free = !occupied[r][col]
				}
				if free {
					return r
				}
			}
			return -1
		}
		place := func(s *span, first, last, row int, timed bool) {
			for col := first; col <= last; col++ {
				occupied[row][col] = true
			}
			firstDay, lastDay := occurrenceDays(s.occurrence, dates[start].Location())
			week.Events = append(week.Events, MonthEvent{
				Occurrence: s.occurrence, Column: first + 1, Span: last - first + 1, Row: row + 1, Timed: timed, Day: dates[start+first],
				ContinuesBefore: firstDay.Before(dates[start+first]),
				ContinuesAfter:  lastDay.After(dates[start+last]),
			})
		}
		next := make(map[monthOccurrenceKey]int)
		for _, s := range bars {
			first, last := max(start, s.first)-start, min(end-1, s.last)-start
			if row := lowest(first, last); row >= 0 {
				place(s, first, last, row, false)
				if s.last >= end {
					next[s.key] = row + 1
				}
				continue
			}
			// No lane is free all week: show each run of days that has one.
			for col := first; col <= last; {
				row := lowest(col, col)
				if row < 0 {
					hidden[col]++
					col++
					continue
				}
				run := col
				for run < last && lowest(run+1, run+1) == row {
					run++
				}
				place(s, col, run, row, false)
				if run == end-1-start && s.last >= end {
					next[s.key] = row + 1
				}
				col = run + 1
			}
		}
		for _, s := range items {
			col := s.first - start
			if row := lowest(col, col); row >= 0 {
				place(s, col, col, row, true)
			} else {
				hidden[col]++
			}
		}
		// Day by day, top to bottom: the order a screen reader reads them in.
		slices.SortStableFunc(week.Events, func(a, b MonthEvent) int {
			if c := cmp.Compare(a.Column, b.Column); c != 0 {
				return c
			}
			return cmp.Compare(a.Row, b.Row)
		})
		for col, count := range hidden {
			if count > 0 {
				week.Overflow = append(week.Overflow, MonthOverflow{dates[start+col], col + 1, count, monthLanes + 1})
			}
		}
		squeeze(&week, occupied, hidden)
		weeks = append(weeks, week)
		previous = next
	}
	return weeks
}

// squeeze plans the week for a window that fits fewer than monthLanes+1
// lines, as Apple Calendar does: with n lines the week shows n lanes when
// nothing beyond them is hidden, else n-1 lanes and a count on line n.
// The stylesheet picks the plan with a container query on the lane area.
func squeeze(week *MonthWeek, occupied [monthLanes][7]bool, hidden [7]int) {
	beyond := func(lanes int) bool {
		for col := range hidden {
			if hidden[col] > 0 {
				return true
			}
			for row := lanes; row < monthLanes; row++ {
				if occupied[row][col] {
					return true
				}
			}
		}
		return false
	}
	// shown[n] is how many lanes n lines show.
	var shown [monthLanes + 2]int
	shown[monthLanes+1] = monthLanes
	for n := 1; n <= monthLanes; n++ {
		shown[n] = n
		if beyond(n) {
			shown[n] = n - 1
			for col := range hidden {
				count := hidden[col]
				for row := n - 1; row < monthLanes; row++ {
					if occupied[row][col] {
						count++
					}
				}
				if count > 0 {
					week.Squeezed = append(week.Squeezed, MonthOverflow{week.Dates[col], col + 1, count, n})
				}
			}
		}
	}
	for i := range week.Events {
		e := &week.Events[i]
		e.Fit = e.Row
		if shown[e.Row] < e.Row {
			e.Fit = e.Row + 1
		}
	}
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
