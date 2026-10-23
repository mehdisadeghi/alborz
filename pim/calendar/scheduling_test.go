package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alborzmail/go-itip"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
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

func TestImportLookupRequiresExactUID(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(fmt.Sprint(exact), func(t *testing.T) {
			var writes []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					if r.URL.Path == "/cal/event-123.ics" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					writes = append(writes, r.URL.Path)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusMultiStatus)
				fmt.Fprint(w, `<multistatus xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`)
				for _, uid := range []string{"prefix-event-123", "event-123"} {
					if uid == "event-123" && !exact {
						continue
					}
					raw := strings.Replace(groupRequest, "calsrv.example.com-873970198738777@example.com", uid, 1)
					fmt.Fprintf(w, `<response><href>/cal/%s-other.ics</href><propstat><prop><C:calendar-data><![CDATA[%s]]></C:calendar-data></prop><status>HTTP/1.1 200 OK</status></propstat></response>`, uid, raw)
				}
				fmt.Fprint(w, `</multistatus>`)
			}))
			defer server.Close()
			client, err := caldav.NewClient(server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			raw := strings.Replace(groupRequest, "calsrv.example.com-873970198738777@example.com", "event-123", 1)
			n, err := Import(context.Background(), client, "/cal/", []byte(raw))
			if exact {
				if err != nil || n != 1 || len(writes) != 1 || writes[0] != "/cal/event-123-other.ics" {
					t.Fatalf("exact fallback: %v %d %v", writes, n, err)
				}
			} else if err == nil || n != 0 || len(writes) != 0 {
				t.Fatalf("substring fallback overwrote event: %v %d %v", writes, n, err)
			}
		})
	}
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
