// Package dates writes and reads dates as a reader sees them: in their language, zone and calendars.
package dates

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/number"

	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/internal/locale"
)

// Reader is the person a date is written for.
type Reader struct {
	Lang string
	// Loc is the reader's zone; nil keeps a time in its own.
	Loc *time.Location
	// Primary is the calendar the reader counts in, Secondary the one glossed beside it, empty for none.
	Primary   string
	Secondary string
}

func (r Reader) t(key string) string { return locale.T(r.Lang, key) }

// Since dates a message in the reader's language, as short as a list column.
func (r Reader) Since(t time.Time) string {
	unit, n := elapsed(r.In(t), r.In(time.Now()))
	if unit == "" {
		return r.t("since.now")
	}
	key := "since.ago"
	if n < 0 {
		key, n = "since.ahead", -n
	}
	// A year count is never grouped: "2025 yrs ago", not "2,025".
	span := locale.Sprintf(r.Lang, locale.Plural(r.Lang, "since."+unit, n), number.Decimal(n, number.NoSeparator()))
	return locale.Tf(r.Lang, key, span)
}

var secondsIn = []struct {
	unit string
	n    int64
}{
	{"week", int64(7 * 24 * time.Hour / time.Second)},
	{"day", int64(24 * time.Hour / time.Second)},
	{"hour", int64(time.Hour / time.Second)},
	{"minute", int64(time.Minute / time.Second)},
	{"second", 1},
}

func elapsed(t, now time.Time) (string, int) {
	sign, s, e := 1, t, now
	if e.Before(s) {
		sign, s, e = -1, e, s
	}
	years := e.Year() - s.Year()
	if s.AddDate(years, 0, 0).After(e) {
		years--
	}
	if years > 0 {
		return "year", sign * years
	}
	months := (e.Year()-s.Year())*12 + int(e.Month()-s.Month())
	if s.AddDate(0, months, 0).After(e) {
		months--
	}
	if months > 0 {
		return "month", sign * months
	}
	secs := e.Unix() - s.Unix()
	for _, u := range secondsIn {
		if secs >= u.n {
			return u.unit, sign * int(secs/u.n)
		}
	}
	return "", 0
}

// Full states a full date and time.
func (r Reader) Full(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return locale.Sprintf(r.Lang, r.t("date.withtime"), r.Long(t), r.Clock(t))
}

// Short is a bare date, without the current year.
func (r Reader) Short(t time.Time) string {
	cal := calsys.For(r.Calendar(), r.In(t))
	year, m, day := cal.Date(r.In(t))
	thisYear, _, _ := cal.Date(r.In(time.Now()))
	month := r.ShortMonth(cal, m)
	if year != thisYear {
		return locale.Sprintf(r.Lang, r.t("date.shortyear"), day, month, locale.Year(r.Lang, year))
	}
	return locale.Sprintf(r.Lang, r.t("date.short"), day, month)
}

// ShortDay is Short led by the weekday, for a title too narrow for Long.
func (r Reader) ShortDay(t time.Time) string {
	weekday := r.t(fmt.Sprintf("calendar.weekdayshort.%d", r.In(t).Weekday()))
	return locale.Sprintf(r.Lang, r.t("date.shortday"), weekday, r.Short(t))
}

// DayMonth is a day and its month as Short writes them, split around the day so a page can mark the number alone.
type DayMonth struct {
	Before, Day, After string
}

// DayMonth writes a day of a month of cal in the order the language puts them.
func (r Reader) DayMonth(cal calsys.System, day, month int) DayMonth {
	before, after, ok := strings.Cut(r.t("date.short"), "%[1]d")
	if !ok {
		panic("date.short in " + r.Lang + " has no %[1]d")
	}
	name := r.ShortMonth(cal, month)
	return DayMonth{
		Before: strings.ReplaceAll(before, "%[2]s", name),
		Day:    locale.Num(r.Lang, day),
		After:  strings.ReplaceAll(after, "%[2]s", name),
	}
}

// Clock is the time of day in the reader's zone.
func (r Reader) Clock(t time.Time) string {
	t = r.In(t)
	return locale.Sprintf(r.Lang, "%02d:%02d", t.Hour(), t.Minute())
}

// In is t in the reader's zone.
func (r Reader) In(t time.Time) time.Time {
	if r.Loc != nil {
		return t.In(r.Loc)
	}
	return t
}

// Weekday names a weekday.
func (r Reader) Weekday(d time.Weekday) string {
	return r.t(fmt.Sprintf("calendar.weekday.%d", d))
}

// Month names the month of t, in the primary calendar.
func (r Reader) Month(t time.Time) string {
	cal := calsys.For(r.Calendar(), r.In(t))
	_, m, _ := cal.Date(r.In(t))
	return r.t(cal.MonthKey(m))
}

// ShortMonth names a month of cal as a narrow column does.
func (r Reader) ShortMonth(cal calsys.System, month int) string {
	if cal.Name() != calsys.Gregorian {
		return r.t(cal.MonthKey(month))
	}
	return r.t(fmt.Sprintf("calendar.gmonthshort.%d", month))
}

// Calendar is the primary calendar.
func (r Reader) Calendar() calsys.System { return calsys.Of(r.Primary) }

// SecondaryCalendar is the calendar glossed beside the primary one at t; false for none, or one that cannot count t.
func (r Reader) SecondaryCalendar(t time.Time) (calsys.System, bool) {
	if r.Secondary == "" || r.Secondary == r.Calendar().Name() {
		return calsys.System{}, false
	}
	cal := calsys.Of(r.Secondary)
	return cal, calsys.For(cal, t) == cal
}

// SecondaryDate is the full secondary date of one day.
func (r Reader) SecondaryDate(t time.Time) string {
	cal, ok := r.SecondaryCalendar(r.In(t))
	if !ok || t.IsZero() {
		return ""
	}
	return r.DateIn(cal, t)
}

// DateIn is the date of t in cal, day, month and year.
func (r Reader) DateIn(cal calsys.System, t time.Time) string {
	y, m, d := cal.Date(r.In(t))
	return locale.Sprintf(r.Lang, "%d %s %s", d, r.t(cal.MonthKey(m)), locale.Year(r.Lang, y))
}

// MonthYear is the calendar heading for t's month, without the current year.
func (r Reader) MonthYear(t time.Time) string {
	return r.monthYear(t, r.Month(t))
}

// MonthYearShort is MonthYear with the month abbreviated, for a narrow toolbar.
func (r Reader) MonthYearShort(t time.Time) string {
	cal := calsys.For(r.Calendar(), r.In(t))
	_, m, _ := cal.Date(r.In(t))
	return r.monthYear(t, r.ShortMonth(cal, m))
}

func (r Reader) monthYear(t time.Time, month string) string {
	cal := calsys.For(r.Calendar(), r.In(t))
	year, _, _ := cal.Date(r.In(t))
	thisYear, _, _ := cal.Date(r.In(time.Now()))
	if year == thisYear {
		return month
	}
	return fmt.Sprintf("%s %s", month, locale.Year(r.Lang, year))
}

// Long is a day heading; time.Format knows only English names.
func (r Reader) Long(t time.Time) string {
	weekday := r.Weekday(r.In(t).Weekday())
	month := r.Month(t)
	y, _, d := calsys.For(r.Calendar(), r.In(t)).Date(r.In(t))
	return locale.Sprintf(r.Lang, r.t("date.long"), weekday, d, month, locale.Year(r.Lang, y))
}
