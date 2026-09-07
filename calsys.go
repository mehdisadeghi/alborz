package alborz

import (
	"fmt"
	"time"

	"github.com/dromara/carbon/v2"
	"github.com/dromara/carbon/v2/calendar/persian"
)

// A CalendarSystem counts the same instants in its own months and
// years. It exists so the reader may live in a calendar other than the
// Gregorian one without the server knowing: an event stays an instant
// or a date in iCalendar, and only what a page shows and asks for -
// month bounds, page names, day numbers, month names, week numbers -
// is counted in the system the reader chose. The renderer counts in
// the primary system and glosses in the secondary; the month handler
// asks it for the month's bounds. Two are known: the Gregorian one and
// the Solar Hijri one that Iran and Afghanistan count in. Another is a
// further implementation of this interface and a name in
// calendarSystems; nothing else needs to change.
type CalendarSystem interface {
	Name() string
	// Date reads t in this calendar.
	Date(t time.Time) (year, month, day int)
	// MonthStart is midnight on the first day of the month t falls in.
	MonthStart(t time.Time) time.Time
	// AddMonths moves a month start by n months.
	AddMonths(start time.Time, n int) time.Time
	// DaysInMonth counts the days of the month beginning at start.
	DaysInMonth(start time.Time) int
	// Page names a month in a URL, and ParsePage reads one back.
	Page(t time.Time) string
	ParsePage(s string, loc *time.Location) (time.Time, error)
	// MonthName names a month in the page's language.
	MonthName(month int, lang string) string
	// YearDay is the ordinal day in the year, for counting weeks.
	YearDay(t time.Time) int
	// WeekStarts is the weekday the calendar's own weeks begin on.
	WeekStarts() time.Weekday
}

const (
	gregorianName = "gregorian"
	// shcalName is the Solar Hijri (Jalali) calendar.
	shcalName = "shcal"
)

// calendarSystems are the ones a reader may count in, in the order the
// settings offer them.
var calendarSystems = []string{gregorianName, shcalName}

// Calendar returns the named system, the Gregorian one for any name
// that is not known.
func Calendar(name string) CalendarSystem {
	if name == shcalName {
		return solarHijri{}
	}
	return gregorian{}
}

type gregorian struct{}

func (gregorian) Name() string { return gregorianName }
func (gregorian) Date(t time.Time) (int, int, int) {
	y, m, d := t.Date()
	return y, int(m), d
}
func (gregorian) MonthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
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
func (gregorian) MonthName(month int, lang string) string {
	l, ok := carbonLangs[lang]
	if !ok {
		l = carbonLangs["en"]
	}
	return carbon.CreateFromDate(2000, month, 1).SetLanguage(l).ToMonthString()
}
func (gregorian) YearDay(t time.Time) int  { return t.YearDay() }
func (gregorian) WeekStarts() time.Weekday { return time.Monday }

type solarHijri struct{}

func (solarHijri) Name() string { return shcalName }
func (solarHijri) Date(t time.Time) (int, int, int) {
	d := persian.FromStdTime(t)
	return d.Year(), d.Month(), d.Day()
}
func (solarHijri) MonthStart(t time.Time) time.Time {
	y, m, _ := solarHijri{}.Date(t)
	return shDay(y, m, 1, t.Location())
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
	return shDay(y, m, 1, start.Location())
}
func (solarHijri) DaysInMonth(start time.Time) int {
	next := solarHijri{}.AddMonths(start, 1)
	return int(next.Sub(start).Hours()+12) / 24
}
func (solarHijri) Page(t time.Time) string {
	y, m, _ := solarHijri{}.Date(t)
	return fmt.Sprintf("%04d-%02d", y, m)
}
func (solarHijri) ParsePage(s string, loc *time.Location) (time.Time, error) {
	var y, m int
	if _, err := fmt.Sscanf(s, "%d-%d", &y, &m); err != nil || m < 1 || m > 12 {
		return time.Time{}, fmt.Errorf("not a month: %q", s)
	}
	return shDay(y, m, 1, loc), nil
}
func (solarHijri) MonthName(month int, lang string) string {
	d := persian.NewPersian(1400, month, 1)
	if lang == "fa" {
		return d.ToMonthString(persian.FaLocale)
	}
	return d.ToMonthString(persian.EnLocale)
}

// YearDay needs no table: the first six months hold 31 days and the
// next five hold 30.
func (solarHijri) YearDay(t time.Time) int {
	_, m, d := solarHijri{}.Date(t)
	if m <= 6 {
		return (m-1)*31 + d
	}
	return 186 + (m-7)*30 + d
}
func (solarHijri) WeekStarts() time.Weekday { return time.Saturday }

// shDay is midnight on a Solar Hijri date, in loc. The converter
// answers in UTC for a date, which is only the day, so the wall clock is
// rebuilt in the reader's zone.
func shDay(y, m, d int, loc *time.Location) time.Time {
	g := persian.NewPersian(y, m, d).ToGregorian().Time
	return time.Date(g.Year(), g.Month(), g.Day(), 0, 0, 0, 0, loc)
}
