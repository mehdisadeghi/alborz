// Package shcal converts between Gregorian time and the Solar Hijri
// (Jalali) calendar. It is shaped like the standard library's time
// package: a Date value, a Month with names, and the arithmetic a view
// needs. The algorithm is the 33-year-cycle one with the historical
// break table, exact over the range any mail client can show.
package shcal

import "time"

// Month is a Solar Hijri month, Farvardin through Esfand.
type Month int

const (
	Farvardin Month = 1 + iota
	Ordibehesht
	Khordad
	Tir
	Mordad
	Shahrivar
	Mehr
	Aban
	Azar
	Dey
	Bahman
	Esfand
)

var monthNames = [...]string{
	"Farvardin", "Ordibehesht", "Khordad", "Tir", "Mordad", "Shahrivar",
	"Mehr", "Aban", "Azar", "Dey", "Bahman", "Esfand",
}

func (m Month) String() string {
	if m < Farvardin || m > Esfand {
		return "%!Month(" + itoa(int(m)) + ")"
	}
	return monthNames[m-1]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Date is a day in the Solar Hijri calendar.
type Date struct {
	Year  int
	Month Month
	Day   int
}

// FromTime converts an instant to the Solar Hijri date of its own
// location.
func FromTime(t time.Time) Date {
	y, m, d := t.Date()
	jy, jm, jd := fromGregorian(y, int(m), d)
	return Date{jy, Month(jm), jd}
}

// Time returns Gregorian midnight of the date in loc.
func (d Date) Time(loc *time.Location) time.Time {
	gy, gm, gd := toGregorian(d.Year, int(d.Month), d.Day)
	return time.Date(gy, time.Month(gm), gd, 0, 0, 0, 0, loc)
}

// IsValid reports a date the calendar actually has.
func (d Date) IsValid() bool {
	if d.Month < Farvardin || d.Month > Esfand || d.Day < 1 {
		return false
	}
	return d.Day <= DaysInMonth(d.Year, d.Month)
}

// IsLeap reports a year of 366 days, whose Esfand has 30.
func IsLeap(year int) bool {
	leap, _, _ := yearCycle(year)
	return leap == 0
}

// DaysInMonth returns the length of a month: 31 in the first half of the
// year, 30 in the second, and 29 or 30 in Esfand.
func DaysInMonth(year int, m Month) int {
	switch {
	case m >= Farvardin && m <= Shahrivar:
		return 31
	case m >= Mehr && m <= Bahman:
		return 30
	case m == Esfand:
		if IsLeap(year) {
			return 30
		}
		return 29
	}
	return 0
}

// breaks are the years where the leap pattern shifts, from the
// astronomical determination of Nowruz.
var breaks = [...]int{
	-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210,
	1635, 1701, 1866, 2328, 3167,
}

// yearCycle reports the year's position in its leap cycle: leap is the
// distance to the previous leap year, gy the Gregorian year the Solar
// year starts in, and march the day of March that Farvardin 1 falls on.
func yearCycle(jy int) (leap, gy, march int) {
	gy = jy + 621
	leapJ := -14
	jp := breaks[0]

	jump := 0
	for i := 1; i < len(breaks); i++ {
		jm := breaks[i]
		jump = jm - jp
		if jy < jm {
			break
		}
		leapJ += (jump/33)*8 + (jump%33)/4
		jp = jm
	}
	n := jy - jp

	leapJ += (n/33)*8 + ((n%33)+3)/4
	if jump%33 == 4 && jump-n == 4 {
		leapJ++
	}

	leapG := gy/4 - ((gy/100+1)*3)/4 - 150
	march = 20 + leapJ - leapG

	if jump-n < 6 {
		n = n - jump + ((jump+4)/33)*33
	}
	leap = ((n+1)%33 - 1) % 4
	if leap == -1 {
		leap = 4
	}
	return leap, gy, march
}

// fromGregorian converts a Gregorian date to a Solar Hijri one.
func fromGregorian(gy, gm, gd int) (jy, jm, jd int) {
	return fromDayNumber(gregorianDayNumber(gy, gm, gd))
}

// toGregorian converts a Solar Hijri date to a Gregorian one.
func toGregorian(jy, jm, jd int) (gy, gm, gd int) {
	return gregorianFromDayNumber(dayNumber(jy, jm, jd))
}

// gregorianDayNumber is the count of days to a Gregorian date, on the
// same scale dayNumber uses.
func gregorianDayNumber(gy, gm, gd int) int {
	d := (gy+(gm-8)/6+100100)*1461/4 +
		(153*((gm+9)%12)+2)/5 + gd - 34840408
	d = d - (gy+100100+(gm-8)/6)/100*3/4 + 752
	return d
}

// gregorianFromDayNumber is the inverse of gregorianDayNumber.
func gregorianFromDayNumber(dn int) (gy, gm, gd int) {
	j := 4*dn + 139361631
	j = j + (4*dn+183187720)/146097*3/4*4 - 3908
	i := (j%1461)/4*5 + 308
	gd = (i%153)/5 + 1
	gm = (i/153)%12 + 1
	gy = j/1461 - 100100 + (8-gm)/6
	return gy, gm, gd
}

// dayNumber is the count of days to a Solar Hijri date.
func dayNumber(jy, jm, jd int) int {
	_, gy, march := yearCycle(jy)
	return gregorianDayNumber(gy, 3, march) + (jm-1)*31 -
		jm/7*(jm-7) + jd - 1
}

// fromDayNumber is the inverse of dayNumber.
func fromDayNumber(dn int) (jy, jm, jd int) {
	gy, _, _ := gregorianFromDayNumber(dn)
	jy = gy - 621
	leap, _, march := yearCycle(jy)
	jdn1f := gregorianDayNumber(gy, 3, march)

	k := dn - jdn1f
	if k >= 0 {
		if k <= 185 {
			jm = 1 + k/31
			jd = k%31 + 1
			return jy, jm, jd
		}
		k -= 186
	} else {
		// Before Farvardin 1: the day belongs to the previous year,
		// whose length the leap of this one decides.
		jy--
		k += 179
		if leap == 1 {
			k++
		}
	}
	jm = 7 + k/30
	jd = k%30 + 1
	return jy, jm, jd
}
