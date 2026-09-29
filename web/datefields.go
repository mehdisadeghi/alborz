package web

import (
	"fmt"
	"strings"
	"time"

	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/internal/locale"
)

// Gregorian fields are picked, since a native date picker speaks only Gregorian; other calendars take dates typed.
const (
	isoDate     = "2006-01-02"
	isoDateTime = "2006-01-02T15:04"
	typedDate   = "%04d/%02d/%02d"
	typedTime   = "%02d:%02d"
	// fieldCalendar suffixes a date field's name to name the hidden field saying its calendar, as templates/partials/form.html writes it.
	fieldCalendar = "-calendar"
)

// DateField is a date field's text and the calendar it is written in, so it is read back in that calendar and never by its shape.
type DateField struct{ Value, Calendar string }

// Typed reports whether the field takes its date typed rather than picked.
func (f DateField) Typed() bool { return f.Calendar != calsys.Gregorian }

// InputDate and InputDateTime write t in the primary calendar, or in Gregorian where the primary cannot count it; zero is an empty field.
func (g *GlobalRenderData) InputDate(t time.Time) DateField {
	return g.inputIn(g.calendarFor(t), t, false)
}

func (g *GlobalRenderData) InputDateTime(t time.Time) DateField {
	return g.inputIn(g.calendarFor(t), t, true)
}

func (g *GlobalRenderData) calendarFor(t time.Time) calsys.System {
	if t.IsZero() {
		return g.PrimaryCalendar()
	}
	return calsys.For(g.PrimaryCalendar(), t)
}

func (g *GlobalRenderData) inputIn(cal calsys.System, t time.Time, clock bool) DateField {
	f := DateField{Calendar: cal.Name()}
	switch {
	case t.IsZero():
	case !f.Typed() && clock:
		f.Value = t.Format(isoDateTime)
	case !f.Typed():
		f.Value = t.Format(isoDate)
	default:
		y, m, d := cal.Date(t)
		f.Value = locale.Shape(g.Lang, fmt.Sprintf(typedDate, y, m, d))
		if clock {
			f.Value += " " + locale.Shape(g.Lang, fmt.Sprintf(typedTime, t.Hour(), t.Minute()))
		}
	}
	return f
}

// DateExample and DateTimeExample show today as a typed field in f's calendar expects it.
func (g *GlobalRenderData) DateExample(f DateField) string {
	return g.inputIn(calsys.Of(f.Calendar), g.InTimezone(time.Now()), false).Value
}

func (g *GlobalRenderData) DateTimeExample(f DateField) string {
	return g.inputIn(calsys.Of(f.Calendar), g.InTimezone(time.Now()), true).Value
}

// InputDay is InputDate for an ISO day string; anything else is kept as written, in the primary calendar.
func (g *GlobalRenderData) InputDay(iso string) DateField {
	t, err := time.Parse(isoDate, iso)
	if err != nil {
		return DateField{Value: iso, Calendar: g.PrimaryCalendar().Name()}
	}
	return g.InputDate(t)
}

func (ctx *Context) InputDate(t time.Time) DateField { return ctx.dates().InputDate(t) }

func (ctx *Context) InputDay(iso string) DateField { return ctx.dates().InputDay(iso) }

// DateField is the named field as the form sent it.
func (ctx *Context) DateField(name string) DateField {
	return DateField{Value: strings.TrimSpace(ctx.FormValue(name)), Calendar: ctx.FormValue(name + fieldCalendar)}
}

// Date and DateTime read the field in the calendar it names.
func (f DateField) Date(loc *time.Location) (time.Time, error) {
	cal, err := f.calendar()
	switch {
	case err != nil:
		return time.Time{}, err
	case !f.Typed():
		return time.ParseInLocation(isoDate, f.Value, loc)
	}
	day, rest, err := typedDay(cal, f.Value, loc)
	if err != nil || rest != "" {
		return time.Time{}, fmt.Errorf("not a date: %q", f.Value)
	}
	return day, nil
}

func (f DateField) DateTime(loc *time.Location) (time.Time, error) {
	cal, err := f.calendar()
	switch {
	case err != nil:
		return time.Time{}, err
	case !f.Typed():
		return time.ParseInLocation(isoDateTime, f.Value, loc)
	}
	day, rest, err := typedDay(cal, f.Value, loc)
	var h, m int
	if err != nil || rest == "" {
		return time.Time{}, fmt.Errorf("not a date and time: %q", f.Value)
	}
	if _, err := fmt.Sscanf(rest, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("not a time: %q", rest)
	}
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), nil
}

func (f DateField) calendar() (calsys.System, error) {
	if calsys.Known(f.Calendar) == "" {
		return calsys.System{}, fmt.Errorf("no calendar %q", f.Calendar)
	}
	return calsys.Of(f.Calendar), nil
}

func typedDay(cal calsys.System, s string, loc *time.Location) (time.Time, string, error) {
	s = strings.TrimSpace(locale.Latin(s))
	date, rest, _ := strings.Cut(s, " ")
	var y, m, d int
	if n, err := fmt.Sscanf(strings.ReplaceAll(date, "-", "/"), "%d/%d/%d", &y, &m, &d); n != 3 || err != nil {
		return time.Time{}, "", fmt.Errorf("not a date: %q", date)
	}
	t, err := cal.Day(y, m, d, loc)
	return t, strings.TrimSpace(rest), err
}
