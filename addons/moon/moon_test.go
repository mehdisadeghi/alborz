package moon_test

import (
	"testing"
	"time"

	"git.mehdix.org/alborz/addons/moon"
)

// Published phases (USNO), a day either side of each still drawn the same.
func TestThePhaseMatchesPublishedMoons(t *testing.T) {
	for _, c := range []struct {
		at   string
		want string
	}{
		{"1999-12-22T17:31:00Z", "🌕"},
		{"2024-01-11T11:57:00Z", "🌑"},
		{"2024-01-18T03:53:00Z", "🌓"},
		{"2024-01-25T17:54:00Z", "🌕"},
		{"2024-02-02T23:18:00Z", "🌗"},
		{"2024-04-08T18:21:00Z", "🌑"},
		{"2025-03-14T06:55:00Z", "🌕"},
	} {
		at, err := time.Parse(time.RFC3339, c.at)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
			if p, _ := moon.At(at.Add(d)); p.Emoji() != c.want {
				t.Errorf("%s%+v is %s, want %s", c.at, d, p.Emoji(), c.want)
			}
		}
	}
}
