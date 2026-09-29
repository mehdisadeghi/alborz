package calendar

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alborzmail/go-recur"
	"github.com/emersion/go-ical"

	"git.mehdix.org/alborz/internal/calsys"
	"git.mehdix.org/alborz/web"
)

// Monthly and yearly rules count in the reader's calendar (RFC 7529 RSCALE).

// Repeat is a rule as the form holds it.
type Repeat struct {
	Freq  string
	Until web.DateField
}

const repeatCustom = "custom"

var repeatFreqs = []string{"daily", "weekly", "monthly", "yearly"}

func formRepeat(ctx *web.Context, event *ical.Event, loc *time.Location) Repeat {
	if event.Props.Get(ical.PropRecurrenceRule) == nil {
		return Repeat{Until: ctx.InputDate(time.Time{})}
	}
	rule, err := event.Props.RecurrenceRule()
	if err != nil || !plain(*rule) {
		return Repeat{Freq: repeatCustom, Until: ctx.InputDate(time.Time{})}
	}
	freq := strings.ToLower(rule.Freq.String())
	scale := cmp.Or(rule.Scale, calsys.Of("").RSCALE())
	// A monthly or yearly rule in another calendar would mean other days if rewritten.
	neutral := rule.Freq == recur.Daily || rule.Freq == recur.Weekly
	if !slices.Contains(repeatFreqs, freq) || !neutral && scale != ctx.CalendarSystem().RSCALE() {
		return Repeat{Freq: repeatCustom, Until: ctx.InputDate(time.Time{})}
	}
	var until time.Time
	if rule.Until != nil {
		until = rule.Until.Time.In(loc)
		if rule.Until.Kind != recur.DateTime {
			until = time.Date(rule.Until.Year(), rule.Until.Month(), rule.Until.Day(), rule.Until.Hour(), rule.Until.Minute(), rule.Until.Second(), 0, loc)
		}
	}
	return Repeat{Freq: freq, Until: ctx.InputDate(until)}
}

// plain reports whether the form can say rule: a frequency, a last day, and
// the scale and SKIP it writes itself.
func plain(r recur.Rule) bool {
	return r.Interval <= 1 && r.Count == 0 && r.BySecond == nil && r.ByMinute == nil && r.ByHour == nil &&
		r.ByDay == nil && r.ByMonthDay == nil && r.ByYearDay == nil && r.ByWeekNo == nil && r.ByMonth == nil && r.BySetPos == nil
}

// UNTIL takes DTSTART's value type (RFC 5545 3.3.10) and includes the last day.
func setRepeat(ctx *web.Context, event *ical.Event, rep Repeat, start time.Time, allDay bool, loc *time.Location) error {
	switch rep.Freq {
	case "":
		event.Props.Del(ical.PropRecurrenceRule)
		return nil
	case repeatCustom:
		return nil
	}
	if !slices.Contains(repeatFreqs, rep.Freq) {
		return fmt.Errorf("no repeat %q", rep.Freq)
	}
	rule, err := recur.Parse("FREQ=" + strings.ToUpper(rep.Freq))
	if err != nil {
		return err
	}
	cal := ctx.CalendarSystem()
	if (rule.Freq == recur.Monthly || rule.Freq == recur.Yearly) && cal.RSCALE() != calsys.Of("").RSCALE() {
		rule.Scale, rule.Skip = cal.RSCALE(), recur.Backward
	}
	if rep.Until.Value != "" {
		last, err := rep.Until.Date(loc)
		if err != nil {
			return err
		}
		if last.Before(start) {
			return fmt.Errorf("ends before it starts")
		}
		if allDay {
			rule.Until = &recur.Value{Time: last, Kind: recur.Date}
		} else {
			rule.Until = &recur.Value{Time: last.AddDate(0, 0, 1).Add(-time.Second).UTC(), Kind: recur.DateTime}
		}
	}
	event.Props.SetRecurrenceRule(&rule)
	return nil
}

func repeatWords(ctx *web.Context, event *ical.Event, loc *time.Location) (words, until string) {
	rep := formRepeat(ctx, event, loc)
	switch rep.Freq {
	case "":
		return "", ""
	case repeatCustom:
		return ctx.T("calendar.repeat.custom"), ""
	}
	words = ctx.T("calendar.repeat." + rep.Freq)
	if rep.Until.Value != "" {
		return fmt.Sprintf(ctx.T("calendar.repeat.until"), words), rep.Until.Value
	}
	return words, ""
}
