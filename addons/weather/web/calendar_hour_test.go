package web

import (
	"testing"
	"time"
)

func TestCalendarHourAtLocalMidnight(t *testing.T) {
	loc := time.FixedZone("+14", 14*60*60)
	now := time.Date(2026, 10, 23, 23, 40, 0, 0, loc)
	today := time.Date(2026, 10, 23, 0, 0, 0, 0, loc)
	for _, test := range []struct {
		name      string
		day, hour time.Time
		want      bool
	}{
		{"current hour ending at midnight", today, today.AddDate(0, 0, 1), true},
		{"current hour ending in UTC", today, today.AddDate(0, 0, 1).UTC(), true},
		{"hour already ended", today, now.Add(-time.Minute), false},
		{"tomorrow after a gap", today, now.Add(2 * time.Hour), false},
		{"future noon on its own day", today.AddDate(0, 0, 1), today.AddDate(0, 0, 1).Add(12 * time.Hour), true},
		{"future noon borrowed from next day", today.AddDate(0, 0, 1), today.AddDate(0, 0, 2).Add(12 * time.Hour), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := calendarHourMatches(now, test.day, test.hour); got != test.want {
				t.Errorf("matched=%v, want %v", got, test.want)
			}
		})
	}
}
