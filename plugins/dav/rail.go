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
}
