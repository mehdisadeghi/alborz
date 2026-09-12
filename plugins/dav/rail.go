package dav

// Rail is the section's rail on a page that is not its list: every
// account's collections of the kind, with the entries that add one, so
// a form or an object stands where its list does rather than in an
// empty aside.
type Rail struct {
	Path      string
	Field     string
	ItemClass string
	Action    string
	EditHref  string
	Items     []Collection
	NewHref   string
	NewLabel  string
	// Follow is the calendar's second way of adding one; empty elsewhere.
	FollowHref  string
	FollowLabel string
	// Import is the section's page for bringing a file in.
	ImportHref  string
	ImportLabel string
	// Groups and Categories are the contact rail's other narrowings,
	// nil for every other kind (ADR 17).
	Groups     []FilterRow
	Categories []FilterRow
}

// FilterRow is one entry beside the collections: a narrowing the list
// offers, the page with it, and whether it is in force. Groups and
// categories are such rows on the contacts rail, nil elsewhere.
type FilterRow struct {
	Label  string
	Href   string
	Active bool
}
