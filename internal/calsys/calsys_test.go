package calsys

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// Nowruz and leap years from jalaali-js 2.0.1 (toGregorian, isLeapJalaaliYear); 1395-1410 also agree with ICU's Persian calendar.
var nowruzes = []struct {
	year   int
	nowruz time.Time
	leap   bool
}{
	{-61, day(560, 3, 20), true},
	{-60, day(561, 3, 21), false},
	{1180, day(1801, 3, 21), false},
	{1181, day(1802, 3, 21), true},
	{1205, day(1826, 3, 21), true},
	{1206, day(1827, 3, 22), false},
	{1209, day(1830, 3, 21), false},
	{1210, day(1831, 3, 21), true},
	{1211, day(1832, 3, 21), false},
	{1395, day(2016, 3, 20), true},
	{1396, day(2017, 3, 21), false},
	{1397, day(2018, 3, 21), false},
	{1398, day(2019, 3, 21), false},
	{1399, day(2020, 3, 20), true},
	{1400, day(2021, 3, 21), false},
	{1401, day(2022, 3, 21), false},
	{1402, day(2023, 3, 21), false},
	{1403, day(2024, 3, 20), true},
	{1404, day(2025, 3, 21), false},
	{1405, day(2026, 3, 21), false},
	{1406, day(2027, 3, 21), false},
	{1407, day(2028, 3, 20), false},
	{1408, day(2029, 3, 20), true},
	{1409, day(2030, 3, 21), false},
	{1410, day(2031, 3, 21), false},
	{1630, day(2251, 3, 21), true},
	{1631, day(2252, 3, 21), false},
	{1634, day(2255, 3, 21), false},
	{1635, day(2256, 3, 20), true},
	{2055, day(2676, 3, 20), true},
	{2056, day(2677, 3, 21), false},
	{2059, day(2680, 3, 20), false},
	{2060, day(2681, 3, 20), true},
	{3177, day(3798, 3, 20), false},
}

func TestJalaliNowruz(t *testing.T) {
	cal := solarHijri
	for _, c := range nowruzes {
		if got := cal.Time(c.year, 1, 1, time.UTC); !got.Equal(c.nowruz) {
			t.Errorf("Time(%d, 1, 1) = %s; want %s", c.year, got.Format(time.DateOnly), c.nowruz.Format(time.DateOnly))
		}
		if y, m, d := cal.Date(c.nowruz); y != c.year || m != 1 || d != 1 {
			t.Errorf("Date(%s) = %d/%d/%d; want %d/1/1", c.nowruz.Format(time.DateOnly), y, m, d, c.year)
		}
	}
}

func TestJalaliEsfandLength(t *testing.T) {
	cal := solarHijri
	for _, c := range nowruzes {
		// Esfand of the last year runs into a Gregorian year whose Nowruz is outside the reckoning.
		if c.year == 3177 {
			continue
		}
		want := 29
		if c.leap {
			want = 30
		}
		if got := cal.DaysInMonth(cal.Time(c.year, 12, 1, time.UTC)); got != want {
			t.Errorf("Esfand %d has %d days; want %d", c.year, got, want)
		}
	}
}

// Dates from jalaali-js 2.0.1 (toJalaali); the 1402-1404 rows also agree with ICU's Persian calendar.
func TestJalaliDate(t *testing.T) {
	cases := []struct {
		t       time.Time
		y, m, d int
	}{
		{day(2024, 3, 19), 1402, 12, 29},
		{day(2025, 3, 20), 1403, 12, 30},
		{day(2025, 9, 22), 1404, 6, 31},
		{day(2025, 9, 23), 1404, 7, 1},
		{day(2026, 3, 20), 1404, 12, 29},
		{day(2026, 9, 28), 1405, 7, 6},
		{day(2030, 3, 20), 1408, 12, 30},
		{day(1832, 3, 20), 1210, 12, 30},
		{day(2257, 3, 19), 1635, 12, 29},
		{day(2000, 1, 1), 1378, 10, 11},
		{day(1979, 2, 11), 1357, 11, 22},
		{day(1900, 1, 1), 1278, 10, 11},
		{day(3798, 12, 31), 3177, 10, 11},
	}
	cal := solarHijri
	for _, c := range cases {
		if y, m, d := cal.Date(c.t); y != c.y || m != c.m || d != c.d {
			t.Errorf("Date(%s) = %d/%d/%d; want %d/%d/%d", c.t.Format(time.DateOnly), y, m, d, c.y, c.m, c.d)
		}
		if got := cal.Time(c.y, c.m, c.d, time.UTC); !got.Equal(c.t) {
			t.Errorf("Time(%d/%d/%d) = %s; want %s", c.y, c.m, c.d, got.Format(time.DateOnly), c.t.Format(time.DateOnly))
		}
	}
}

func TestJalaliRoundTrip(t *testing.T) {
	cal := solarHijri
	spans := [][2]time.Time{
		{day(1826, 1, 1), day(1833, 1, 1)},
		{day(2016, 1, 1), day(2032, 1, 1)},
		{day(2250, 1, 1), day(2258, 1, 1)},
		{day(2675, 1, 1), day(2683, 1, 1)},
	}
	n := 0
	for _, s := range spans {
		for d := s[0]; d.Before(s[1]); d = d.AddDate(0, 0, 1) {
			y, m, dd := cal.Date(d)
			if got := cal.Time(y, m, dd, time.UTC); !got.Equal(d) {
				t.Fatalf("Time(Date(%s)) = %s via %d/%d/%d", d.Format(time.DateOnly), got.Format(time.DateOnly), y, m, dd)
			}
			n++
		}
	}
	if n < 10000 {
		t.Fatalf("walked only %d days", n)
	}
}

func TestJalaliOutsideReckoningRefused(t *testing.T) {
	for _, y := range []int{-62, 3178} {
		if got, err := solarHijri.Day(y, 1, 1, time.UTC); err == nil {
			t.Errorf("Day(%d, 1, 1) = %s; want it refused", y, got.Format(time.DateOnly))
		}
	}
}
