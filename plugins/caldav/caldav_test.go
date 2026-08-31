package alborzcaldav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

func TestOccurrencesLeaveOverriddenSlotsToTheOverride(t *testing.T) {
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:test
BEGIN:VEVENT
UID:standup
DTSTART:20260901T090000Z
DTEND:20260901T100000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Standup
END:VEVENT
BEGIN:VEVENT
UID:standup
RECURRENCE-ID:20260902T090000Z
DTSTART:20260902T140000Z
DTEND:20260902T150000Z
SUMMARY:Standup, moved
END:VEVENT
END:VCALENDAR
`, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	obj := CalendarObject{CalendarObject: &caldav.CalendarObject{Data: cal}}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	got := occurrences(obj, time.UTC, from, from.AddDate(0, 0, 7))

	var starts []string
	for _, o := range got {
		starts = append(starts, o.Start.UTC().Format("02T15:04"))
	}
	want := "01T09:00 02T09:00 03T09:00 02T14:00"
	if len(got) != 3 {
		t.Fatalf("the moved instance was drawn twice: %v", starts)
	}
	joined := strings.Join(starts, " ")
	if strings.Contains(joined, "02T09:00") || !strings.Contains(joined, "02T14:00") {
		t.Errorf("wrong slots drawn: %s (the rule alone would give %s)", joined, want)
	}
}
