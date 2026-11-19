package search

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// Case folding leaves a dot above (U+0307) after the i of a Turkish İ, ı as it is, and the Arabic
// yeh (U+064A) and kaf (U+0643) apart from the Persian ones (U+06CC, U+06A9).
var alike = strings.NewReplacer("i\u0307", "i", "ı", "i", "ي", "ی", "ك", "ک")

// Fold is text as every search compares it: under Unicode full case folding, so ß is ss and ς is σ,
// with the Turkish dotted and dotless i read as i, and the Arabic yeh and kaf as the Persian ones.
// A letter with a mark stays another letter.
func Fold(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return alike.Replace(cases.Fold().String(s))
		}
	}
	return strings.ToLower(s)
}
