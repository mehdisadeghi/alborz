package web

import (
	"git.mehdix.org/alborz/pim/dav"
	rootweb "git.mehdix.org/alborz/web"
)

// ObjectRenderData is the common frame for an individual DAV object page.
// Calendar, contacts, and future PIM sections add only their object fields.
type ObjectRenderData struct {
	rootweb.PageData[Rail]
	Collection *dav.Collection
	Authors    dav.Authors
	List       string
	Star       string
	Neighbours rootweb.Neighbours
}

// ListRenderData is the shared frame for every collection-backed list page.
// Sections add only their rows and section-specific filters around this frame.
type ListRenderData struct {
	rootweb.BaseRenderData
	CollectionLabels
	Collections []dav.Collection
	View        string
	Filters     []rootweb.Filter
	FilterRows  []rootweb.FilterRow
	Query       string
	Sorting     Sorting
	Pager       rootweb.Pager
	SelectForm  string
	Invitations *RailInvitations
}

// NewListRenderData builds the common frame shared by every collection-backed list.
// Section pages add only their collection labels and row-specific data.
func NewListRenderData(ctx *rootweb.Context, title string, collections []dav.Collection, view, query string, sorting Sorting, pager rootweb.Pager, filters []rootweb.Filter, filterRows []rootweb.FilterRow, selectForm string, invitations *RailInvitations) ListRenderData {
	return ListRenderData{
		BaseRenderData: *rootweb.NewBaseRenderData(ctx).WithTitle(title), Collections: collections,
		View: view, Query: query, Sorting: sorting, Pager: pager, Filters: filters, FilterRows: filterRows,
		SelectForm: selectForm, Invitations: invitations,
	}
}

// CollectionLabels are the common links and attribution shown beside DAV rows.
type CollectionLabels struct {
	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	AddedBy        func(account, path string) string
}

// NewCollectionLabels combines section links with the provider's attribution.
func NewCollectionLabels(p *dav.Provider, viewer string, collection func(string, string) dav.Collection, href func(string, string) string) CollectionLabels {
	return CollectionLabels{CollectionFor: collection, CollectionHref: href, AddedBy: p.AddedBy(viewer)}
}

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

// EditRenderData is the common frame for forms that edit one DAV object.
// Sections provide only their object-specific form fields around this frame.
type EditRenderData[O any] struct {
	rootweb.PageData[Rail]
	Groups     []dav.Group
	Collection *dav.Collection
	Object     *O
}

// FilterRow is one narrowing beside the collections.
type FilterRow struct {
	Label  string
	Href   string
	Active bool
	// Edit is the row's own page, where it has one: a group is a card.
	Edit string
}
