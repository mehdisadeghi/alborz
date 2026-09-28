package dav

// Rail is the section's rail on a page that is not its list.
type Rail struct {
	Path      string
	Field     string
	ItemClass string
	Action    string
	EditHref  string
	Items     []Collection
	NewHref   string
	NewLabel  string
	// FollowHref is the calendar's second way of adding one; empty elsewhere.
	FollowHref  string
	FollowLabel string
	ImportHref  string
	ImportLabel string
	// Groups and Categories are the contact rail's narrowings, nil for other kinds.
	Groups     []FilterRow
	Categories []FilterRow
}

// FilterRow is one narrowing beside the collections.
type FilterRow struct {
	Label  string
	Href   string
	Active bool
	// Edit is the row's own page, where it has one: a group is a card.
	Edit string
}
