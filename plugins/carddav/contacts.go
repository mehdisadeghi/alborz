package alborzcarddav

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	alborzbase "git.mehdix.org/alborz/plugins/base"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"github.com/labstack/echo/v4"
)

type AddressBookRenderData struct {
	// Pager walks the pages of AddressObjects, which holds one of them.
	Pager dav.Pager
	alborz.BaseRenderData
	AddressBooks   []dav.Collection
	AddressObjects []AddressObject
	Sorting        dav.Sorting
	Query          string
	// View is the colour the rail is filtering by, empty for all.
	View string
	// Filters are the narrowings in force that nothing else on the
	// page states, and FilterRows the views the filter menu offers.
	Filters    []alborz.Filter
	FilterRows []alborz.FilterRow
	// CollectionFor is the address book a contact is in, so the list
	// can carry ownership on the collection rather than beside the name;
	// CollectionHref narrows the list to that book, in the row's own
	// scope.
	CollectionFor  func(account, path string) dav.Collection
	CollectionHref func(account, path string) string
	// HrefFor is where a contact's own page is, with the list carried
	// along so that page can name the contacts either side of it.
	HrefFor func(account, path string) string
	// AddedBy names who added a card kept here, where not the row's own
	// account.
	AddedBy func(account, path string) string
	// Groups are the group cards the accounts hold, and Categories the
	// words in use; the rail filters by either (RFC 6350 6.1.4, 6.7.1).
	Groups     []AddressObject
	Categories []string
	Group      string
	Category   string
}

// contactListParams are what decide which contacts a list holds and in
// what order: a contact's page is opened with them and returns to them.
var contactListParams = []string{"account", "book", "query", "view", "group", "category", "sort", "dir", "page", "ipp"}

// ContactList is the list page in one value: the cards in the order
// they are shown, the books they came from, and what shaped them.
type ContactList struct {
	Cards []AddressObject
	Books []dav.Collection
	// Items are the cards as the neighbours of a single contact, in
	// the same order.
	Items []dav.Item
	// Groups are the cards that are groups rather than people, which
	// the list never shows as contacts and the rail offers as filters.
	Groups []AddressObject
	// Categories are the words in use across what was read, sorted.
	Categories []string
	Query      string
	View       string // a colour the rail is filtering by, or empty
	Group      string // a group's UID, or empty
	Category   string // a word from CATEGORIES, or empty
	Sorting    dav.Sorting
}

// contactList is what the list page shows, in the order it shows it. A
// single contact's page asks for it as well, to know what stands before
// and after it in the list it was opened from.
func (p *plugin) contactList(ctx *alborz.Context) (ContactList, error) {
	queryText := ctx.QueryParam("query")
	view := ctx.QueryParam("view")
	if view != "" && view != alborzbase.ViewStarred && !slices.Contains(alborzbase.FlagColors[:], view) {
		return ContactList{}, echo.NewHTTPError(http.StatusBadRequest, "no such view")
	}
	group, category := ctx.QueryParam("group"), ctx.QueryParam("category")
	params := dav.RowParams(ctx, "/contacts", dav.ListParams(ctx, contactListParams...))

	only := dav.Only(ctx, "book")
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return ContactList{}, err
	}

	addressBookInfos, sites, err := visibleBooks(accounts, ctx.URLAccount(), only)
	if err != nil {
		return ContactList{}, err
	}

	query := contactsQuery(queryText)

	var aos, groups []AddressObject
	for _, result := range dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site dav.Site[*carddav.Client]) ([]carddav.AddressObject, error) {
		return site.Client.QueryAddressBook(ctx, site.Collection.Path, &query)
	}) {
		if result.Err != nil {
			return ContactList{}, fmt.Errorf("failed to query address book %s: %v", result.Site.Collection.Name, result.Err)
		}
		for i := range result.Value {
			ao := AddressObject{AddressObject: &result.Value[i], Account: result.Site.Collection.Account}
			// A group is a card, so it arrives with the contacts; it is
			// not one of them, and the list would read it as a person
			// with no address.
			if ao.IsGroup() {
				groups = append(groups, ao)
				continue
			}
			// A colour is a view, the way the mail rail's colours are:
			// one parameter, and the list is the search it names;
			// starred is any colour at all.
			if view == alborzbase.ViewStarred && ao.Color() == "" {
				continue
			}
			if view != "" && view != alborzbase.ViewStarred && ao.Color() != view {
				continue
			}
			aos = append(aos, ao)
		}
	}

	sorting, err := dav.Sort(ctx, aos, contactColumns(addressBookInfos), func(ao AddressObject) string {
		return strings.ToLower(ao.DisplayName())
	}, "query", "account")
	if err != nil {
		return ContactList{}, err
	}

	// A group names its members by UID, and the filter is the other way
	// round: which cards this group holds.
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
	// The words in use are the ones the rail offers: a filing scheme
	// nobody has written to has nothing to show.
	var words []string
	for _, ao := range aos {
		words = append(words, ao.Categories()...)
	}
	slices.Sort(words)
	words = slices.Compact(words)
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].DisplayName()) < strings.ToLower(groups[j].DisplayName())
	})

	items := make([]dav.Item, len(aos))
	for i, ao := range aos {
		items[i] = dav.Item{Path: ao.Path, URL: dav.ObjectURL("/contacts/", ao.Path, ao.Account, params)}
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

// contactColumns are the orders the contact list can be put in, by name
// unless asked.
func contactColumns(books []dav.Collection) []dav.Column[AddressObject] {
	return []dav.Column[AddressObject]{
		{Key: "name", Value: func(ao AddressObject) string { return strings.ToLower(ao.DisplayName()) }},
		{Key: alborzbase.ViewStarred, Value: func(ao AddressObject) string { return dav.StarredFirst(ao.Color()) }},
		{Key: "email", Value: func(ao AddressObject) string { return strings.ToLower(ao.Card.PreferredValue("EMAIL")) }},
		{Key: "phone", Value: func(ao AddressObject) string { return strings.ToLower(ao.Card.PreferredValue("TEL")) }},
		{Key: "account", Value: func(ao AddressObject) string { return strings.ToLower(ao.Account) }},
		{Key: "book", Value: func(ao AddressObject) string {
			if ab := dav.Holding(books, ao.Account, ao.Path); ab != nil {
				return strings.ToLower(ab.Name + "\x00" + ao.Account)
			}
			return dav.Last
		}},
		{Key: "changed", Value: func(ao AddressObject) string { return dav.When(cardModified(ao.AddressObject)) }},
	}
}

func (p *plugin) contacts(ctx *alborz.Context) error {
	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	addressBookInfos := list.Books
	hrefs := make(map[string]string, len(list.Items))
	for _, item := range list.Items {
		hrefs[item.Path] = item.URL
	}
	cards, pager := dav.Paginate(ctx, list.Cards)
	collection, href := dav.Labels(ctx, addressBookInfos, "/contacts", "book", nil)
	return ctx.Render(http.StatusOK, "address-book.html", &AddressBookRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.contacts")),
		AddressBooks:   addressBookInfos,
		AddressObjects: cards,
		Pager:          pager,
		HrefFor:        func(_, contactPath string) string { return hrefs[contactPath] },
		AddedBy:        p.dav.AddedBy(ctx.Session.Username()),
		Query:          list.Query,
		View:           list.View,
		Groups:         list.Groups,
		Categories:     list.Categories,
		Group:          list.Group,
		Category:       list.Category,
		Filters:        searchFilter(ctx, list),
		FilterRows:     alborzbase.ViewRows(ctx, list.View, false, ctx.WithoutParam("view")),
		Sorting:        list.Sorting,
		CollectionFor:  collection,
		CollectionHref: href,
	})
}

// refresh asks every signed-in account's server again, for a change
// made elsewhere that the poll has not caught up with.
// contactsQuery asks a book for what the list shows, narrowed to
// queryText when there is one.
func contactsQuery(queryText string) carddav.AddressBookQuery {
	// Every property: a server that honours a narrower request (Nextcloud
	// does) answers without the colour, the categories, the revision and
	// a group's members, and the list showed a star that was not there.
	query := carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
	}

	if queryText != "" {
		query.PropFilters = []carddav.PropFilter{
			{
				Name:        vcard.FieldFormattedName,
				TextMatches: []carddav.TextMatch{{Text: queryText}},
			},
			{
				Name:        vcard.FieldName,
				TextMatches: []carddav.TextMatch{{Text: queryText}},
			},
			{
				Name:        vcard.FieldNickname,
				TextMatches: []carddav.TextMatch{{Text: queryText}},
			},
			{
				Name:        vcard.FieldOrganization,
				TextMatches: []carddav.TextMatch{{Text: queryText}},
			},
			{
				Name:        vcard.FieldEmail,
				TextMatches: []carddav.TextMatch{{Text: queryText}},
			},
		}
	}
	return query
}

// searchFilter names the search a contact list was narrowed by, in the
// one shape every list uses.
func searchFilter(ctx *alborz.Context, list ContactList) []alborz.Filter {
	var out []alborz.Filter
	if f, ok := ctx.FilterOn("query", ctx.T("filter.search")); ok {
		out = append(out, f)
	}
	if f, ok := ctx.FilterOn("view", ctx.T("filter.color")); ok {
		if f.Value == alborzbase.ViewStarred {
			f.Value = ctx.T("mailbox.starred")
		} else {
			f.Value = ctx.T("color." + f.Value)
		}
		out = append(out, f)
	}
	if f, ok := ctx.FilterOn("category", ctx.T("filter.category")); ok {
		out = append(out, f)
	}
	// The book the page is in is a place, not a narrowing of one: the
	// rail marks it and the crumb names it, as a mail folder is named.
	if f, ok := ctx.FilterOn("group", ctx.T("filter.group")); ok {
		// The URL names a group by UID; the chip names it the way the
		// reader does.
		for _, g := range list.Groups {
			if g.UID() == f.Value {
				f.Value = g.DisplayName()
			}
		}
		out = append(out, f)
	}
	return out
}
