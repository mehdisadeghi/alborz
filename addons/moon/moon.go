// Package moon tells the moon's phase from the time alone, by the mean synodic month: close enough
// for an eighth of a cycle, with no ephemeris to ship or fetch.
package moon

import (
	"math"
	"time"
)

// synodicMonth is the mean time from new moon to new moon, in days (Meeus, Astronomical
// Algorithms, ch. 49); the true one strays from it by hours, a phase lasts days.
const synodicMonth = 29.530588853

// newMoon is the new moon Meeus counts lunations from, the nearest one to 2000.
var newMoon = time.Date(2000, time.January, 6, 18, 14, 0, 0, time.UTC)

// phases is the number of shapes the emoji draw, each centred on its point of the cycle.
const phases = 8

// Phase is one of the eight, from the new moon (0) through the waning crescent (7).
type Phase int

var emoji = [phases]string{"🌑", "🌒", "🌓", "🌔", "🌕", "🌖", "🌗", "🌘"}

// Emoji draws the phase as seen from the northern hemisphere.
func (p Phase) Emoji() string { return emoji[p] }

// At is the phase at t and the fraction of the disc lit.
func At(t time.Time) (Phase, float64) {
	days := t.Sub(newMoon).Hours() / 24
	age := math.Mod(days/synodicMonth, 1)
	if age < 0 {
		age++
	}
	return Phase(int(math.Floor(age*phases+0.5)) % phases), (1 - math.Cos(2*math.Pi*age)) / 2
}
