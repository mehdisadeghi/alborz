package alborz

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// CalendarSystem counts instants in its own months and years, for display and input only.
type CalendarSystem interface {
	Name() string
	// RSCALE is the calendar's name in a recurrence rule.
	RSCALE() string
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

// CalendarNamed returns name when it is a known system, else "", so a form cannot store an unknown calendar.
func CalendarNamed(name string) string {
	if slices.Contains(calendarSystems, name) {
		return name
	}
	return ""
}

var calendarSystems = []string{gregorianName, shcalName}

// Calendar returns the named system, Gregorian for an unknown name.
func Calendar(name string) CalendarSystem {
	if name == shcalName {
		return solarHijri{}
	}
	return gregorian{}
}

// CalendarOfRSCALE returns the system a rule counts in; false for an unknown scale.
func CalendarOfRSCALE(scale string) (CalendarSystem, bool) {
	for _, name := range calendarSystems {
		if cal := Calendar(name); strings.EqualFold(cal.RSCALE(), scale) {
			return cal, true
		}
	}
	return gregorian{}, false
}

type gregorian struct{}

func (gregorian) Name() string   { return gregorianName }
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
	return translate(lang, fmt.Sprintf("calendar.gmonth.%d", month))
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
func (solarHijri) ParsePage(s string, loc *time.Location) (time.Time, error) {
	var y, m int
	if _, err := fmt.Sscanf(s, "%d-%d", &y, &m); err != nil || m < 1 || m > 12 {
		return time.Time{}, fmt.Errorf("not a month: %q", s)
	}
	return solarHijri{}.Time(y, m, 1, loc), nil
}
func (solarHijri) MonthName(month int, lang string) string {
	return translate(lang, fmt.Sprintf("calendar.shmonth.%d", month))
}
func (solarHijri) YearDay(t time.Time) int {
	_, m, d := solarHijri{}.Date(t)
	if m <= 6 {
		return (m-1)*31 + d
	}
	return 186 + (m-7)*30 + d
}
func (solarHijri) WeekStarts() time.Weekday { return time.Saturday }

// shYear returns the years since the last leap year (0 for a leap year) and the Gregorian day of Nowruz, per Borkowski (Earth, Moon and Planets 74, 1996).
func shYear(jy int) (sinceLeap int, nowruz time.Time) {
	breaks := [...]int{-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210, 1635, 2060, 2097, 2192, 2262, 2324, 2394, 2456, 3178}
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

// Non-Gregorian calendars take typed dates, since a native date picker speaks only Gregorian.
const (
	isoDate     = "2006-01-02"
	isoDateTime = "2006-01-02T15:04"
	typedDate   = "%04d/%02d/%02d"
	typedTime   = "%02d:%02d"
)

// TypedDates reports whether forms take dates typed rather than picked.
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

// DateExample and DateTimeExample show today as a typed field expects it.
func (g *GlobalRenderData) DateExample() string { return g.InputDate(g.InTimezone(time.Now())) }

func (g *GlobalRenderData) DateTimeExample() string {
	return g.InputDateTime(g.InTimezone(time.Now()))
}

// InputDay is InputDate for an ISO day string; anything else is returned as typed.
func (g *GlobalRenderData) InputDay(iso string) string {
	t, err := time.Parse(isoDate, iso)
	if err != nil {
		return iso
	}
	return g.InputDate(t)
}

func (ctx *Context) InputDate(t time.Time) string     { return ctx.dates().InputDate(t) }
func (ctx *Context) InputDateTime(t time.Time) string { return ctx.dates().InputDateTime(t) }

// ReadDate and ReadDateTime read a field's date back in the reader's calendar.
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
