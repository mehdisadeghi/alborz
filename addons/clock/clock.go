// Package clock tells the time in other zones: a contact's, from the card's TZ (RFC 6350 6.5.1), and
// the zones the reader keeps a clock for.
package clock

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// utcOffset is TZ written as an offset, as vCard 3 and VALUE=utc-offset do: +0200, -05:00, +05.
var utcOffset = regexp.MustCompile(`^([+-])(\d{2}):?(\d{2})?$`)

// Zone reads a TZ value: an IANA name or a UTC offset; false for neither.
func Zone(value string) (*time.Location, bool) {
	value = strings.TrimSpace(value)
	if m := utcOffset.FindStringSubmatch(value); m != nil {
		h, _ := strconv.Atoi(m[2])
		mins, _ := strconv.Atoi(m[3])
		secs := (h*60 + mins) * 60
		if m[1] == "-" {
			secs = -secs
		}
		return time.FixedZone(value, secs), true
	}
	return Named(value)
}

// Named loads an IANA zone; false for an unknown name, an empty one or the server's own.
func Named(name string) (*time.Location, bool) {
	if name == "" || name == "Local" {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	return loc, err == nil
}

// City names a zone by its last part: "America/Los_Angeles" is "Los Angeles".
func City(zone string) string {
	return strings.ReplaceAll(zone[strings.LastIndex(zone, "/")+1:], "_", " ")
}

// Offset is t's distance from UTC: its sign, hours and minutes.
func Offset(t time.Time) (sign string, hours, minutes int) {
	_, secs := t.Zone()
	sign = "+"
	if secs < 0 {
		sign, secs = "-", -secs
	}
	return sign, secs / 3600, secs % 3600 / 60
}
