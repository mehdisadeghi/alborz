package pim

import (
	"testing"

	"git.mehdix.org/alborz/search"
)

func TestMatchesInAnyCase(t *testing.T) {
	for _, tc := range []struct {
		query, field string
		want         bool
	}{
		{"lantern", "The Lantern Walk", true},
		{"LANTERN", "the lantern walk", true},
		{"-LANTERN", "the lantern walk", false},
		{"Note:Lantern", "note:lantern", true},
		{"strasse", "Grüße aus der Hauptstraße", true},
		{"HAUPTSTRASSE", "Grüße aus der Hauptstraße", true},
		{"straße", "HAUPTSTRASSE 5", true},
		{"GRÜSSE", "Grüße aus der Hauptstraße", true},
		{"istanbul", "İstanbul Ofisi", true},
		{"ISTANBUL", "ıstanbul", true},
		{"ΟΔΟΣ", "οδός και οδος", true},
		{"گزارش", "گزارش ماهانه", true},
		{"-گزارش", "گزارش ماهانه", false},
		{"كتاب علي", "کتاب علی", true},
		{"کیک", "كيك تولد", true},
		{"bar", "Bär", false},
	} {
		if got := Matches(search.Parse(tc.query), tc.field); got != tc.want {
			t.Errorf("%q in %q: got %v, want %v", tc.query, tc.field, got, tc.want)
		}
	}
}
