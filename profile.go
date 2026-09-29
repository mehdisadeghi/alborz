package alborz

import (
	"cmp"
	"time"
)

// Profile is the person's own choices, kept on every account signed in (ADR 52).
type Profile struct {
	Reading Reading
	Writing Writing
	Display Display
}

type Reading struct {
	MessagesPerPage int
	Timezone        string
	// FirstDayOfWeek counts as ISO 8601 does, 1 for Monday to 7 for Sunday; 0 was never chosen.
	FirstDayOfWeek int
	PreferHTML     bool
	// Primary and Secondary are calendar systems; empty means Gregorian and none.
	Primary   string
	Secondary string
}

type Writing struct {
	ReplyBelowQuote bool
	// NoDirectionPart is negative so the zero value keeps the part.
	NoDirectionPart bool
}

// Display is empty where it follows the default.
type Display struct {
	Theme         string
	TextSize      string
	AlignByScript bool
	Language      string
}

const ProfileKey = "profile"

// DefaultMessagesPerPage is the page size of a reader who never chose one.
const DefaultMessagesPerPage = 50

// DefaultFirstDayOfWeek is Monday, the first day of an ISO 8601 week.
const DefaultFirstDayOfWeek = 1

// WeekStart is the weekday a week begins on.
func (r Reading) WeekStart() time.Weekday {
	return time.Weekday(cmp.Or(r.FirstDayOfWeek, DefaultFirstDayOfWeek) % 7)
}
