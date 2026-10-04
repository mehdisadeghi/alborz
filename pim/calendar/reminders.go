package calendar

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-ical"
)

// ReminderReach is how far from its occurrence an alarm is looked for: a day either side holds the
// reminders calendars offer, from the moment itself to the day before.
const ReminderReach = 24 * time.Hour

// Reminder is an alarm of one occurrence, due at At.
type Reminder struct {
	At, Start time.Time
	// UID names the event; Path is its object.
	UID, Path string
}

// Reminders are the login's alarms due after from and at or before to, over every event calendar, each
// occurrence of a series on its own; times without a zone are read in loc. A calendar that fails is
// named in the error and costs the others nothing.
func (p *Calendars) Reminders(ctx context.Context, login *alborz.Login, loc *time.Location, from, to time.Time) ([]Reminder, error) {
	c, calendars, err := p.Open(ctx, login)
	if err != nil {
		return nil, err
	}
	start, end := from.Add(-ReminderReach), to.Add(ReminderReach)
	query := EventQuery(start, end)
	seen := map[Reminder]bool{}
	var out []Reminder
	var errs []error
	for _, cal := range calendars {
		if !SupportsEvent(cal.Components) {
			continue
		}
		objects, err := c.QueryCalendar(ctx, cal.Path, &query)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cal.Path, err))
			continue
		}
		for i := range objects {
			occurrences, err := Occurrences(Object{CalendarObject: &objects[i]}, loc, start, end)
			errs = append(errs, err)
			for _, o := range occurrences {
				for _, alarm := range o.Event.Children {
					at, ok := due(alarm, o, loc)
					r := Reminder{At: at, Start: o.Start, UID: o.UID(), Path: o.Path}
					if ok && at.After(from) && !at.After(to) && !seen[r] {
						seen[r] = true
						out = append(out, r)
					}
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

// due is when alarm fires for the occurrence: a trigger relative to its start or, with RELATED=END, its
// end, or an absolute one (RFC 5545 section 3.8.6.3). Only alarms that show or sound fire here.
func due(alarm *ical.Component, o Occurrence, loc *time.Location) (time.Time, bool) {
	if alarm.Name != ical.CompAlarm {
		return time.Time{}, false
	}
	if action, _ := alarm.Props.Text(ical.PropAction); action != "DISPLAY" && action != "AUDIO" {
		return time.Time{}, false
	}
	trigger := alarm.Props.Get(ical.PropTrigger)
	if trigger == nil {
		return time.Time{}, false
	}
	if trigger.ValueType() == ical.ValueDateTime {
		at, err := trigger.DateTime(loc)
		return at, err == nil
	}
	offset, err := trigger.Duration()
	if err != nil {
		return time.Time{}, false
	}
	base := o.Start
	if strings.EqualFold(trigger.Params.Get(ical.ParamRelated), "END") {
		base = o.End
	}
	return base.Add(offset), true
}
