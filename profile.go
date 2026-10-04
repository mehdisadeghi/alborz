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
	// Toured is set once a sign-in has opened the tour; it opens by itself only once (ADR 76).
	Toured bool
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
	// Status names the status bar's segments turned on; the bar keeps the order they were mounted in.
	Status []string
	// Addons names the add-ons turned on.
	Addons []string
}

const ProfileKey = "profile"

// DefaultMessagesPerPage is the page size of a reader who never chose one.
const DefaultMessagesPerPage = 50

// Page is one page of a list: its number, counted from zero, and how many rows a page holds.
type Page struct {
	Number, Size int
}

// Within is the page in a list of total rows. A page past the end is the
// last one: the list may have shrunk since the link was drawn.
func (p Page) Within(total int) Page {
	p.Number = min(p.Number, max(0, (total-1)/p.Size))
	return p
}

// Rows are the bounds of the page's rows in a list of total rows.
func (p Page) Rows(total int) (from, to int) {
	from = min(p.Number*p.Size, total)
	return from, min(from+p.Size, total)
}

// Window is how many rows lie from the top of the list to the end of this page.
func (p Page) Window() int {
	return (p.Number + 1) * p.Size
}

// DefaultFirstDayOfWeek is Monday, the first day of an ISO 8601 week.
const DefaultFirstDayOfWeek = 1

// WeekStart is the weekday a week begins on.
func (r Reading) WeekStart() time.Weekday {
	return time.Weekday(cmp.Or(r.FirstDayOfWeek, DefaultFirstDayOfWeek) % 7)
}
