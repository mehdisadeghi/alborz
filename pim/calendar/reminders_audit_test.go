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
	const coll = DAVHome + "alarms/"
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
			got, err := p.Reminders(context.Background(), a.Server.Logins()[0], time.UTC, tc.at.Add(-time.Second), tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("alarms: %+v, want %d at %s", got, tc.want, tc.at)
			}
			if len(got) > 0 && !got[0].At.Equal(tc.at) {
				t.Errorf("trigger %s, want %s", got[0].At, tc.at)
			}
		})
	}
}
