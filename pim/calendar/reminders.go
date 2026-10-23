package calendar

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	add := func(alarm *ical.Component, o Occurrence) {
		for _, at := range alarmTimes(alarm, o, loc, from, to) {
			r := Reminder{At: at, Start: o.Start, UID: o.UID(), Path: o.Path}
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
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
					if absoluteTrigger(alarm) {
						// Absolute triggers belong to the definition, independently of
						// recurrence exclusions or replacements. Anchor their identity
						// to that definition, never to an expanded occurrence.
						start, err := event.DateTimeStart(loc)
						status, _ := event.Props.Text(ical.PropStatus)
						if err == nil && !strings.EqualFold(status, "CANCELLED") {
							add(alarm, Occurrence{Object: obj, Event: &event, Start: start})
						}
						continue
					}
					start, end, ok := alarmWindow(alarm, loc, from, to)
					if !ok {
						continue
					}
					occurrences, err := Occurrences(obj, loc, start, end)
					errs = append(errs, err)
					for _, o := range occurrences {
						for _, alarm := range o.Event.Children {
							if !absoluteTrigger(alarm) {
								add(alarm, o)
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
	n, err := strconv.ParseInt(repeat.Value, 10, 32)
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
	return int(n), d
}

func absoluteTrigger(alarm *ical.Component) bool {
	trigger := alarm.Props.Get(ical.PropTrigger)
	return trigger != nil && trigger.ValueType() == ical.ValueDateTime
}

func alarmWindow(alarm *ical.Component, loc *time.Location, from, to time.Time) (time.Time, time.Time, bool) {
	if _, ok := due(alarm, Occurrence{}, loc); !ok {
		return time.Time{}, time.Time{}, false
	}
	trigger := alarm.Props.Get(ical.PropTrigger)
	offset, _ := trigger.Duration()
	n, interval := repetitions(alarm)
	// Between includes events overlapping the window, so RELATED=END also
	// finds occurrences starting before it, regardless of event duration.
	start := repeatTime(from.Add(-offset), n, -interval)
	return start, to.Add(-offset).Add(time.Nanosecond), true
}

// RFC 5545 durations count whole seconds. Check the product before adding in
// Unix seconds, which cover the entire calendar range without Duration's
// approximately 292-year limit. The sentinels bound expansion, not valid alarms.
func repeatTime(base time.Time, n int, interval time.Duration) time.Time {
	if n == 0 || interval == 0 {
		return base
	}
	first := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	after := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	step, count := int64(interval/time.Second), int64(n)
	if step > 0 && count > (after.Unix()-base.Unix())/step {
		return after
	}
	if step < 0 && count > (base.Unix()-first.Unix())/-step {
		return first
	}
	return time.Unix(base.Unix()+count*step, int64(base.Nanosecond())).In(base.Location())
}

func alarmTimes(alarm *ical.Component, o Occurrence, loc *time.Location, from, to time.Time) []time.Time {
	at, ok := due(alarm, o, loc)
	if !ok {
		return nil
	}
	n, interval := repetitions(alarm)
	first := sort.Search(n+1, func(i int) bool { return repeatTime(at, i, interval).After(from) })
	var times []time.Time
	for i := first; i <= n; i++ {
		due := repeatTime(at, i, interval)
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
