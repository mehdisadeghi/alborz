package alborz

import (
	"fmt"
	"strings"
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
	// Date reads t in this calendar, and Time is midnight on a date of it.
	Date(t time.Time) (year, month, day int)
	Time(year, month, day int, loc *time.Location) time.Time
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
func (gregorian) Time(y, m, d int, loc *time.Location) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc)
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
func (solarHijri) Time(y, m, d int, loc *time.Location) time.Time { return shDay(y, m, d, loc) }
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

// A native date picker speaks only Gregorian, so under any other
// primary calendar a date field is text and the date is typed in that
// calendar: 1405/06/16, or 1405/06/16 10:30 with a time, in either
// digit shape and with either separator, and shown in the page's
// digits. The Gregorian fields keep the picker and its ISO value.
const (
	isoDate     = "2006-01-02"
	isoDateTime = "2006-01-02T15:04"
	typedDate   = "%04d/%02d/%02d"
	typedTime   = "%02d:%02d"
)

// TypedDates says whether the forms take dates typed rather than picked.
func (g *GlobalRenderData) TypedDates() bool { return g.primary().Name() != gregorianName }

// InputDate and InputDateTime are a date as its field holds it.
func (g *GlobalRenderData) InputDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if !g.TypedDates() {
		return t.Format(isoDate)
	}
	y, m, d := g.primary().Date(t)
	return g.shapeDigits(fmt.Sprintf(typedDate, y, m, d))
}

func (g *GlobalRenderData) InputDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if !g.TypedDates() {
		return t.Format(isoDateTime)
	}
	return g.InputDate(t) + " " + g.shapeDigits(fmt.Sprintf(typedTime, t.Hour(), t.Minute()))
}

// DateExample and DateTimeExample are today, as a typed field wants
// it, for the field to show what it expects.
func (g *GlobalRenderData) DateExample() string { return g.InputDate(g.InTimezone(time.Now())) }

func (g *GlobalRenderData) DateTimeExample() string {
	return g.InputDateTime(g.InTimezone(time.Now()))
}

// InputDay is InputDate for a day kept as an ISO string; one that is
// not a day is what was typed, and goes back to the field as it came.
func (g *GlobalRenderData) InputDay(iso string) string {
	t, err := time.Parse(isoDate, iso)
	if err != nil {
		return iso
	}
	return g.InputDate(t)
}

func (ctx *Context) InputDate(t time.Time) string     { return ctx.dates().InputDate(t) }
func (ctx *Context) InputDateTime(t time.Time) string { return ctx.dates().InputDateTime(t) }

// ReadDate and ReadDateTime read a field's date back in the reader's
// calendar; the whole day is a time, which iCalendar wants when it is
// a date.
func (ctx *Context) ReadDate(s string, loc *time.Location) (time.Time, error) {
	g := ctx.dates()
	if !g.TypedDates() {
		return time.ParseInLocation(isoDate, s, loc)
	}
	day, rest, err := typedDay(g.primary(), s, loc)
	if err != nil || rest != "" {
		return time.Time{}, fmt.Errorf("not a date: %q", s)
	}
	return day, nil
}

func (ctx *Context) ReadDateTime(s string, loc *time.Location) (time.Time, error) {
	g := ctx.dates()
	if !g.TypedDates() {
		return time.ParseInLocation(isoDateTime, s, loc)
	}
	day, rest, err := typedDay(g.primary(), s, loc)
	var h, m int
	if err != nil || rest == "" {
		return time.Time{}, fmt.Errorf("not a date and time: %q", s)
	}
	if _, err := fmt.Sscanf(rest, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("not a time: %q", rest)
	}
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), nil
}

// typedDay reads the date at the front of a typed field and returns
// what followed it.
func typedDay(cal CalendarSystem, s string, loc *time.Location) (time.Time, string, error) {
	s = strings.TrimSpace(LatinDigits(s))
	date, rest, _ := strings.Cut(s, " ")
	var y, m, d int
	if n, err := fmt.Sscanf(strings.ReplaceAll(date, "-", "/"), "%d/%d/%d", &y, &m, &d); n != 3 || err != nil {
		return time.Time{}, "", fmt.Errorf("not a date: %q", date)
	}
	if m < 1 || m > 12 || d < 1 || d > cal.DaysInMonth(cal.Time(y, m, 1, loc)) {
		return time.Time{}, "", fmt.Errorf("no such day: %q", date)
	}
	return cal.Time(y, m, d, loc), strings.TrimSpace(rest), nil
}
