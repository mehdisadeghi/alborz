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

// timezoneComponent describes one zone as the two observances in force
// around the given time, each recurring yearly. nil means the zone is
// not one this build knows, which leaves the object as it was rather
// than inventing a definition for it.
func timezoneComponent(tzid string, around time.Time) *ical.Component {
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil
	}

	tz := ical.NewComponent(ical.CompTimezone)
	tz.Props.SetText(ical.PropTimezoneID, tzid)

	changes := transitions(loc, around)
	if len(changes) == 0 {
		// A zone that does not move: one observance, standing since
		// before anything this program will be asked to write.
		name, offset := around.In(loc).Zone()
		tz.Children = append(tz.Children,
			observance(ical.CompTimezoneStandard, time.Date(1970, 1, 1, 0, 0, 0, 0, loc), offset, offset, name, ""))
		return tz
	}

	for _, at := range changes {
		before := at.Add(-time.Second).In(loc)
		after := at.In(loc)
		_, from := before.Zone()
		name, to := after.Zone()

		kind := ical.CompTimezoneStandard
		if after.IsDST() {
			kind = ical.CompTimezoneDaylight
		}
		// The onset is written in the offset it replaces, which is what
		// makes a local time unambiguous at the moment it changes.
		onset := at.In(time.FixedZone("", from))
		tz.Children = append(tz.Children,
			observance(kind, onset, from, to, name, yearlyRule(onset)))
	}
	return tz
}

// observance is one STANDARD or DAYLIGHT block. DTSTART is a local time
// with neither a zone nor a Z, so it is written rather than set: what
// the property means here is a wall clock, not an instant.
func observance(kind string, onset time.Time, from, to int, name, rule string) *ical.Component {
	c := ical.NewComponent(kind)
	setRaw(c, ical.PropDateTimeStart, onset.Format("20060102T150405"))
	setRaw(c, ical.PropTimezoneOffsetFrom, utcOffset(from))
	setRaw(c, ical.PropTimezoneOffsetTo, utcOffset(to))
	if name != "" {
		c.Props.SetText(ical.PropTimezoneName, name)
	}
	if rule != "" {
		setRaw(c, ical.PropRecurrenceRule, rule)
	}
	return c
}

// setRaw writes a value that is not text - a local time, a UTC offset, a
// recurrence rule - so that it is neither escaped as text nor labelled
// VALUE=TEXT. A rule written through SetText comes out with its
// semicolons backslashed, which is a different rule.
func setRaw(c *ical.Component, name, value string) {
	prop := ical.NewProp(name)
	prop.Value = value
	c.Props.Set(prop)
}

// transitions finds when the zone's offset last changed in each
// direction, looking over the years either side of the given time. Go
// holds the rules but will not name them, so they are read back off the
// clock: a day at a time until the offset differs, then bisected.
func transitions(loc *time.Location, around time.Time) []time.Time {
	year := around.In(loc).Year()
	at := time.Date(year-1, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)

	// One per direction: the DST rules in use everywhere change twice a
	// year, and a yearly rule covers every repetition of each.
	latest := make(map[bool]time.Time)
	for at.Before(end) {
		next := at.AddDate(0, 0, 1)
		_, a := at.In(loc).Zone()
		_, b := next.In(loc).Zone()
		if a != b {
			exact := bisect(loc, at, next)
			latest[exact.In(loc).IsDST()] = exact
		}
		at = next
	}

	var out []time.Time
	for _, dst := range []bool{true, false} {
		if t, ok := latest[dst]; ok {
			out = append(out, t)
		}
	}
	return out
}

// bisect narrows a day known to contain a change down to the second it
// happens on.
func bisect(loc *time.Location, lo, hi time.Time) time.Time {
	_, before := lo.In(loc).Zone()
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if _, o := mid.In(loc).Zone(); o == before {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
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

// utcOffset writes seconds east of UTC as iCalendar's signed hhmm.
func utcOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, seconds%3600/60)
}
