package calendar

import (
	"fmt"
	"time"

	"github.com/emersion/go-ical"
)

// Every TZID needs its VTIMEZONE in the object (RFC 5545 3.2.19); ours come from Go's zone data.
func ensureTimezones(cal *ical.Calendar, around time.Time) {
	defined := make(map[string]bool)
	for _, child := range cal.Children {
		if child.Name != ical.CompTimezone {
			continue
		}
		if id, err := child.Props.Text(ical.PropTimezoneID); err == nil {
			defined[id] = true
		}
	}

	for _, child := range cal.Children {
		if child.Name == ical.CompTimezone {
			continue
		}
		for _, props := range child.Props {
			for _, prop := range props {
				id := prop.Params.Get(ical.PropTimezoneID)
				// Only missing definitions are added; one written elsewhere is never replaced.
				if id == "" || defined[id] {
					continue
				}
				if tz := timezoneComponent(id, around); tz != nil {
					cal.Children = append(cal.Children, tz)
					defined[id] = true
				}
			}
		}
	}
}

const localTime = "20060102T150405"

// The newest onset each way gets a yearly rule inferred from its pattern, so the zone outlives the span.
func timezoneComponent(tzid string, around time.Time) *ical.Component {
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil
	}
	year := around.In(loc).Year()
	tz := ical.NewTimezone(loc,
		time.Date(year-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC))

	// The first observance is the one in force when the span opens.
	newest := make(map[string]*ical.Component)
	for _, observance := range tz.Children[1:] {
		newest[observance.Name] = observance
	}
	if len(newest) < 2 {
		return tz
	}
	for _, observance := range newest {
		onset, err := time.Parse(localTime, observance.Props.Get(ical.PropDateTimeStart).Value)
		if err != nil {
			panic(err)
		}
		// Written, not set as text: escaped semicolons are a different rule.
		rule := ical.NewProp(ical.PropRecurrenceRule)
		rule.Value = yearlyRule(onset)
		observance.Props.Set(rule)
	}
	return tz
}

// yearlyRule gives the onset's rule: the last Sunday in March, not the 29th.
func yearlyRule(onset time.Time) string {
	days := [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	day := days[int(onset.Weekday())]

	inMonth := time.Date(onset.Year(), onset.Month()+1, 0, 0, 0, 0, 0, onset.Location()).Day()
	ordinal := (onset.Day()-1)/7 + 1
	if onset.Day()+7 > inMonth {
		ordinal = -1
	}
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", int(onset.Month()), ordinal, day)
}
