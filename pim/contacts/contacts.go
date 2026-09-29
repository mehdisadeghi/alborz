package contacts

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav/carddav"
)

type AddressBookRenderData struct {
	// Pager walks the pages of AddressObjects.
	Pager web.Pager
	web.BaseRenderData
	AddressBooks   []dav.Collection
	AddressObjects []AddressObject
	Sorting        pim.Sorting
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
	Groups     []AddressObject
	Categories []string
	Group      string
	Category   string
	// GroupRows and CategoryRows are the rail rows for groups and categories.
	GroupRows, CategoryRows []FilterRow

	Invitations *dav.RailInvitations
}

var contactListParams = []string{"account", "book", "query", "view", "group", "category", "sort", "dir", "page", "ipp"}

// ContactList is the list page's cards in order, their books, and what shaped them.
type ContactList struct {
	Cards []AddressObject
	Books []dav.Collection
	// Items are the cards as a single contact's neighbours, in the same order.
	Items []pim.Item
	// Groups are the group cards, offered as filters and never listed as contacts.
	Groups []AddressObject
	// Categories are the words in use across what was read, sorted.
	Categories []string
	Query      string
	View       string
	Group      string
	Category   string
	// GroupRows and CategoryRows are the rail rows for groups and categories.
	GroupRows, CategoryRows []FilterRow
	Sorting                 pim.Sorting
}

func (p *plugin) contactList(ctx *web.Context) (ContactList, error) {
	queryText := ctx.QueryParam("query")
	view := ctx.QueryParam("view")
	if view != "" && view != web.ViewStarred && !slices.Contains(alborz.FlagColors[:], view) {
		return ContactList{}, web.NewError(http.StatusBadRequest, "no such view")
	}
	group, category := ctx.QueryParam("group"), ctx.QueryParam("category")
	params := pim.RowParams(ctx, ctx.Path("contacts.list"), pim.ListParams(ctx, contactListParams...))

	only := pim.Only(ctx, "book")
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return ContactList{}, err
	}

	addressBookInfos, sites, err := visibleBooks(ctx.Request().Context(), accounts, ctx.URLAccount(), only)
	if err != nil {
		return ContactList{}, err
	}

	query := contactsQuery(queryText)

	var aos, groups []AddressObject
	for _, result := range pim.Present(p.DAV, dav.Across(ctx.Request().Context(), sites, pim.Site[*carddav.Client].Account, func(ctx context.Context, site pim.Site[*carddav.Client]) ([]carddav.AddressObject, error) {
		return site.Client.QueryAddressBook(ctx, site.Collection.Path, &query)
	})) {
		if result.Err != nil {
			return ContactList{}, fmt.Errorf("failed to query address book %s: %v", result.Site.Collection.Name, result.Err)
		}
		for i := range result.Value {
			ao := AddressObject{AddressObject: &result.Value[i], Account: result.Site.Collection.Account}
			// A group is a card and arrives with the contacts, but is not a person.
			if ao.IsGroup() {
				groups = append(groups, ao)
				continue
			}
			// A colour is a view, as in the mail rail; starred is any colour.
			if view == web.ViewStarred && ao.Color() == "" {
				continue
			}
			if view != "" && view != web.ViewStarred && ao.Color() != view {
				continue
			}
			aos = append(aos, ao)
		}
	}

	sorting, err := pim.Sort(ctx, aos, contactColumns(addressBookInfos), func(ao AddressObject) string {
		return strings.ToLower(ao.DisplayName())
	}, contactListParams...)
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

	items := make([]pim.Item, len(aos))
	for i, ao := range aos {
		items[i] = pim.Item{Path: ao.Path, URL: pim.ObjectURL(ctx, "contacts.contact", ao.Path, ao.Account, params)}
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
	}, nil
}

func contactColumns(books []dav.Collection) []pim.Column[AddressObject] {
	return []pim.Column[AddressObject]{
		{Key: "name", Value: func(ao AddressObject) string { return strings.ToLower(ao.DisplayName()) }},
		{Key: web.ViewStarred, Value: func(ao AddressObject) string { return pim.StarredFirst(ao.Color()) }},
		{Key: "email", Value: func(ao AddressObject) string { return strings.ToLower(ao.Card.PreferredValue("EMAIL")) }},
		{Key: "phone", Value: func(ao AddressObject) string { return strings.ToLower(ao.Card.PreferredValue("TEL")) }},
		{Key: "account", Value: func(ao AddressObject) string { return strings.ToLower(ao.Account) }},
		{Key: "book", Value: func(ao AddressObject) string {
			if ab := dav.Holding(books, ao.Account, ao.Path); ab != nil {
				return strings.ToLower(ab.Name + "\x00" + ao.Account)
			}
			return pim.Last
		}},
		{Key: "changed", Value: func(ao AddressObject) string { return pim.When(cardModified(ao.AddressObject)) }},
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
	cards, pager, err := web.Paginate(ctx, list.Cards)
	if err != nil {
		return err
	}
	collection, href := pim.Labels(ctx, addressBookInfos, "contacts.list", "book", nil)
	invitations, err := p.invitations(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "contacts/list", &AddressBookRenderData{
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
		if f.Value == web.ViewStarred {
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
