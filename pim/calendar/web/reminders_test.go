package web_test

import (
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func alarmed(uid, rule, action string, start time.Time) string {
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260901T080000Z\r\nDTSTART:" + stamp(start) + "\r\nDTEND:" + stamp(start.Add(time.Hour)) + rule +
		"\r\nSUMMARY:Budget review\r\nBEGIN:VALARM\r\nACTION:" + action + "\r\nDESCRIPTION:Soon\r\nTRIGGER:-PT2S\r\nEND:VALARM" +
		"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// A display alarm of today's occurrence of a daily series is pushed once, naming only its time, though
// two logins of the account find it; an e-mail alarm is not alborz's to fire.
func TestEventReminderPushesItsOccurrence(t *testing.T) {
	push := PushServer(t)
	a, c := SignedIn(t, User, Guarded(push.Client().Transport), ReminderTick(200*time.Millisecond))
	endpoint := push.TurnOn(t, c)
	// A second browser's login of the account finds the same alarm in each round.
	a.SignIn(t, User)
	cal := c.CreateCalendar(t, "Personal", "")

	start := time.Now().Add(4 * time.Second).Truncate(time.Second)
	a.Put(t, cal+"daily.ics", "text/calendar", alarmed("daily", "\r\nRRULE:FREQ=DAILY;COUNT=3", "DISPLAY", start.AddDate(0, 0, -1)))
	a.Put(t, cal+"mailed.ics", "text/calendar", alarmed("mailed", "", "EMAIL", start))

	want := T(t, "push.event", start.UTC().Format("15:04"))
	got := push.Await(t, endpoint, want)
	if got.Title != want || got.Body != User {
		t.Fatalf("the push says %q / %q, want %q / %q", got.Title, got.Body, want, User)
	}
	if strings.Contains(got.Title+got.Body, "Budget") {
		t.Fatalf("the push carries the event's title: %+v", got)
	}
	if page := c.Follow(t, got.URL); !strings.Contains(page, "Budget review") {
		t.Fatalf("a click on the push opens %q, which is not the event", got.URL)
	}
	if got.Header.Get("Urgency") != "high" {
		t.Fatalf("urgency %q", got.Header.Get("Urgency"))
	}
	time.Sleep(time.Second)
	if n := len(push.Received()); n != 1 {
		t.Fatalf("%d pushes, want the one reminder: %+v", n, push.Received())
	}
}
