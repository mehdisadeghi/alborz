package alborz_test

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

type sentCalendar struct {
	to      []string
	subject string
	method  string
	event   *ical.Component
	cal     *ical.Calendar
}

func sentCalendars(t *testing.T, raws []string) []sentCalendar {
	t.Helper()
	var out []sentCalendar
	for _, raw := range raws {
		entity, err := message.Read(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("reading a sent message: %v", err)
		}
		header := gomail.Header{Header: entity.Header}
		subject, _ := header.Subject()
		var to []string
		addrs, _ := header.AddressList("To")
		for _, a := range addrs {
			to = append(to, a.Address)
		}
		entity.Walk(func(_ []int, part *message.Entity, err error) error {
			if err != nil {
				return err
			}
			if mediaType, _, _ := part.Header.ContentType(); mediaType != "text/calendar" {
				return nil
			}
			cal, err := ical.NewDecoder(part.Body).Decode()
			if err != nil {
				t.Fatalf("reading a sent calendar: %v", err)
			}
			method, _ := cal.Props.Text(ical.PropMethod)
			out = append(out, sentCalendar{to: to, subject: subject, method: method, event: cal.Events()[0].Component, cal: cal})
			return nil
		})
	}
	return out
}

func appendMessage(t *testing.T, imapAddr, mbox, raw string) string {
	t.Helper()
	ic, err := imapclient.DialInsecure(imapAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	if err := ic.Login(smokeUser, smokePass).Wait(); err != nil {
		t.Fatal(err)
	}
	cmd := ic.Append(mbox, int64(len(raw)), nil)
	cmd.Write([]byte(raw))
	cmd.Close()
	data, err := cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if data.UID == 0 {
		t.Fatal("the server gave the appended message no UID")
	}
	return fmt.Sprint(data.UID)
}

func calendarMail(from, subject, method, body string) string {
	return "From: " + from + "\r\nTo: " + smokeUser + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/calendar; charset=utf-8; method=" + method +
		"\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n")
}

func prop(comp *ical.Component, name string) string {
	if p := comp.Props.Get(name); p != nil {
		return p.Value
	}
	return ""
}

func attendees(comp *ical.Component) []string {
	var out []string
	for _, p := range comp.Props.Values(ical.PropAttendee) {
		out = append(out, strings.TrimPrefix(strings.ToLower(p.Value), "mailto:"))
	}
	return out
}

// A new event is named after the UID it is given.
func eventPath(coll, uid string) (page, object string) {
	object = coll + uid + ".ics"
	return "/calendar/" + url.PathEscape(object), object
}

func created(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the event answered %s", resp.Status)
	}
}

func TestAnOrganizerKeepsAttendeesInformed(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	base := startAlborz(t, startIMAP(t), smtpAddr)
	c := login(t, base)
	u, _ := url.Parse(base)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: "alborz_tz", Value: "Europe/Berlin"}})
	coll := strings.TrimPrefix(keptCalendar(t, base, c, "Plans", "plans"), base)

	form := url.Values{"summary": {"Review"}, "start": {"2026-10-05T10:00"}, "end": {"2026-10-05T11:00"}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"},
		"calendar": {smokeUser + "|" + coll}, "attendees": {"b@example.org\nc@example.org"}}
	created(t, postForm(t, c, base+"/calendar/create", form))
	got := sentCalendars(t, sent.All())
	if len(got) != 1 || got[0].method != "REQUEST" {
		t.Fatalf("creating the event sent %d calendars, want one REQUEST", len(got))
	}
	page, _ := eventPath(coll, prop(got[0].event, ical.PropUID))
	if seq := prop(got[0].event, ical.PropSequence); seq != "0" {
		t.Errorf("the first REQUEST carries SEQUENCE %q, want 0", seq)
	}
	zoned := false
	for _, child := range got[0].cal.Children {
		if child.Name == ical.CompTimezone && prop(child, ical.PropTimezoneID) == "Europe/Berlin" {
			zoned = true
		}
	}
	if !zoned {
		t.Errorf("the REQUEST names Europe/Berlin but carries no VTIMEZONE for it")
	}

	step := func(change url.Values) []sentCalendar {
		t.Helper()
		was := len(sent.All())
		for k, v := range change {
			form[k] = v
		}
		if resp := postForm(t, c, base+page+"/update", form); resp.StatusCode != http.StatusFound {
			t.Fatalf("updating the event answered %s", resp.Status)
		}
		return sentCalendars(t, sent.All()[was:])
	}
	byMethod := func(cals []sentCalendar, method string) *sentCalendar {
		for i := range cals {
			if cals[i].method == method {
				return &cals[i]
			}
		}
		t.Fatalf("no %s among %d calendars sent", method, len(cals))
		return nil
	}

	dropped := step(url.Values{"attendees": {"b@example.org"}})
	cancel := byMethod(dropped, "CANCEL")
	if strings.Join(cancel.to, ",") != "c@example.org" || strings.Join(attendees(cancel.event), ",") != "c@example.org" {
		t.Errorf("the CANCEL went to %v naming %v, want c@example.org alone", cancel.to, attendees(cancel.event))
	}
	if prop(cancel.event, ical.PropOrganizer) != "mailto:"+smokeUser {
		t.Errorf("the CANCEL names organizer %q", prop(cancel.event, ical.PropOrganizer))
	}
	if status := prop(cancel.event, ical.PropStatus); status != "" {
		t.Errorf("uninviting one attendee sent STATUS %q; that cancels the meeting", status)
	}
	if seq := prop(cancel.event, ical.PropSequence); seq != "1" {
		t.Errorf("the CANCEL carries SEQUENCE %q, want 1", seq)
	}
	if req := byMethod(dropped, "REQUEST"); strings.Join(req.to, ",") != "b@example.org" || prop(req.event, ical.PropSequence) != "1" {
		t.Errorf("the update went to %v at SEQUENCE %q, want b@example.org at 1", req.to, prop(req.event, ical.PropSequence))
	}

	if req := byMethod(step(url.Values{"start": {"2026-10-05T12:00"}, "end": {"2026-10-05T13:00"}}), "REQUEST"); prop(req.event, ical.PropSequence) != "2" {
		t.Errorf("moving the meeting sent SEQUENCE %q, want 2", prop(req.event, ical.PropSequence))
	}
	if req := byMethod(step(url.Values{"summary": {"Review, renamed"}}), "REQUEST"); prop(req.event, ical.PropSequence) != "2" {
		t.Errorf("renaming the meeting sent SEQUENCE %q, want it kept at 2", prop(req.event, ical.PropSequence))
	}

	was := len(sent.All())
	if resp := postForm(t, c, base+page+"/delete", nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("deleting the event answered %s", resp.Status)
	}
	deleted := sentCalendars(t, sent.All()[was:])
	if len(deleted) != 1 {
		t.Fatalf("deleting the meeting sent %d calendars, want one CANCEL", len(deleted))
	}
	cancel = &deleted[0]
	if cancel.method != "CANCEL" || strings.Join(cancel.to, ",") != "b@example.org" {
		t.Fatalf("deleting sent %s to %v, want CANCEL to b@example.org", cancel.method, cancel.to)
	}
	if prop(cancel.event, ical.PropStatus) != "CANCELLED" || prop(cancel.event, ical.PropSequence) != "3" ||
		strings.Join(attendees(cancel.event), ",") != "b@example.org" {
		t.Errorf("the CANCEL carries STATUS %q, SEQUENCE %q, attendees %v; want CANCELLED, 3, b@example.org",
			prop(cancel.event, ical.PropStatus), prop(cancel.event, ical.PropSequence), attendees(cancel.event))
	}
}

// RFC 5546 4.4.2, the request for one instance, sent to an identity of the account.
const instanceRequest = `BEGIN:VCALENDAR
METHOD:REQUEST
PRODID:-//Example/ExampleCalendarClient//EN
VERSION:2.0
BEGIN:VEVENT
UID:guid-1@example.com
RECURRENCE-ID:19970701T210000Z
SEQUENCE:1
ORGANIZER:mailto:a@example.com
ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:a@example.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:alias@test.local
ATTENDEE:mailto:c@example.com
DESCRIPTION:IETF-C&S Conference Call
CLASS:PUBLIC
SUMMARY:IETF Calendaring Working Group Meeting
DTSTART:19970703T210000Z
DTEND:19970703T220000Z
LOCATION:Conference Call
DTSTAMP:19970626T093000Z
STATUS:CONFIRMED
END:VEVENT
END:VCALENDAR
`

func TestAnInvitationIsAnsweredAsInvited(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	imapAddr := startIMAP(t)
	base := startAlborz(t, imapAddr, smtpAddr)
	c := login(t, base)
	if resp := postForm(t, c, base+"/settings/account", url.Values{"identities": {"Alias <alias@test.local>"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the identity answered %s", resp.Status)
	}
	uid := appendMessage(t, imapAddr, "INBOX", calendarMail("a@example.com", "Moved", "REQUEST", instanceRequest))

	if page := get(t, c, base+"/message/INBOX/"+uid); !strings.Contains(page, `class="invitation-mine">no answer yet<`) {
		t.Errorf("the page does not show the identity's own answer")
	}
	if resp := postForm(t, c, base+"/message/INBOX/"+uid+"/invite?lang=fa", url.Values{"status": {"ACCEPTED"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("answering answered %s", resp.Status)
	}
	got := sentCalendars(t, []string{sent.Last()})
	if len(got) != 1 || got[0].method != "REPLY" {
		t.Fatalf("answering sent %d calendars, want one REPLY", len(got))
	}
	reply := got[0].event
	if who := attendees(reply); strings.Join(who, ",") != "alias@test.local" {
		t.Errorf("the REPLY answers as %v, want the invited alias@test.local", who)
	}
	if seq, rid := prop(reply, ical.PropSequence), prop(reply, ical.PropRecurrenceID); seq != "1" || rid != "19970701T210000Z" {
		t.Errorf("the REPLY carries SEQUENCE %q RECURRENCE-ID %q, want the request's 1 and 19970701T210000Z", seq, rid)
	}
	if want := "پذیرفته: IETF Calendaring Working Group Meeting"; got[0].subject != want {
		t.Errorf("the REPLY's subject is %q, want %q", got[0].subject, want)
	}
}

func TestAnAnswerIsRecordedOnTheEvent(t *testing.T) {
	smtpAddr, sent := startSMTP(t)
	imapAddr := startIMAP(t)
	base := startAlborz(t, imapAddr, smtpAddr)
	c := login(t, base)
	coll := strings.TrimPrefix(keptCalendar(t, base, c, "Plans", "plans"), base)
	form := url.Values{"summary": {"Review"}, "start": {"2026-10-05T10:00"}, "end": {"2026-10-05T11:00"}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"},
		"calendar": {smokeUser + "|" + coll}, "attendees": {"b@example.org"}}
	created(t, postForm(t, c, base+"/calendar/create", form))
	eventUID := prop(sentCalendars(t, sent.All())[0].event, ical.PropUID)
	_, object := eventPath(coll, eventUID)

	reply := fmt.Sprintf(`BEGIN:VCALENDAR
PRODID:-//Example/ExampleCalendarClient//EN
METHOD:REPLY
VERSION:2.0
BEGIN:VEVENT
ATTENDEE;PARTSTAT=ACCEPTED:mailto:b@example.org
ORGANIZER:mailto:%s
UID:%s
SEQUENCE:0
DTSTAMP:%s
END:VEVENT
END:VCALENDAR
`, smokeUser, eventUID, time.Now().UTC().Format("20060102T150405Z"))
	uid := appendMessage(t, imapAddr, "INBOX", calendarMail("b@example.org", "Accepted: Review", "REPLY", reply))

	page := get(t, c, base+"/message/INBOX/"+uid)
	if !strings.Contains(page, `action="/calendar/from-message`) {
		t.Fatalf("the answer's page offers nothing to record it with")
	}
	if resp := postForm(t, c, base+"/calendar/from-message", url.Values{"mbox": {"INBOX"}, "uid": {uid}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("recording the answer answered %s", resp.Status)
	}
	_, stored := dav(t, http.MethodGet, base+object, nil, "")
	cal, err := ical.NewDecoder(strings.NewReader(stored)).Decode()
	if err != nil {
		t.Fatalf("reading the stored event: %v\n%s", err, stored)
	}
	for _, p := range cal.Events()[0].Props.Values(ical.PropAttendee) {
		if strings.EqualFold(p.Value, "mailto:b@example.org") && p.Params.Get("PARTSTAT") != "ACCEPTED" {
			t.Errorf("b@example.org's answer on the stored event is %q, want ACCEPTED", p.Params.Get("PARTSTAT"))
		}
	}
}

func TestAnIcsAttachmentIsImported(t *testing.T) {
	imapAddr := startIMAP(t)
	base := startAlborz(t, imapAddr)
	c := login(t, base)
	coll := strings.TrimPrefix(keptCalendar(t, base, c, "Plans", "plans"), base)
	raw := "From: eve@example.org\r\nTo: " + smokeUser + "\r\nSubject: The plan\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAttached.\r\n" +
		"--b1\r\nContent-Type: text/calendar; charset=utf-8\r\nContent-Disposition: attachment; filename=\"plan.ics\"\r\n\r\n" +
		event("imported", "Imported") + "--b1--\r\n"
	uid := appendMessage(t, imapAddr, "INBOX", raw)

	page := get(t, c, base+"/message/INBOX/"+uid)
	m := regexp.MustCompile(`<form method="post" action="([^"]*)" class="invitation-actions">\s*<input type="hidden" name="mbox"[^>]*>\s*<input type="hidden" name="uid"[^>]*>\s*<input type="hidden" name="part"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the attachment has no import form")
	}
	form := url.Values{"mbox": {"INBOX"}, "uid": {uid}, "part": {"2"}, "calendar": {smokeUser + "|" + coll}}
	if resp := postForm(t, c, base+html.UnescapeString(m[1]), form); resp.StatusCode != http.StatusFound {
		t.Fatalf("importing the attachment answered %s", resp.Status)
	}
	if resp, body := dav(t, http.MethodGet, base+coll+"imported.ics", nil, ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Imported") {
		t.Errorf("the imported event is not in the calendar: %s", resp.Status)
	}
}

// A server advertising calendar-auto-schedule sends the invitations itself (RFC 6638 2).
func TestAServerThatSchedulesSendsTheInvitations(t *testing.T) {
	personal := davHome + "personal/"
	stub := &davStub{cals: map[string]davCal{personal: {"Personal", "#3366cc"}},
		refuse: map[string]bool{}, keep: map[string]bool{}, gone: map[string]bool{}}
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			w.Header().Set("DAV", "1, 3, calendar-access, calendar-auto-schedule")
			return
		}
		stub.ServeHTTP(w, r)
	}))
	defer dav.Close()
	smtpAddr, sent := startSMTP(t)
	base := startAlborzWith(t, append([]string{
		"test.local=imap+insecure://" + startIMAP(t),
		"test.local=caldav+insecure://" + strings.TrimPrefix(dav.URL, "http://") + "/dav",
	}, smtpUpstreams([]string{smtpAddr})...))
	c := login(t, base)
	kept := strings.TrimPrefix(keptCalendar(t, base, c, "Plans", "plans"), base)

	form := url.Values{"summary": {"Review"}, "start": {"2026-10-05T10:00"}, "end": {"2026-10-05T11:00"}, "start-calendar": {"gregorian"}, "end-calendar": {"gregorian"},
		"calendar": {smokeUser + "|" + personal}, "attendees": {"b@example.org"}}
	created(t, postForm(t, c, base+"/calendar/create", form))
	if got := sent.All(); len(got) != 0 {
		t.Fatalf("saving on a scheduling server sent %d messages, want none", len(got))
	}

	form.Set("calendar", smokeUser+"|"+kept)
	created(t, postForm(t, c, base+"/calendar/create", form))
	if got := sentCalendars(t, sent.All()); len(got) != 1 || got[0].method != "REQUEST" {
		t.Fatalf("saving on a calendar kept here sent %d calendars, want one REQUEST", len(got))
	}
}
