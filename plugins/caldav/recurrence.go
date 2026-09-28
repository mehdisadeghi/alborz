package alborzcaldav

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"git.mehdix.org/alborz"
)

// Monthly and yearly rules count in the reader's calendar (RFC 7529 RSCALE).

// Repeat is a rule as the form holds it.
type Repeat struct {
	Freq  string
	Until string
}

const repeatCustom = "custom"

var repeatFreqs = []string{"daily", "weekly", "monthly", "yearly"}

type rule struct {
	freq     string
	interval int
	until    time.Time
	count    int
	cal      alborz.CalendarSystem
	skip     string
	other    bool
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
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t
	}
	for _, layout := range []string{"20060102T150405", "20060102"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

func formRepeat(ctx *alborz.Context, event *ical.Event, loc *time.Location) Repeat {
	prop := event.Props.Get(ical.PropRecurrenceRule)
	if prop == nil {
		return Repeat{}
	}
	r := parseRule(prop.Value, loc)
	// A monthly or yearly rule in another calendar would mean other days if rewritten.
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

// UNTIL takes DTSTART's value type (RFC 5545 3.3.10) and includes the last day.
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

// A rule with a part not read here yields only its first instance: a wrong one is worse.
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
	// Bounded so a runaway rule cannot spin.
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
