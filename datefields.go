package alborz

import (
	"fmt"
	"strings"
	"time"

	"git.mehdix.org/alborz/internal/calsys"
)

// Non-Gregorian calendars take typed dates, since a native date picker speaks only Gregorian.
const (
	isoDate     = "2006-01-02"
	isoDateTime = "2006-01-02T15:04"
	typedDate   = "%04d/%02d/%02d"
	typedTime   = "%02d:%02d"
)

// TypedDates reports whether forms take dates typed rather than picked.
func (g *GlobalRenderData) TypedDates() bool { return g.PrimaryCalendar().Name() != calsys.Gregorian }

// InputDate and InputDateTime are a date as its field holds it.
func (g *GlobalRenderData) InputDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if !g.TypedDates() {
		return t.Format(isoDate)
	}
	y, m, d := g.PrimaryCalendar().Date(t)
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

func (ctx *Context) InputDate(t time.Time) string { return ctx.dates().InputDate(t) }

// ReadDate and ReadDateTime read a field's date back in the reader's calendar.
func (ctx *Context) ReadDate(s string, loc *time.Location) (time.Time, error) {
	g := ctx.dates()
	if !g.TypedDates() {
		return time.ParseInLocation(isoDate, s, loc)
	}
	day, rest, err := typedDay(g.PrimaryCalendar(), s, loc)
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
	day, rest, err := typedDay(g.PrimaryCalendar(), s, loc)
	var h, m int
	if err != nil || rest == "" {
		return time.Time{}, fmt.Errorf("not a date and time: %q", s)
	}
	if _, err := fmt.Sscanf(rest, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("not a time: %q", rest)
	}
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), nil
}

func typedDay(cal calsys.System, s string, loc *time.Location) (time.Time, string, error) {
	s = strings.TrimSpace(latinDigits(s))
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
