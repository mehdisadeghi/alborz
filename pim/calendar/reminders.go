package calendar

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-ical"
)

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
	// Trigger offsets have no fixed horizon. Read definitions first, then expand
	// only the occurrence windows that can produce an alarm in this round.
	query := SeriesQuery()
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
			obj := Object{CalendarObject: &objects[i]}
			if obj.Data == nil {
				continue
			}
			for _, event := range obj.Data.Events() {
				for _, alarm := range event.Children {
					start, end, ok := alarmWindow(alarm, event, loc, from, to)
					if !ok {
						continue
					}
					occurrences, err := Occurrences(obj, loc, start, end)
					errs = append(errs, err)
					for _, o := range occurrences {
						for _, alarm := range o.Event.Children {
							for _, at := range alarmTimes(alarm, o, loc, from, to) {
								r := Reminder{At: at, Start: o.Start, UID: o.UID(), Path: o.Path}
								if !seen[r] {
									seen[r] = true
									out = append(out, r)
								}
							}
						}
					}
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

func repetitions(alarm *ical.Component) (int, time.Duration) {
	repeat := alarm.Props.Get(ical.PropRepeat)
	if repeat == nil {
		return 0, 0
	}
	n, err := strconv.Atoi(repeat.Value)
	if err != nil || n <= 0 {
		return 0, 0
	}
	p := alarm.Props.Get(ical.PropDuration)
	if p == nil {
		return 0, 0
	}
	d, err := p.Duration()
	if err != nil || d <= 0 {
		return 0, 0
	}
	return n, d
}

func alarmWindow(alarm *ical.Component, event ical.Event, loc *time.Location, from, to time.Time) (time.Time, time.Time, bool) {
	if _, ok := due(alarm, Occurrence{}, loc); !ok {
		return time.Time{}, time.Time{}, false
	}
	trigger := alarm.Props.Get(ical.PropTrigger)
	if trigger.ValueType() == ical.ValueDateTime {
		start, err := event.DateTimeStart(loc)
		return start, start.Add(time.Nanosecond), err == nil
	}
	offset, _ := trigger.Duration()
	n, interval := repetitions(alarm)
	// Between includes events overlapping the window, so RELATED=END also
	// finds occurrences starting before it, regardless of event duration.
	start := from.Add(-offset)
	if interval > 0 && int64(n) > int64((1<<63-1)/interval) {
		start = time.Time{}
	} else {
		start = start.Add(-time.Duration(n) * interval)
	}
	return start, to.Add(-offset).Add(time.Nanosecond), true
}

func alarmTimes(alarm *ical.Component, o Occurrence, loc *time.Location, from, to time.Time) []time.Time {
	at, ok := due(alarm, o, loc)
	if !ok {
		return nil
	}
	n, interval := repetitions(alarm)
	first := 0
	if interval > 0 && !at.After(from) {
		first = int(from.Sub(at)/interval) + 1
	}
	var times []time.Time
	for i := first; i <= n; i++ {
		due := at.Add(time.Duration(i) * interval)
		if due.After(to) {
			break
		}
		if due.After(from) {
			times = append(times, due)
		}
	}
	return times
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
