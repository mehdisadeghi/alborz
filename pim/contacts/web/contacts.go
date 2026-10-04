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
	// Pager walks the pages of AddressObjects.
	Pager web.Pager
	web.BaseRenderData
	AddressBooks   []dav.Collection
	AddressObjects []contacts.AddressObject
	Sorting        pimweb.Sorting
	Query          string
	// View is the colour the rail is filtering by, empty for all.
	View string
	// Filters are the narrowings nothing else on the page states; FilterRows the filter menu.
	Filters    []web.Filter
	FilterRows []web.FilterRow
	// CollectionFor is a contact's address book, CollectionHref the list narrowed to it.
	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// HrefFor is a contact's page with the list carried along.
	HrefFor func(account, path string) string
	// AddedBy names who added a card, where not the row's own account.
	AddedBy func(account, path string) string
	// Groups are the accounts' group cards, Categories the words in use.
	Groups     []contacts.AddressObject
	Categories []string
	Group      string
	Category   string
	// GroupRows and CategoryRows are the rail rows for groups and categories.
	GroupRows, CategoryRows []FilterRow
	// SelectForm is the form a row's checkbox belongs to; empty where rows offer none.
	SelectForm string

	Invitations *pimweb.RailInvitations
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
	queryText := ctx.QueryParam("query")
	view := ctx.QueryParam("view")
	if view != "" && view != alborz.ViewStarred && !slices.Contains(alborz.FlagColors[:], view) {
		return ContactList{}, web.NewError(http.StatusBadRequest, "no such view")
	}
	group, category := ctx.QueryParam("group"), ctx.QueryParam("category")
	params := pimweb.RowParams(ctx, ctx.Path("contacts.list"), pimweb.ListParams(ctx, contactListParams...))

	aos, groups, addressBookInfos, unread, err := p.shownCards(ctx, pimweb.Only(ctx, "book"), view, queryText)
	if err != nil {
		return ContactList{}, err
	}

	sorting, err := pimweb.Sort(ctx, aos, contactColumns(addressBookInfos), cardTie, contactListParams...)
	if err != nil {
		return ContactList{}, err
	}

	// The group names its members by UID; the filter asks which cards it holds.
	if group != "" {
		members := map[string]bool{}
		for _, g := range groups {
			if g.UID() != group {
				continue
			}
			for _, uid := range g.Members() {
				members[uid] = true
			}
		}
		kept := aos[:0]
		for _, ao := range aos {
			if members[ao.UID()] {
				kept = append(kept, ao)
			}
		}
		aos = kept
	}
	if category != "" {
		kept := aos[:0]
		for _, ao := range aos {
			if slices.Contains(ao.Categories(), category) {
				kept = append(kept, ao)
			}
		}
		aos = kept
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

	items := make([]pimweb.Item, len(aos))
	for i, ao := range aos {
		items[i] = pimweb.Item{Path: ao.Path, URL: pimweb.ObjectURL(ctx, "contacts.contact", ao.Path, ao.Account, params)}
	}
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
func (p *plugin) shownCards(ctx *web.Context, only map[string]bool, view, queryText string) (aos, groups []contacts.AddressObject, books []dav.Collection, unread []pim.Unread, err error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	books, sites, err := contacts.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), only)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	words, starred, err := web.StarFilter(search.Parse(queryText), view)
	if err != nil {
		return nil, nil, nil, nil, web.NewError(http.StatusBadRequest, err.Error())
	}
	query := contacts.Query(words.String())
	for _, result := range pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*carddav.Client].Account, func(ctx context.Context, site pim.Site[*carddav.Client]) ([]carddav.AddressObject, error) {
		return site.Client.QueryAddressBook(ctx, site.Collection.Path, &query)
	})) {
		name := result.Site.Collection.Name
		if result.Err != nil {
			unread = append(unread, pim.Unread{Collection: name, Err: result.Err})
			continue
		}
		for i := range result.Value {
			if result.Value[i].Card == nil {
				unread = append(unread, contacts.Unparsed(name, &result.Value[i]))
				continue
			}
			ao := contacts.AddressObject{AddressObject: &result.Value[i], Account: result.Site.Collection.Account}
			// A group is a card and arrives with the contacts, but is not a person.
			if ao.IsGroup() {
				groups = append(groups, ao)
				continue
			}
			// A colour is a view, as in the mail rail; starred is any colour.
			if starred(ao.Color()) {
				aos = append(aos, ao)
			}
		}
	}
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
	pimweb.WarnUnread(ctx, list.Unread)
	addressBookInfos := list.Books
	hrefs := make(map[string]string, len(list.Items))
	for _, item := range list.Items {
		hrefs[item.Path] = item.URL
	}
	cards, pager, err := web.Paginate(ctx, list.Cards)
	if err != nil {
		return err
	}
	collection, href := pimweb.Labels(ctx, addressBookInfos, "contacts.list", "book", nil)
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "contacts/list", &AddressBookRenderData{
		SelectForm:     "address-book-form",
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.contacts")),
		AddressBooks:   addressBookInfos,
		AddressObjects: cards,
		Pager:          pager,
		HrefFor:        func(_, contactPath string) string { return hrefs[contactPath] },
		AddedBy:        p.DAV.AddedBy(ctx.Login.Username()),
		Query:          list.Query,
		View:           list.View,
		Groups:         list.Groups,
		Categories:     list.Categories,
		GroupRows:      contactGroupRows(ctx, list.Groups, list.Group, ctx.URLAccount()),
		CategoryRows:   contactCategoryRows(ctx, list.Categories, list.Category, ctx.URLAccount()),
		Group:          list.Group,
		Category:       list.Category,
		Filters:        searchFilter(ctx, list),
		FilterRows:     web.ViewRows(ctx, list.View, false, ctx.WithoutParam("view")),
		Sorting:        list.Sorting,
		CollectionFor:  collection,
		CollectionHref: href,
		Invitations:    invitations,
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
	cards, _, books, unread, err := p.shownCards(ctx, nil, view, q.String())
	if err != nil {
		return web.Found{}, err
	}
	pimweb.WarnUnread(ctx, unread)
	if _, err := pimweb.Sort(ctx, cards, contactColumns(books), cardTie); err != nil {
		return web.Found{}, err
	}
	params := pimweb.ListParams(ctx, contactListParams...)
	params.Set("from", ctx.Request().URL.RequestURI())
	shown := cards[:min(limit, len(cards))]
	hrefs := make(map[string]string, len(shown))
	for _, ao := range shown {
		hrefs[ao.Path] = pimweb.ObjectURL(ctx, "contacts.contact", ao.Path, ao.Account, params)
	}
	collection, href := pimweb.Labels(ctx, books, "contacts.list", "book", nil)
	return web.Found{Rows: &AddressBookRenderData{
		AddressObjects: shown,
		HrefFor:        func(_, path string) string { return hrefs[path] },
		AddedBy:        p.DAV.AddedBy(ctx.Login.Username()),
		CollectionFor:  collection,
		CollectionHref: href,
	}, Total: len(cards), All: ctx.SearchURL("contacts.list", q, view)}, nil
}
