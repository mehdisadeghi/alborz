package calsys

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// System counts instants in its own months and years, for display and input only.
type System interface {
	Name() string
	// RSCALE is the calendar's name in a recurrence rule.
	RSCALE() string
	// Date reads t in this calendar, and Time is midnight on a date of it.
	Date(t time.Time) (year, month, day int)
	Time(year, month, day int, loc *time.Location) time.Time
	// Day is Time for a date as typed, refusing one the calendar does not have or cannot count.
	Day(year, month, day int, loc *time.Location) (time.Time, error)
	// MonthStart is midnight on the first day of the month t falls in.
	MonthStart(t time.Time) time.Time
	// AddMonths moves a month start by n months.
	AddMonths(start time.Time, n int) time.Time
	// DaysInMonth counts the days of the month beginning at start.
	DaysInMonth(start time.Time) int
	// Page names a month in a URL, and ParsePage reads one back.
	Page(t time.Time) string
	ParsePage(s string, loc *time.Location) (time.Time, error)
	// MonthKey is the locale key naming a month.
	MonthKey(month int) string
	// YearDay is the ordinal day in the year, for counting weeks.
	YearDay(t time.Time) int
	// WeekStarts is the weekday the calendar's own weeks begin on.
	WeekStarts() time.Weekday
}

const (
	// Gregorian is the Gregorian calendar, which numbers weeks by ISO 8601.
	Gregorian = "gregorian"
	// shcalName is the Solar Hijri (Jalali) calendar.
	shcalName = "shcal"
)

// Known returns name when it is a known system, else "", so a form cannot store an unknown calendar.
func Known(name string) string {
	if slices.Contains(calendarSystems, name) {
		return name
	}
	return ""
}

var calendarSystems = []string{Gregorian, shcalName}

// Of returns the named system, Gregorian for an unknown name.
func Of(name string) System {
	if name == shcalName {
		return solarHijri{}
	}
	return gregorian{}
}

// OfRSCALE returns the system a rule counts in; false for an unknown scale.
func OfRSCALE(scale string) (System, bool) {
	for _, name := range calendarSystems {
		if cal := Of(name); strings.EqualFold(cal.RSCALE(), scale) {
			return cal, true
		}
	}
	return gregorian{}, false
}

// For is s where it reckons t, else Gregorian: a date s cannot count is shown in one that can, never guessed.
func For(s System, t time.Time) System {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if s == (solarHijri{}) && (day.Before(shFirst) || !day.Before(shEnd)) {
		return gregorian{}
	}
	return s
}

// validDay refuses a date cal does not have or cannot count.
func validDay(cal System, y, m, d int, loc *time.Location) (time.Time, error) {
	if m < 1 || m > 12 {
		return time.Time{}, fmt.Errorf("no month %d", m)
	}
	t := cal.Time(y, m, d, loc)
	if For(cal, t) != cal || d < 1 || d > cal.DaysInMonth(cal.Time(y, m, 1, loc)) {
		return time.Time{}, fmt.Errorf("no day %d/%d/%d", y, m, d)
	}
	return t, nil
}

// shFirst is Nowruz of the first year shYear reckons, -61; shEnd the first day whose year may be past its last.
var shFirst, shEnd = time.Date(560, time.March, 20, 0, 0, 0, 0, time.UTC), time.Date(3799, time.January, 1, 0, 0, 0, 0, time.UTC)

type gregorian struct{}

func (gregorian) Name() string   { return Gregorian }
func (gregorian) RSCALE() string { return "GREGORIAN" }
func (gregorian) Date(t time.Time) (int, int, int) {
	y, m, d := t.Date()
	return y, int(m), d
}
func (gregorian) Time(y, m, d int, loc *time.Location) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc)
}
func (gregorian) MonthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
func (g gregorian) Day(y, m, d int, loc *time.Location) (time.Time, error) {
	return validDay(g, y, m, d, loc)
}
func (gregorian) AddMonths(start time.Time, n int) time.Time { return start.AddDate(0, n, 0) }
func (gregorian) DaysInMonth(start time.Time) int {
	return time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
func (gregorian) Page(t time.Time) string { return t.Format("2006-01") }
func (gregorian) ParsePage(s string, loc *time.Location) (time.Time, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc), nil
}
func (gregorian) MonthKey(month int) string {
	return fmt.Sprintf("calendar.gmonth.%d", month)
}
func (gregorian) YearDay(t time.Time) int  { return t.YearDay() }
func (gregorian) WeekStarts() time.Weekday { return time.Monday }

type solarHijri struct{}

func (solarHijri) Name() string   { return shcalName }
func (solarHijri) RSCALE() string { return "PERSIAN" }
func (solarHijri) Date(t time.Time) (int, int, int) {
	gy := t.Year()
	jy := gy - 621
	since, nowruz := shYear(jy)
	k := daysBetween(nowruz, time.Date(gy, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
	if k >= 0 {
		if k <= 185 {
			return jy, 1 + k/31, k%31 + 1
		}
		k -= 186
	} else {
		// Before Nowruz the day belongs to the previous year, whose Esfand is a day longer in a leap year.
		jy--
		k += 179
		if since == 1 {
			k++
		}
	}
	return jy, 7 + k/30, k%30 + 1
}
func (solarHijri) Time(y, m, d int, loc *time.Location) time.Time {
	_, nowruz := shYear(y)
	// The first six months hold 31 days and the next five hold 30.
	return time.Date(nowruz.Year(), nowruz.Month(), nowruz.Day()+(m-1)*31-(m/7)*(m-7)+d-1, 0, 0, 0, 0, loc)
}
func (s solarHijri) Day(y, m, d int, loc *time.Location) (time.Time, error) {
	if y < shBreaks[0] || y >= shBreaks[len(shBreaks)-1] {
		return time.Time{}, fmt.Errorf("solar hijri year %d is outside the reckoning", y)
	}
	return validDay(s, y, m, d, loc)
}
func (solarHijri) MonthStart(t time.Time) time.Time {
	y, m, _ := solarHijri{}.Date(t)
	return solarHijri{}.Time(y, m, 1, t.Location())
}
func (solarHijri) AddMonths(start time.Time, n int) time.Time {
	y, m, _ := solarHijri{}.Date(start)
	m += n
	y += (m - 1) / 12
	m = (m-1)%12 + 1
	if m < 1 {
		m += 12
		y--
	}
	return solarHijri{}.Time(y, m, 1, start.Location())
}
func (solarHijri) DaysInMonth(start time.Time) int {
	y, m, _ := solarHijri{}.Date(start)
	switch {
	case m <= 6:
		return 31
	case m <= 11:
		return 30
	}
	if since, _ := shYear(y); since == 0 {
		return 30
	}
	return 29
}
func (solarHijri) Page(t time.Time) string {
	y, m, _ := solarHijri{}.Date(t)
	return fmt.Sprintf("%04d-%02d", y, m)
}

// A page links the months either side of it, so the years either side must be reckoned too.
func (solarHijri) ParsePage(s string, loc *time.Location) (time.Time, error) {
	var y, m int
	if _, err := fmt.Sscanf(s, "%d-%d", &y, &m); err != nil || y <= shBreaks[0] || y+1 >= shBreaks[len(shBreaks)-1] {
		return time.Time{}, fmt.Errorf("not a month: %q", s)
	}
	return solarHijri{}.Day(y, m, 1, loc)
}
func (solarHijri) MonthKey(month int) string {
	return fmt.Sprintf("calendar.shmonth.%d", month)
}
func (solarHijri) YearDay(t time.Time) int {
	_, m, d := solarHijri{}.Date(t)
	if m <= 6 {
		return (m-1)*31 + d
	}
	return 186 + (m-7)*30 + d
}
func (solarHijri) WeekStarts() time.Weekday { return time.Saturday }

// shBreaks are the years shYear reckons from; the last is the first it cannot.
var shBreaks = [...]int{-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210, 1635, 2060, 2097, 2192, 2262, 2324, 2394, 2456, 3178}

// shYear returns the years since the last leap year (0 for a leap year) and the Gregorian day of Nowruz, per Borkowski (Earth, Moon and Planets 74, 1996).
func shYear(jy int) (sinceLeap int, nowruz time.Time) {
	breaks := shBreaks
	if jy < breaks[0] || jy >= breaks[len(breaks)-1] {
		panic(fmt.Sprintf("solar hijri year %d is outside the reckoning", jy))
	}
	gy := jy + 621
	leapJ, jp, jump := -14, breaks[0], 0
	for _, jm := range breaks[1:] {
		jump = jm - jp
		if jy < jm {
			break
		}
		leapJ += jump/33*8 + jump%33/4
		jp = jm
	}
	n := jy - jp
	leapJ += n/33*8 + (n%33+3)/4
	if jump%33 == 4 && jump-n == 4 {
		leapJ++
	}
	leapG := gy/4 - (gy/100+1)*3/4 - 150
	march := 20 + leapJ - leapG
	if jump-n < 6 {
		n = n - jump + (jump+4)/33*33
	}
	since := ((n+1)%33 - 1) % 4
	if since == -1 {
		since = 4
	}
	return since, time.Date(gy, time.March, march, 0, 0, 0, 0, time.UTC)
}

func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}
