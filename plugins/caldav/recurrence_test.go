package alborzcaldav

import (
	"slices"
	"testing"
	"time"
)

// Expected days follow RFC 7529 section 4.1 (SKIP) and 4.2 (COUNT), counted by hand from Nowruz 1404 = 2025-03-21,
// 1405 = 2026-03-21, 1406 = 2027-03-21, 1407 = 2028-03-20, 1408 = 2029-03-20, 1409 = 2030-03-21 (jalaali-js), 1403 and 1408 leap.
func TestRSCALE(t *testing.T) {
	tehran, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		t.Fatal(err)
	}
	at9 := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 0, 0, 0, time.UTC) }
	midnight := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	inTehran := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 0, 0, 0, tehran) }
	shahrivar31 := at9(2025, 9, 22)
	esfand30 := at9(2025, 3, 20)
	jan31 := at9(2025, 1, 31)
	// Well past every last instance, so COUNT, UNTIL or SKIP must stop the rule.
	far := at9(2032, 1, 1)
	cases := []struct {
		name  string
		rule  string
		first time.Time
		to    time.Time
		want  []time.Time
	}{
		{"monthly 31st omits the 30-day months", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=OMIT", shahrivar31, at9(2026, 6, 1),
			[]time.Time{shahrivar31, at9(2026, 4, 20), at9(2026, 5, 21)}},
		{"monthly 31st backward to the 30th", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=BACKWARD;COUNT=3", shahrivar31, far,
			[]time.Time{shahrivar31, at9(2025, 10, 22), at9(2025, 11, 21)}},
		{"monthly 31st forward to the 1st", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=FORWARD;COUNT=3", shahrivar31, far,
			[]time.Time{shahrivar31, at9(2025, 10, 23), at9(2025, 11, 22)}},
		{"omitted dates do not count", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=OMIT;COUNT=3", shahrivar31, far,
			[]time.Time{shahrivar31, at9(2026, 4, 20), at9(2026, 5, 21)}},
		{"yearly 30 Esfand omitted in common years", "RSCALE=PERSIAN;FREQ=YEARLY;COUNT=2", esfand30, far,
			[]time.Time{esfand30, at9(2030, 3, 20)}},
		{"yearly 30 Esfand backward to the 29th", "RSCALE=PERSIAN;FREQ=YEARLY;SKIP=BACKWARD;COUNT=6", esfand30, far,
			[]time.Time{esfand30, at9(2026, 3, 20), at9(2027, 3, 20), at9(2028, 3, 19), at9(2029, 3, 19), at9(2030, 3, 20)}},
		{"yearly 30 Esfand forward to Nowruz", "RSCALE=PERSIAN;FREQ=YEARLY;SKIP=FORWARD;COUNT=3", esfand30, far,
			[]time.Time{esfand30, at9(2026, 3, 21), at9(2027, 3, 21)}},
		{"UNTIL as a date includes that day", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=BACKWARD;UNTIL=20251022", midnight(2025, 9, 22), far,
			[]time.Time{midnight(2025, 9, 22), midnight(2025, 10, 22)}},
		{"UNTIL in UTC includes the instant", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=BACKWARD;UNTIL=20251022T053000Z", inTehran(2025, 9, 22), far,
			[]time.Time{inTehran(2025, 9, 22), inTehran(2025, 10, 22)}},
		{"UNTIL in UTC a second early", "RSCALE=PERSIAN;FREQ=MONTHLY;SKIP=BACKWARD;UNTIL=20251022T052959Z", inTehran(2025, 9, 22), far,
			[]time.Time{inTehran(2025, 9, 22)}},
		{"unreadable part yields the first", "RSCALE=PERSIAN;FREQ=MONTHLY;BYMONTHDAY=-1", shahrivar31, far,
			[]time.Time{shahrivar31}},
		{"unknown scale yields the first", "RSCALE=HEBREW;FREQ=MONTHLY", shahrivar31, far,
			[]time.Time{shahrivar31}},
		{"gregorian Jan 31 backward", "RSCALE=GREGORIAN;FREQ=MONTHLY;SKIP=BACKWARD;COUNT=4", jan31, far,
			[]time.Time{jan31, at9(2025, 2, 28), at9(2025, 3, 31), at9(2025, 4, 30)}},
		{"gregorian Jan 31 omit", "RSCALE=GREGORIAN;FREQ=MONTHLY;COUNT=3", jan31, far,
			[]time.Time{jan31, at9(2025, 3, 31), at9(2025, 5, 31)}},
		{"gregorian Jan 31 forward", "RSCALE=GREGORIAN;FREQ=MONTHLY;SKIP=FORWARD;COUNT=3", jan31, far,
			[]time.Time{jan31, at9(2025, 3, 1), at9(2025, 3, 31)}},
	}
	for _, c := range cases {
		got := expandRule(c.rule, c.first, c.first, c.to)
		if !slices.EqualFunc(got, c.want, time.Time.Equal) {
			t.Errorf("%s: got %v; want %v", c.name, got, c.want)
		}
	}
}
