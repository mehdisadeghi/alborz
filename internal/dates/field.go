package dates

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
)

// Field is a date field's text and the calendar it is written in, so it is read back in that calendar and never by its shape.
type Field struct{ Value, Calendar string }

// Typed reports whether the field takes its date typed rather than picked.
func (f Field) Typed() bool { return f.Calendar != calsys.Gregorian }

// Input and InputTime write t in the primary calendar, or in Gregorian where the primary cannot count it; zero is an empty field.
func (r Reader) Input(t time.Time) Field {
	return r.inputIn(r.calendarFor(t), t, false)
}

func (r Reader) InputTime(t time.Time) Field {
	return r.inputIn(r.calendarFor(t), t, true)
}

func (r Reader) calendarFor(t time.Time) calsys.System {
	if t.IsZero() {
		return r.Calendar()
	}
	return calsys.For(r.Calendar(), t)
}

func (r Reader) inputIn(cal calsys.System, t time.Time, clock bool) Field {
	f := Field{Calendar: cal.Name()}
	switch {
	case t.IsZero():
	case !f.Typed() && clock:
		f.Value = t.Format(isoDateTime)
	case !f.Typed():
		f.Value = t.Format(isoDate)
	default:
		y, m, d := cal.Date(t)
		f.Value = locale.Shape(r.Lang, fmt.Sprintf(typedDate, y, m, d))
		if clock {
			f.Value += " " + locale.Shape(r.Lang, fmt.Sprintf(typedTime, t.Hour(), t.Minute()))
		}
	}
	return f
}

// Example and ExampleTime show today as a typed field in f's calendar expects it.
func (r Reader) Example(f Field) string {
	return r.inputIn(calsys.Of(f.Calendar), r.In(time.Now()), false).Value
}

func (r Reader) ExampleTime(f Field) string {
	return r.inputIn(calsys.Of(f.Calendar), r.In(time.Now()), true).Value
}

// Day is Input for an ISO day string; anything else is kept as written, in the primary calendar.
func (r Reader) Day(iso string) Field {
	t, err := time.Parse(isoDate, iso)
	if err != nil {
		return Field{Value: iso, Calendar: r.Calendar().Name()}
	}
	return r.Input(t)
}

// Read and ReadTime read the field in the calendar it names.
func (f Field) Read(loc *time.Location) (time.Time, error)     { return f.read(loc, false) }
func (f Field) ReadTime(loc *time.Location) (time.Time, error) { return f.read(loc, true) }

func (f Field) read(loc *time.Location, clock bool) (time.Time, error) {
	cal, err := f.calendar()
	if err != nil {
		return time.Time{}, err
	}
	if !f.Typed() {
		layout := isoDate
		if clock {
			layout = isoDateTime
		}
		return time.ParseInLocation(layout, f.Value, loc)
	}
	day, rest, err := typedDay(cal, f.Value, loc)
	if err != nil || !clock && rest != "" || clock && rest == "" {
		message := "not a date"
		if clock {
			message = "not a date and time"
		}
		return time.Time{}, fmt.Errorf(message+": %q", f.Value)
	}
	if !clock {
		return day, nil
	}
	var h, m int
	if _, err := fmt.Sscanf(rest, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("not a time: %q", rest)
	}
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), nil
}

func (f Field) calendar() (calsys.System, error) {
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
