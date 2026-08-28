package shcal

import (
	"testing"
	"time"
)

func TestFromTime(t *testing.T) {
	for _, c := range []struct {
		g    string
		want Date
	}{
		{"2026-08-28", Date{1405, Shahrivar, 6}},
		{"2026-03-21", Date{1405, Farvardin, 1}},
		{"2026-03-20", Date{1404, Esfand, 29}},
		{"2024-03-20", Date{1403, Farvardin, 1}},
		{"2000-01-01", Date{1378, Dey, 11}},
		{"1979-02-11", Date{1357, Bahman, 22}},
	} {
		when, err := time.Parse("2006-01-02", c.g)
		if err != nil {
			t.Fatal(err)
		}
		if got := FromTime(when); got != c.want {
			t.Errorf("FromTime(%s) = %v, want %v", c.g, got, c.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	day := time.Date(1990, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 40000; i++ {
		d := FromTime(day)
		if !d.IsValid() {
			t.Fatalf("%s converted to invalid %v", day.Format("2006-01-02"), d)
		}
		if back := d.Time(time.UTC); !back.Equal(day) {
			t.Fatalf("%s round-tripped to %s", day.Format("2006-01-02"), back.Format("2006-01-02"))
		}
		day = day.AddDate(0, 0, 1)
	}
}

func TestDaysInMonth(t *testing.T) {
	if got := DaysInMonth(1403, Esfand); got != 30 {
		t.Errorf("Esfand 1403 = %d days, want 30 (leap)", got)
	}
	if got := DaysInMonth(1404, Esfand); got != 29 {
		t.Errorf("Esfand 1404 = %d days, want 29", got)
	}
}
