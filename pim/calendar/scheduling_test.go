package calendar

import (
	"strings"
	"testing"

	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
)

// RFC 5546 4.2.1, DTEND's stray digit removed.
const groupRequest = `BEGIN:VCALENDAR
PRODID:-//Example/ExampleCalendarClient//EN
METHOD:REQUEST
VERSION:2.0
BEGIN:VEVENT
ORGANIZER:mailto:a@example.com
ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED;CN=A:mailto:a@example.com
ATTENDEE;RSVP=TRUE;CUTYPE=INDIVIDUAL;CN=B:mailto:b@example.com
ATTENDEE;RSVP=TRUE;CUTYPE=INDIVIDUAL;CN=C:mailto:c@example.com
ATTENDEE;RSVP=TRUE;CUTYPE=INDIVIDUAL;CN=Hal:mailto:d@example.com
ATTENDEE;RSVP=FALSE;CUTYPE=ROOM:conf_big@example.com
ATTENDEE;ROLE=NON-PARTICIPANT;RSVP=FALSE:mailto:e@example.com
DTSTAMP:19970611T190000Z
DTSTART:19970701T200000Z
DTEND:19970701T210000Z
SUMMARY:Conference
UID:calsrv.example.com-873970198738777@example.com
SEQUENCE:0
STATUS:CONFIRMED
END:VEVENT
END:VCALENDAR
`

func parse(t *testing.T, text string) *ical.Calendar {
	t.Helper()
	cal, err := itip.Parse([]byte(strings.ReplaceAll(text, "\n", "\r\n")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cal
}

func TestSetSchedulingKeepsAnswers(t *testing.T) {
	event := &ical.Event{Component: itip.Series(parse(t, groupRequest))}
	SetScheduling(event, "a@example.com", []string{"a@example.com", "b@example.com", "new@example.com"})
	got := map[string]string{}
	for _, prop := range event.Props[ical.PropAttendee] {
		got[itip.Address(&prop)] = prop.Params.Get("PARTSTAT") + "/" + prop.Params.Get("CN")
	}
	want := map[string]string{"a@example.com": "ACCEPTED/A", "b@example.com": "/B", "new@example.com": "NEEDS-ACTION/"}
	for addr, w := range want {
		if got[addr] != w {
			t.Errorf("%s: %q, want %q", addr, got[addr], w)
		}
	}
	answered := &ical.Event{Component: itip.Series(parse(t, strings.Replace(groupRequest, "CN=B:", "PARTSTAT=DECLINED;CN=B:", 1)))}
	SetScheduling(answered, "a@example.com", []string{"b@example.com"})
	if p := answered.Props.Get(ical.PropAttendee); p.Params.Get("PARTSTAT") != PartDeclined {
		t.Errorf("saving reset b's answer to %q", p.Params.Get("PARTSTAT"))
	}
}
