package calendar_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
	"git.mehdix.org/alborz/pim/calendar"
	"git.mehdix.org/alborz/pim/store"
)

func TestImportedAlarmTriggersWithoutAFixedHorizon(t *testing.T) {
	start := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, trigger, extra, rule, action string
		at                                 time.Time
		want                               int
	}{
		{"two days before", "-P2D", "", "", "DISPLAY", start.Add(-48 * time.Hour), 1},
		{"audio two days before", "-P2D", "", "", "AUDIO", start.Add(-48 * time.Hour), 1},
		{"repeated", "-P2D", "REPEAT:3\r\nDURATION:PT1H\r\n", "", "DISPLAY", start.Add(-47 * time.Hour), 1},
		{"last repeat", "-P2D", "REPEAT:3\r\nDURATION:PT1H\r\n", "", "DISPLAY", start.Add(-45 * time.Hour), 1},
		{"repeat exhausted", "-P2D", "REPEAT:3\r\nDURATION:PT1H\r\n", "", "DISPLAY", start.Add(-44 * time.Hour), 0},
		{"absolute", ";VALUE=DATE-TIME:20260901T100000Z", "", "", "DISPLAY", start.AddDate(0, 0, -14), 1},
		{"absolute repeated", ";VALUE=DATE-TIME:20260901T100000Z", "REPEAT:3\r\nDURATION:PT1H\r\n", "RRULE:FREQ=DAILY\r\n", "DISPLAY", start.AddDate(0, 0, -14).Add(time.Hour), 1},
		{"end relative", ";RELATED=END:-P2D", "", "", "DISPLAY", start.Add(-47 * time.Hour), 1},
		{"positive offset", "P30D", "", "", "DISPLAY", start.AddDate(0, 0, 30), 1},
		{"recurring", "-P2D", "", "RRULE:FREQ=DAILY;COUNT=3\r\n", "DISPLAY", start.Add(-24 * time.Hour), 1},
		{"excluded", "-P2D", "", "RRULE:FREQ=DAILY;COUNT=3\r\nEXDATE:20260916T100000Z\r\n", "DISPLAY", start.Add(-24 * time.Hour), 0},
		{"email excluded", "-P2D", "", "", "EMAIL", start.Add(-48 * time.Hour), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trigger := "TRIGGER:" + tc.trigger
			if strings.HasPrefix(tc.trigger, ";") {
				trigger = "TRIGGER" + tc.trigger
			}
			raw := strings.Replace(Event("alarm", "Reminder"), "SUMMARY:Reminder\r\n", tc.rule+"SUMMARY:Reminder\r\nBEGIN:VALARM\r\nACTION:"+tc.action+"\r\nDESCRIPTION:Soon\r\n"+trigger+"\r\n"+tc.extra+"END:VALARM\r\n", 1)
			got := remindersFor(t, raw, tc.at.Add(-time.Second), tc.at)
			if len(got) != tc.want {
				t.Fatalf("alarms: %+v, want %d at %s", got, tc.want, tc.at)
			}
			if len(got) > 0 && !got[0].At.Equal(tc.at) {
				t.Errorf("trigger %s, want %s", got[0].At, tc.at)
			}
		})
	}
}

func remindersFor(t *testing.T, raw string, from, to time.Time) []calendar.Reminder {
	t.Helper()
	const coll = DAVHome + "alarms/"
	d := DAV(t, Calendar(coll, "Alarms"), Object(coll+"alarm.ics", raw))
	a, _ := SignedIn(t, User, WithDAV(d), NoCache())
	db, err := bolt.Open(filepath.Join(t.TempDir(), "alarm.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := seal.ParseRoot("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := calendar.New(a.Server.Server, store.New(db, root, ring))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Reminders(context.Background(), a.Server.Logins()[0], time.UTC, from, to)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAbsoluteAlarmIsIndependentOfRecurrenceTraversal(t *testing.T) {
	start := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	at := start.AddDate(0, 0, 2)
	abs := "BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Absolute\r\nTRIGGER;VALUE=DATE-TIME:20260917T100000Z\r\nEND:VALARM\r\n"
	rel := "BEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER:PT30S\r\nEND:VALARM\r\n"
	override := "BEGIN:VEVENT\r\nUID:alarm\r\nDTSTAMP:20260901T080000Z\r\nRECURRENCE-ID:20260915T100000Z\r\nDTSTART:20260915T100000Z\r\nDTEND:20260915T110000Z\r\nSUMMARY:No alarms\r\nEND:VEVENT\r\n"
	for _, tc := range []struct {
		name, rule, alarms, extra string
		relative                  bool
	}{
		{"mixed alarms", "", abs + rel, "", true},
		{"initial excluded", "EXDATE:20260915T100000Z\r\n", abs, "", false},
		{"initial overridden", "", abs, override, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(Event("alarm", "Reminder"), "SUMMARY:Reminder\r\n", "RRULE:FREQ=DAILY\r\n"+tc.rule+"SUMMARY:Reminder\r\n"+tc.alarms, 1)
			raw = strings.Replace(raw, "END:VCALENDAR", tc.extra+"END:VCALENDAR", 1)
			got := remindersFor(t, raw, at.Add(-time.Minute), at.Add(time.Minute))
			want := 1
			if tc.relative {
				want++
			}
			if len(got) != want {
				t.Fatalf("alarms: %+v, want %d", got, want)
			}
			absolute, relative := 0, 0
			for _, r := range got {
				if r.At.Equal(at) {
					absolute++
					if !r.Start.Equal(start) {
						t.Errorf("absolute identity changed: %+v", r)
					}
				} else if r.At.Equal(at.Add(30 * time.Second)) {
					relative++
					if !r.Start.Equal(at) {
						t.Errorf("relative occurrence changed: %+v", r)
					}
				} else {
					t.Errorf("unexpected trigger: %+v", r)
				}
			}
			if absolute != 1 || tc.relative && relative != 1 {
				t.Errorf("absolute=%d relative=%d", absolute, relative)
			}
		})
	}
}

func TestAlarmRepeatsBeyondDurationRange(t *testing.T) {
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	for _, trigger := range []string{"TRIGGER:PT0S", "TRIGGER;VALUE=DATE-TIME:16000915T100000Z"} {
		t.Run(trigger, func(t *testing.T) {
			raw := strings.ReplaceAll(Event("alarm", "Reminder"), "20260915", "16000915")
			raw = strings.Replace(raw, "SUMMARY:Reminder\r\n", "SUMMARY:Reminder\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Ancient\r\n"+trigger+"\r\nREPEAT:200000\r\nDURATION:P1D\r\nEND:VALARM\r\n", 1)
			got := remindersFor(t, raw, at.Add(-time.Second), at)
			if len(got) != 1 || !got[0].At.Equal(at) {
				t.Fatalf("daily repeat in 2026: %+v, want one at %s", got, at)
			}
		})
	}
}

func TestAlarmRepeatArithmeticAcrossCalendarRange(t *testing.T) {
	for _, tc := range []struct {
		name, start, interval string
		at                    time.Time
	}{
		{"last supported day", "00010101", "P1D", time.Date(9999, 12, 31, 10, 0, 0, 0, time.UTC)},
		{"large interval and count", "16000915", "P100000D", time.Date(1600, 9, 15, 10, 0, 0, 0, time.UTC).AddDate(0, 0, 2000000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.ReplaceAll(Event("alarm", "Reminder"), "20260915", tc.start)
			raw = strings.Replace(raw, "SUMMARY:Reminder\r\n", "SUMMARY:Reminder\r\nBEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER:PT0S\r\nREPEAT:2147483647\r\nDURATION:"+tc.interval+"\r\nEND:VALARM\r\n", 1)
			got := remindersFor(t, raw, tc.at.Add(-time.Second), tc.at)
			if len(got) != 1 || !got[0].At.Equal(tc.at) {
				t.Fatalf("bounded repeat arithmetic: %+v, want one at %s", got, tc.at)
			}
		})
	}
}
