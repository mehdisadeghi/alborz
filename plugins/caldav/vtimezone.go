package alborzcaldav

import (
	"fmt"
	"time"

	"github.com/emersion/go-ical"
)

// A TZID names a timezone that the calendar object itself has to define:
// without the matching VTIMEZONE the reference points at nothing (RFC
// 5545 3.2.19), and what a reader makes of it is then its own business.
// Nextcloud and Migadu recognise the IANA names and let it pass; a
// stricter consumer need not, and an .ics leaving here has no server to
// be lenient on its behalf.
//
// The definition is generated from Go's own zone data rather than kept
// as a table, so it cannot fall behind a zone that changes its rules.
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
				// An object written elsewhere carries its own
				// definitions; only what is missing is added, so a
				// fuller one is never replaced by ours.
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

// localTime is how an observance writes its onset: a wall clock with
// neither a zone nor a Z (RFC 5545 3.6.5).
const localTime = "20060102T150405"

// timezoneComponent describes one zone by its changes in the years
// either side of the given time, as go-ical reads them off Go's zone
// data. nil means the zone is not one this build knows, which leaves
// the object as it was rather than inventing a definition for it.
//
// A recurring event has no last year, and a list of changes does: so
// the newest change in each direction also carries the yearly rule it
// is an instance of. That is an inference from the pattern, not a fact
// of the zone data, which Go does not expose; a zone that did not
// change both ways in the span gets no rule.
func timezoneComponent(tzid string, around time.Time) *ical.Component {
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil
	}
	year := around.In(loc).Year()
	tz := ical.NewTimezone(loc,
		time.Date(year-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC))

	// The first observance is the one already in force when the span
	// opens, not a change seen in it.
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
		// Written, not set as text: a rule's semicolons escaped are a
		// different rule.
		rule := ical.NewProp(ical.PropRecurrenceRule)
		rule.Value = yearlyRule(onset)
		observance.Props.Set(rule)
	}
	return tz
}

// yearlyRule expresses the onset as the rule it is an instance of - the
// last Sunday in March rather than the 29th - so the definition holds
// for every year after the one it was generated in.
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
