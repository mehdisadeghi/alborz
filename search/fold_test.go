package search

import "testing"

func TestFoldReadsEveryCaseAsOne(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"Invoice", "iNVOICE", true},
		{"ÄRGER", "ärger", true},
		{"Straße", "STRASSE", true},
		{"STRAẞE", "straße", true},
		{"İstanbul", "istanbul", true},
		{"İstanbul", "istanbul", true},
		{"IŞIK", "ışık", true},
		{"ΟΔΟΣ", "οδος", true},
		{"οδοσ", "οδος", true},
		{"ΜΆΪΟΣ", "μάϊος", true},
		{"ПРИВЕТ", "привет", true},
		// An Arabic keyboard writes the Persian yeh and kaf as its own.
		{"علي کيا", "علی کیا", true},
		{"كتاب", "کتاب", true},
		// A letter with a mark is another letter, and text without case is itself.
		{"Bar", "Bär", false},
		{"کتاب", "کتاب", true},
		{"کتاب", "کباب", false},
	} {
		if got := Fold(tc.a) == Fold(tc.b); got != tc.same {
			t.Errorf("%q and %q: folded to %q and %q, same %v, want %v", tc.a, tc.b, Fold(tc.a), Fold(tc.b), got, tc.same)
		}
	}
}
