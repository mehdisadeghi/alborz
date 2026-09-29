package calsys

import (
	"fmt"
	"slices"
	"time"

	"github.com/alborzmail/go-recur/rscale"
)

// System is a calendar as a page shows it: rscale counts its days, and it
// adds the name alborz stores, the locale key of its months and the weekday
// its weeks begin on.
type System struct {
	name      string
	cal       rscale.Calendar
	monthKey  string
	weekStart time.Weekday
}

const (
	// Gregorian is the Gregorian calendar, which numbers weeks by ISO 8601.
	Gregorian = "gregorian"
	// shcalName is the Solar Hijri (Jalali) calendar.
	shcalName = "shcal"
)

var (
	gregorian  = System{Gregorian, rscale.Gregorian{}, "calendar.gmonth.%d", time.Monday}
	solarHijri = System{shcalName, rscale.Persian{}, "calendar.shmonth.%d", time.Saturday}
	systems    = []System{gregorian, solarHijri}
)

// Known returns name when it is a known system, else "", so a form cannot store an unknown calendar.
func Known(name string) string {
	if slices.ContainsFunc(systems, func(s System) bool { return s.name == name }) {
		return name
	}
	return ""
}

// Of returns the named system, Gregorian for an unknown name.
func Of(name string) System {
	for _, s := range systems {
		if s.name == name {
			return s
		}
	}
	return gregorian
}

// RSCALEs names the scales of every system, which a rule may count in.
func RSCALEs() []string {
	var names []string
	for _, s := range systems {
		names = append(names, s.RSCALE())
	}
	return names
}

// For is s where it reckons t, else Gregorian: a date s cannot count is shown in one that can, never guessed.
func For(s System, t time.Time) System {
	if _, ok := s.cal.YearOf(rscale.DayOf(t)); !ok {
		return gregorian
	}
	return s
}

func (s System) Name() string { return s.name }

// RSCALE is the calendar's name in a recurrence rule.
func (s System) RSCALE() string { return s.cal.Name() }

// Date reads t in this calendar.
func (s System) Date(t time.Time) (year, month, day int) {
	d, ok := rscale.DateOf(s.cal, rscale.DayOf(t))
	if !ok {
		panic(fmt.Sprintf("%s does not reckon %s", s.name, t.Format(time.DateOnly)))
	}
	return d.Year, d.Month.N, d.Day
}

// Time is midnight on a date of this calendar.
func (s System) Time(year, month, day int, loc *time.Location) time.Time {
	t, err := s.Day(year, month, day, loc)
	if err != nil {
		panic(err)
	}
	return t
}

// Day is Time for a date as typed, refusing one the calendar does not have or cannot count.
func (s System) Day(year, month, day int, loc *time.Location) (time.Time, error) {
	d, ok := rscale.DayAt(s.cal, rscale.Date{Year: year, Month: rscale.Month{N: month}, Day: day})
	if !ok {
		return time.Time{}, fmt.Errorf("no day %d/%d/%d", year, month, day)
	}
	return time.Date(1970, time.January, 1+int(d), 0, 0, 0, 0, loc), nil
}

// MonthStart is midnight on the first day of the month t falls in.
func (s System) MonthStart(t time.Time) time.Time {
	y, m, _ := s.Date(t)
	return s.Time(y, m, 1, t.Location())
}

// AddMonths moves a month start by n months.
func (s System) AddMonths(start time.Time, n int) time.Time {
	y, m, _ := s.Date(start)
	for ; n > 0; n-- {
		if m++; m > len(s.year(y).Months) {
			y, m = y+1, 1
		}
	}
	for ; n < 0; n++ {
		if m--; m < 1 {
			y--
			m = len(s.year(y).Months)
		}
	}
	return s.Time(y, m, 1, start.Location())
}

// DaysInMonth counts the days of the month beginning at start.
func (s System) DaysInMonth(start time.Time) int {
	y, m, _ := s.Date(start)
	return s.year(y).Months[m-1].Days
}

// Page names a month in a URL.
func (s System) Page(t time.Time) string {
	y, m, _ := s.Date(t)
	return fmt.Sprintf("%04d-%02d", y, m)
}

// ParsePage reads a month from a URL. A page links the months either side
// of it, so the years either side must be reckoned too.
func (s System) ParsePage(page string, loc *time.Location) (time.Time, error) {
	var y, m int
	if _, err := fmt.Sscanf(page, "%d-%d", &y, &m); err != nil {
		return time.Time{}, fmt.Errorf("not a month: %q", page)
	}
	_, before := s.cal.Year(y - 1)
	_, after := s.cal.Year(y + 1)
	if !before || !after {
		return time.Time{}, fmt.Errorf("not a month: %q", page)
	}
	return s.Day(y, m, 1, loc)
}

// MonthKey is the locale key naming a month.
func (s System) MonthKey(month int) string {
	return fmt.Sprintf(s.monthKey, month)
}

// YearDay is the ordinal day in the year, for counting weeks.
func (s System) YearDay(t time.Time) int {
	y, _, _ := s.Date(t)
	return int(rscale.DayOf(t)-s.year(y).Start) + 1
}

// WeekStarts is the weekday the calendar's own weeks begin on.
func (s System) WeekStarts() time.Weekday { return s.weekStart }

// year is a year of a date the calendar has already counted.
func (s System) year(y int) rscale.Year {
	year, ok := s.cal.Year(y)
	if !ok {
		panic(fmt.Sprintf("%s does not reckon the year %d", s.name, y))
	}
	return year
}
