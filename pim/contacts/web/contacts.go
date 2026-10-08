package web

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/contacts"
	"git.mehdix.org/alborz/pim/dav"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/search"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav/carddav"
)

type AddressBookRenderData struct {
	pimweb.ListRenderData
	AddressObjects []contacts.AddressObject
	// HrefFor is a contact's page with the list carried along.
	HrefFor func(account, path string) string
	// Groups are the accounts' group cards, Categories the words in use.
	Groups     []contacts.AddressObject
	Categories []string
	Group      string
	Category   string
	// GroupRows and CategoryRows are the rail rows for groups and categories.
	GroupRows, CategoryRows []FilterRow
}

var contactListParams = []string{"account", "book", "query", "view", "group", "category", "sort", "dir", "page", "ipp"}

// ContactList is the list page's cards in order, their books, and what shaped them.
type ContactList struct {
	Cards []contacts.AddressObject
	Books []dav.Collection
	// Items are the cards as a single contact's neighbours, in the same order.
	Items []pimweb.Item
	// Groups are the group cards, offered as filters and never listed as contacts.
	Groups []contacts.AddressObject
	// Unread is what of the books could not be read.
	Unread []pim.Unread
	// Categories are the words in use across what was read, sorted.
	Categories []string
	Query      string
	View       string
	Group      string
	Category   string
	// GroupRows and CategoryRows are the rail rows for groups and categories.
	GroupRows, CategoryRows []FilterRow
	Sorting                 pimweb.Sorting
}

func (p *plugin) contactList(ctx *web.Context) (ContactList, error) {
	state := web.ReadListState(ctx)
	queryText, query, view := state.Query, state.ParsedQuery(), state.View
	if !web.ValidStarView(view) {
		return ContactList{}, web.NewError(http.StatusBadRequest, "no such view")
	}
	group, category := ctx.QueryParam("group"), ctx.QueryParam("category")
	params := state.Rows(ctx.Path("contacts.list"), state.Keep(contactListParams...))

	aos, groups, addressBookInfos, unread, err := p.shownCards(ctx, pimweb.Only(ctx, "book"), view, query)
	if err != nil {
		return ContactList{}, err
	}

	sorting, err := pimweb.SortList(state, aos, contactColumns(addressBookInfos), cardTie, contactListParams...)
	if err != nil {
		return ContactList{}, err
	}

	// The group names its members by UID; the filter asks which cards it holds.
	var members map[string]bool
	if group != "" {
		members = map[string]bool{}
		for _, g := range groups {
			if g.UID() != group {
				continue
			}
			for _, uid := range g.Members() {
				members[uid] = true
			}
		}
	}
	if group != "" || category != "" {
		aos = slices.DeleteFunc(aos, func(ao contacts.AddressObject) bool {
			return (group != "" && !members[ao.UID()]) || (category != "" && !slices.Contains(ao.Categories(), category))
		})
	}
	var words []string
	for _, ao := range aos {
		words = append(words, ao.Categories()...)
	}
	slices.Sort(words)
	words = slices.Compact(words)
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].DisplayName()) < strings.ToLower(groups[j].DisplayName())
	})

	items := pimweb.ItemsFor(aos, func(ao contacts.AddressObject) pimweb.Item {
		return pimweb.Item{Path: ao.Path, URL: pimweb.ObjectURL(ctx, "contacts.contact", ao.Path, ao.Account, params)}
	})
	return ContactList{
		Cards:      aos,
		Books:      addressBookInfos,
		Items:      items,
		Groups:     groups,
		Categories: words,
		Query:      queryText,
		View:       view,
		Group:      group,
		Category:   category,
		Sorting:    sorting,
		Unread:     unread,
	}, nil
}

// shownCards are the contacts a query and a view find in the address books in the request's scope,
// the groups beside them, every book for the rail, and what of the books could not be read.
func (p *plugin) shownCards(ctx *web.Context, only map[string]bool, view string, parsed search.Query) (aos, groups []contacts.AddressObject, books []dav.Collection, unread []pim.Unread, err error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	books, sites, err := contacts.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	words, starred, err := web.StarFilter(parsed, view)
	if err != nil {
		return nil, nil, nil, nil, web.NewError(http.StatusBadRequest, err.Error())
	}
	query := contacts.Query(words.String())
	results := pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*carddav.Client].Account, func(ctx context.Context, site pim.Site[*carddav.Client]) ([]carddav.AddressObject, error) {
		return site.Client.QueryAddressBook(ctx, site.Collection.Path, &query)
	}))
	aos, unread = pimweb.ProjectResults(results, func(object *carddav.AddressObject, collection dav.Collection) (contacts.AddressObject, *pim.Unread, bool) {
		if object.Card == nil {
			failed := contacts.Unparsed(collection.Name, object)
			return contacts.AddressObject{}, &failed, false
		}
		ao := contacts.AddressObject{AddressObject: object, Account: collection.Account}
		if ao.IsGroup() {
			groups = append(groups, ao)
			return contacts.AddressObject{}, nil, false
		}
		return ao, nil, starred(ao.Color())
	})
	return aos, groups, books, unread, nil
}

func cardTie(ao contacts.AddressObject) string { return strings.ToLower(ao.DisplayName()) }

func contactColumns(books []dav.Collection) []pim.Column[contacts.AddressObject] {
	return []pim.Column[contacts.AddressObject]{
		{Key: "name", Value: func(ao contacts.AddressObject) string { return strings.ToLower(ao.DisplayName()) }},
		{Key: alborz.ViewStarred, Value: func(ao contacts.AddressObject) string { return pim.StarredFirst(ao.Color()) }},
		{Key: "email", Value: func(ao contacts.AddressObject) string { return strings.ToLower(ao.Card.PreferredValue("EMAIL")) }},
		{Key: "phone", Value: func(ao contacts.AddressObject) string { return strings.ToLower(ao.Card.PreferredValue("TEL")) }},
		{Key: "account", Value: func(ao contacts.AddressObject) string { return strings.ToLower(ao.Account) }},
		{Key: "book", Value: func(ao contacts.AddressObject) string {
			if ab := dav.Holding(books, ao.Account, ao.Path); ab != nil {
				return strings.ToLower(ab.Name + "\x00" + ao.Account)
			}
			return pim.Last
		}},
		{Key: "changed", Value: func(ao contacts.AddressObject) string { return pim.When(contacts.Modified(ao.AddressObject)) }},
	}
}

func (p *plugin) contacts(ctx *web.Context) error {
	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	addressBookInfos := list.Books
	hrefs := make(map[string]string, len(list.Items))
	for _, item := range list.Items {
		hrefs[item.Path] = item.URL
	}
	cards, pager, invitations, err := pimweb.ListParts(ctx, list.Unread, list.Cards, func() (*pimweb.RailInvitations, error) { return p.invitations(ctx) })
	if err != nil {
		return err
	}
	collection, href := pimweb.Labels(ctx, addressBookInfos, "contacts.list", "book", nil)
	listData := pimweb.NewListRenderData(ctx, ctx.T("nav.contacts"), addressBookInfos, list.View, list.Query, list.Sorting, pager,
		searchFilter(ctx, list), web.ViewRows(ctx, list.View, false, ctx.WithoutParam("view")), "address-book-form", invitations)
	listData.CollectionLabels = pimweb.NewCollectionLabels(p.DAV, ctx.Login.Username(), collection, href)
	return ctx.Render(http.StatusOK, "contacts/list", &AddressBookRenderData{
		ListRenderData: listData,
		AddressObjects: cards,
		HrefFor:        func(_, contactPath string) string { return hrefs[contactPath] },
		Groups:         list.Groups,
		Categories:     list.Categories,
		GroupRows:      contactGroupRows(ctx, list.Groups, list.Group, ctx.URLAccount()),
		CategoryRows:   contactCategoryRows(ctx, list.Categories, list.Category, ctx.URLAccount()),
		Group:          list.Group,
		Category:       list.Category,
	})
}

func searchFilter(ctx *web.Context, list ContactList) []web.Filter {
	var out []web.Filter
	if f, ok := ctx.FilterOn("query", ctx.T("filter.search")); ok {
		out = append(out, f)
	}
	if f, ok := ctx.FilterOn("view", ctx.T("filter.color")); ok {
		if f.Value == alborz.ViewStarred {
			f.Value = ctx.T("mailbox.starred")
		} else {
			f.Value = ctx.T("color." + f.Value)
		}
		out = append(out, f)
	}
	if f, ok := ctx.FilterOn("category", ctx.T("filter.category")); ok {
		out = append(out, f)
	}
	// The book the page is in is a place, named by rail and crumb, not a chip.
	if f, ok := ctx.FilterOn("group", ctx.T("filter.group")); ok {
		// The chip names the group as the reader does, not by UID.
		for _, g := range list.Groups {
			if g.UID() == f.Value {
				f.Value = g.DisplayName()
			}
		}
		out = append(out, f)
	}
	return out
}

// found is the contacts' part in the search across sections, in the list's own order.
func (p *plugin) found(ctx *web.Context, q search.Query, view string, limit int) (web.Found, error) {
	if unasked := web.StarUnasked(q); unasked != nil {
		return web.Found{Unasked: unasked}, nil
	}
	cards, _, books, unread, err := p.shownCards(ctx, nil, view, q)
	if err != nil {
		return web.Found{}, err
	}
	state := web.ReadListState(ctx)
	shown, total, err := pimweb.SearchListRows(ctx, state, cards, unread, limit, contactColumns(books), cardTie)
	if err != nil {
		return web.Found{}, err
	}
	params := state.Keep(contactListParams...)
	params.Set("from", ctx.Request().URL.RequestURI())
	hrefs := make(map[string]string, len(shown))
	for _, ao := range shown {
		hrefs[ao.Path] = pimweb.ObjectURL(ctx, "contacts.contact", ao.Path, ao.Account, params)
	}
	collection, href := pimweb.Labels(ctx, books, "contacts.list", "book", nil)
	return web.Found{Rows: &AddressBookRenderData{
		ListRenderData: pimweb.ListRenderData{CollectionLabels: pimweb.NewCollectionLabels(p.DAV, ctx.Login.Username(), collection, href)},
		AddressObjects: shown,
		HrefFor:        func(_, path string) string { return hrefs[path] },
	}, Total: total, All: ctx.SearchURL("contacts.list", q, view)}, nil
}
