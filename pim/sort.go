package pim

import (
	"slices"
	"strings"
	"time"
)

// Column is one order a list can take: its URL key and a row's sort value.
type Column[T any] struct {
	Key   string
	Value func(T) string
}

// Last sorts after every value, for a row that has none.
const Last = "\uffff"

// When is a time as a column sorts it; a row without one sorts Last.
func When(t time.Time) string {
	if t.IsZero() {
		return Last
	}
	return t.Format(time.RFC3339)
}

// StarredFirst sorts marked rows first, as the mail list does by flag.
func StarredFirst(star string) string {
	if star != "" {
		return "0"
	}
	return "1"
}

// Order sorts rows by column, equal rows by tie; desc reverses both.
func Order[T any](rows []T, column Column[T], tie func(T) string, desc bool) {
	// Each row's values are taken once, not once per comparison.
	type keyed struct {
		row        T
		value, tie string
	}
	all := make([]keyed, len(rows))
	for i, row := range rows {
		all[i] = keyed{row, column.Value(row), tie(row)}
	}
	slices.SortStableFunc(all, func(a, b keyed) int {
		x, y := a.value, b.value
		if x == y {
			x, y = a.tie, b.tie
		}
		if desc {
			x, y = y, x
		}
		return strings.Compare(x, y)
	})
	for i := range all {
		rows[i] = all[i].row
	}
}
