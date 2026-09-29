package web

import "git.mehdix.org/alborz/pim/dav"

// Rail is the section's rail on a page that is not its list.
type Rail struct {
	Path      string
	Field     string
	ItemClass string
	Action    string
	// Edit is the route of a collection's own page.
	Edit     string
	Items    []dav.Collection
	NewHref  string
	NewLabel string
	// FollowHref is the calendar's second way of adding one; empty elsewhere.
	FollowHref  string
	FollowLabel string
	ImportHref  string
	ImportLabel string
	// Groups and Categories are the contact rail's narrowings, nil for other kinds.
	Groups     []FilterRow
	Categories []FilterRow
	// Invitations are the section's open invitations, nil for none.
	Invitations *RailInvitations
}

// FilterRow is one narrowing beside the collections.
type FilterRow struct {
	Label  string
	Href   string
	Active bool
	// Edit is the row's own page, where it has one: a group is a card.
	Edit string
}
