package alborzcaldav

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"git.mehdix.org/alborz"
)

// The form offers the rules a reader writes without thinking: every
// day, week, month or year, with or without a last day. Written under
// a Solar Hijri calendar, a monthly or yearly rule counts in it
// (RFC 7529, RSCALE), so "every year on 1 Farvardin" is one rule and
// not a list of events. Anything richer that arrived from elsewhere is
// kept as it is.

// Repeat is a rule as the form holds it.
type Repeat struct {
	Freq  string // daily, weekly, monthly, yearly, custom, or empty
	Until string // the last day, as the field holds it
}

const repeatCustom = "custom"

// repeatFreqs are the choices the form offers, in its order.
var repeatFreqs = []string{"daily", "weekly", "monthly", "yearly"}

// rule is an RRULE taken apart, only as far as this file reads one.
type rule struct {
	freq     string
	interval int
	until    time.Time
	count    int
	cal      alborz.CalendarSystem
	skip     string
	other    bool // a part this file does not read
}

func parseRule(s string, loc *time.Location) rule {
	r := rule{interval: 1, cal: alborz.Calendar(""), skip: "OMIT"}
	for _, part := range strings.Split(s, ";") {
		key, value, _ := strings.Cut(part, "=")
		switch strings.ToUpper(key) {
		case "FREQ":
			r.freq = strings.ToLower(value)
		case "INTERVAL":
			r.interval, _ = strconv.Atoi(value)
		case "COUNT":
			r.count, _ = strconv.Atoi(value)
		case "UNTIL":
			r.until = parseUntil(value, loc)
		case "RSCALE":
			cal, ok := alborz.CalendarOfRSCALE(value)
			if !ok {
				r.other = true
			}
			r.cal = cal
		case "SKIP":
			r.skip = strings.ToUpper(value)
		case "WKST":
		default:
			r.other = true
		}
	}
	return r
}

func parseUntil(s string, loc *time.Location) time.Time {
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			if strings.HasSuffix(layout, "Z") {
				return t.UTC()
			}
			return t
		}
	}
	return time.Time{}
}

// formRepeat reads an event's rule into the form; a rule the form
// cannot write is shown as custom and kept as it is.
func formRepeat(ctx *alborz.Context, event *ical.Event, loc *time.Location) Repeat {
	prop := event.Props.Get(ical.PropRecurrenceRule)
	if prop == nil {
		return Repeat{}
	}
	r := parseRule(prop.Value, loc)
	// A monthly or yearly rule counts in one calendar; shown as the
	// form's own it would be written back in the reader's, and mean
	// other days. Days and weeks are the same in every calendar.
	neutral := r.freq == "daily" || r.freq == "weekly"
	simple := !r.other && r.interval == 1 && r.count == 0 &&
		(neutral || r.cal.Name() == ctx.CalendarSystem().Name())
	if !simple {
		return Repeat{Freq: repeatCustom}
	}
	var until string
	if !r.until.IsZero() {
		until = ctx.InputDate(r.until.In(loc))
	}
	return Repeat{Freq: r.freq, Until: until}
}

// setRepeat writes the form's rule onto the event. The last day is the
// last day included, whichever the event's own value type, which 5545
// 3.3.10 wants matched by UNTIL.
func setRepeat(ctx *alborz.Context, event *ical.Event, rep Repeat, start time.Time, allDay bool, loc *time.Location) error {
	switch rep.Freq {
	case "":
		event.Props.Del(ical.PropRecurrenceRule)
		return nil
	case repeatCustom:
		return nil
	}
	parts := []string{"FREQ=" + strings.ToUpper(rep.Freq)}
	cal := ctx.CalendarSystem()
	if (rep.Freq == "monthly" || rep.Freq == "yearly") && cal.RSCALE() != alborz.Calendar("").RSCALE() {
		parts = append([]string{"RSCALE=" + cal.RSCALE()}, parts...)
		parts = append(parts, "SKIP=BACKWARD")
	}
	if rep.Until != "" {
		last, err := ctx.ReadDate(rep.Until, loc)
		if err != nil {
			return err
		}
		if last.Before(start) {
			return fmt.Errorf("ends before it starts")
		}
		if allDay {
			parts = append(parts, "UNTIL="+last.Format("20060102"))
		} else {
			parts = append(parts, "UNTIL="+last.AddDate(0, 0, 1).Add(-time.Second).UTC().Format("20060102T150405Z"))
		}
	}
	prop := ical.NewProp(ical.PropRecurrenceRule)
	prop.SetValueType(ical.ValueRecurrence)
	prop.Value = strings.Join(parts, ";")
	event.Props.Set(prop)
	return nil
}

// repeatWords says a rule in words for the event page, or nothing
// when the event does not repeat; the last day comes apart from the
// words, since a date reads left to right whatever the page does.
func repeatWords(ctx *alborz.Context, event *ical.Event, loc *time.Location) (words, until string) {
	rep := formRepeat(ctx, event, loc)
	switch rep.Freq {
	case "":
		return "", ""
	case repeatCustom:
		return ctx.T("calendar.repeat.custom"), ""
	}
	words = ctx.T("calendar.repeat." + rep.Freq)
	if rep.Until != "" {
		return fmt.Sprintf(ctx.T("calendar.repeat.until"), words), rep.Until
	}
	return words, ""
}

// expandRule lists the instances of a rule rrule-go cannot read (one
// with RSCALE) that begin before to and on or after from. A rule with a
// part this file does not read yields nothing but its first instance:
// a wrong instance is worse than a missing one.
func expandRule(s string, first, from, to time.Time) []time.Time {
	r := parseRule(s, first.Location())
	if r.other || r.interval < 1 {
		return []time.Time{first}
	}
	var out []time.Time
	loc := first.Location()
	y, m, d := r.cal.Date(first)
	h, mi, sec := first.Clock()
	at := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, mi, sec, 0, loc)
	}
	// The generation limit keeps a runaway rule from spinning; no view
	// draws more instances than this.
	made := 0
	for n := 0; n < 100000 && (r.count == 0 || made < r.count); n++ {
		var t time.Time
		switch r.freq {
		case "daily":
			t = first.AddDate(0, 0, n*r.interval)
		case "weekly":
			t = first.AddDate(0, 0, 7*n*r.interval)
		case "monthly", "yearly":
			months := n * r.interval
			if r.freq == "yearly" {
				months *= 12
			}
			month := r.cal.AddMonths(r.cal.Time(y, m, 1, loc), months)
			my, mm, _ := r.cal.Date(month)
			days := r.cal.DaysInMonth(month)
			switch {
			case d <= days:
				t = at(r.cal.Time(my, mm, d, loc))
			case r.skip == "BACKWARD":
				t = at(r.cal.Time(my, mm, days, loc))
			case r.skip == "FORWARD":
				t = at(r.cal.AddMonths(month, 1))
			default:
				continue
			}
		default:
			return []time.Time{first}
		}
		if (!r.until.IsZero() && t.After(r.until)) || !t.Before(to) {
			break
		}
		made++
		if !t.Before(from) {
			out = append(out, t)
		}
	}
	return out
}
