package pim

import (
	"strings"

	"git.mehdix.org/alborz/search"
)

// Matches says whether a query's every term is in the fields, or, negated, is not, in any case
// (search.Fold); a key is read as its text, as mail reads a key it does not know.
func Matches(q search.Query, fields ...string) bool {
	text := search.Fold(strings.Join(fields, "\n"))
	for _, term := range q {
		needle := term.Value
		if term.Key != "" {
			needle = term.Key + ":" + term.Value
		}
		if strings.Contains(text, search.Fold(needle)) == term.Not {
			return false
		}
	}
	return true
}
