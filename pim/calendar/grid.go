package calendar

import (
	"fmt"
	"time"

	"git.mehdix.org/alborz"
)

// The month grid's labels, in the reader's primary and secondary calendars.

// sunday is a Sunday, so adding a weekday index names that weekday.
var sunday = time.Date(2023, time.January, 1, 12, 0, 0, 0, time.UTC)

// Weekdays returns translated weekday names starting from FirstDayOfWeek.
func (d *CalendarRenderData) Weekdays() []string {
	g := &d.GlobalData
	result := make([]string, 7)
	for i := range result {
		result[i] = g.WeekdayName(sunday.AddDate(0, 0, (g.FirstDayOfWeek+i)%7))
	}
	return result
}

// MonthYearShort is MonthYear abbreviated for a narrow toolbar.
func (d *CalendarRenderData) MonthYearShort(t time.Time) string {
	g := &d.GlobalData
	cal := g.PrimaryCalendar()
	y, m, _ := cal.Date(g.InTimezone(t))
	return fmt.Sprintf("%s %s", g.ShortMonth(cal, m), g.FormatYear(y))
}

// Day is the day number of t in the primary calendar.
func (d *CalendarRenderData) Day(t time.Time) string {
	g := &d.GlobalData
	_, _, day := g.PrimaryCalendar().Date(g.InTimezone(t))
	return g.Num(day)
}

// SameMonth reports whether two instants fall in one month of the primary calendar.
func (d *CalendarRenderData) SameMonth(a, b time.Time) bool {
	g := &d.GlobalData
	cal := g.PrimaryCalendar()
	ay, am, _ := cal.Date(g.InTimezone(a))
	by, bm, _ := cal.Date(g.InTimezone(b))
	return ay == by && am == bm
}

// SecondaryDay is the day number in the secondary calendar, empty when none is chosen.
func (d *CalendarRenderData) SecondaryDay(t time.Time) string {
	g := &d.GlobalData
	cal, ok := g.SecondaryCalendar()
	if !ok {
		return ""
	}
	_, m, day := cal.Date(g.InTimezone(t))
	if day == 1 {
		return g.Num(day) + " " + g.ShortMonth(cal, m)
	}
	return g.Num(day)
}

// weekAnchor is the Thursday of the seven days from day, by which ISO 8601 numbers a week.
func weekAnchor(rowStart time.Time) time.Time {
	iso := (int(rowStart.Weekday())+6)%7 + 1
	return rowStart.AddDate(0, 0, (4-iso+7)%7)
}

func weekOf(cal alborz.CalendarSystem, day time.Time) int {
	if cal.Name() == alborz.GregorianName {
		_, week := day.ISOWeek()
		return week
	}
	y, _, _ := cal.Date(day)
	newYear := cal.Time(y, 1, 1, day.Location())
	offset := (int(newYear.Weekday()) - int(cal.WeekStarts()) + 7) % 7
	return (cal.YearDay(day)-1+offset)/7 + 1
}

// WeekNumber is the week a grid row belongs to, in the primary calendar.
func (d *CalendarRenderData) WeekNumber(rowStart time.Time) string {
	g := &d.GlobalData
	return g.Num(weekOf(g.PrimaryCalendar(), weekAnchor(g.InTimezone(rowStart))))
}

// WeekTitle names the week in words.
func (d *CalendarRenderData) WeekTitle(rowStart time.Time) string {
	return d.GlobalData.Tf("calendar.week", d.WeekNumber(rowStart))
}

// SecondaryWeek is the same week counted in the secondary calendar, empty when none is chosen.
func (d *CalendarRenderData) SecondaryWeek(rowStart time.Time) string {
	g := &d.GlobalData
	cal, ok := g.SecondaryCalendar()
	if !ok {
		return ""
	}
	return g.Num(weekOf(cal, weekAnchor(g.InTimezone(rowStart))))
}

// SecondaryMonthYear names the secondary months a primary month spans, empty when none is chosen.
func (d *CalendarRenderData) SecondaryMonthYear(t time.Time) string {
	g := &d.GlobalData
	cal, ok := g.SecondaryCalendar()
	if !ok {
		return ""
	}
	start := g.PrimaryCalendar().MonthStart(g.InTimezone(t))
	end := g.PrimaryCalendar().AddMonths(start, 1).AddDate(0, 0, -1)
	fy, fm, _ := cal.Date(start.Add(12 * time.Hour))
	ly, lm, _ := cal.Date(end.Add(12 * time.Hour))
	first, last := cal.MonthName(fm, g.Lang), cal.MonthName(lm, g.Lang)
	if fm == lm && fy == ly {
		return fmt.Sprintf("%s %s", first, g.FormatYear(fy))
	}
	if fy == ly {
		return fmt.Sprintf("%s – %s %s", first, last, g.FormatYear(ly))
	}
	return fmt.Sprintf("%s %s – %s %s", first, g.FormatYear(fy), last, g.FormatYear(ly))
}

// CalendarDayLabel is a day number, with a compact month name on the first of the month.
func (d *CalendarRenderData) CalendarDayLabel(t time.Time) string {
	g := &d.GlobalData
	cal := g.PrimaryCalendar()
	_, m, day := cal.Date(g.InTimezone(t))
	if day != 1 {
		return g.Num(day)
	}
	month := g.ShortMonth(cal, m)
	switch g.Lang {
	case "de":
		return g.Num(day) + ". " + month
	case "es", "fa":
		return g.Num(day) + " " + month
	default:
		return month + " " + g.Num(day)
	}
}
