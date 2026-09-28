package calendar

import (
	"time"

	"github.com/emersion/go-ical"
)

// Zones are described a year either side of the time they matter for; their yearly rules cover the rest.
func ensureTimezones(cal *ical.Calendar, around time.Time) {
	year := around.Year()
	cal.AddTimezones(
		time.Date(year-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC))
}

// A scheduling message is zoned for the time of its first event, as a sender that wrote no VTIMEZONE meant it.
func messageZones(msg *ical.Calendar) {
	around := time.Now()
	if events := msg.Events(); len(events) > 0 {
		if start, err := events[0].DateTimeStart(time.UTC); err == nil && !start.IsZero() {
			around = start
		}
	}
	ensureTimezones(msg, around)
}
