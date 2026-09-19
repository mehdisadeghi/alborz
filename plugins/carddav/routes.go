package alborzcarddav

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"golang.org/x/image/draw"
)

type AddressBookRenderData struct {
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
	// Groups are the group cards the accounts hold, and Categories the
	// words in use; the rail filters by either (RFC 6350 6.1.4, 6.7.1).
	Groups     []AddressObject
	Categories []string
	Group      string
	Category   string
}

// FilterRow is one narrowing the rail offers: what it is called, where
// it leads, and whether it is the one in force.
type FilterRow = dav.FilterRow

// GroupRows and CategoryRows are the two ways a contact belongs with
// others, as rail rows. The URL keeps the account it is scoped to and
// nothing else: a group and a colour narrow different things and one
// question at a time is enough.
func (d *AddressBookRenderData) GroupRows() []FilterRow {
	return contactGroupRows(d.Groups, d.Group, d.GlobalData.URLAccount)
}

func (d *AddressBookRenderData) CategoryRows() []FilterRow {
	return contactCategoryRows(d.Categories, d.Category, d.GlobalData.URLAccount)
}

// contactGroupRows and contactCategoryRows build the rail's narrowings
// the same way wherever they are drawn. The URL keeps the account it is
// scoped to and nothing else.
func contactGroupRows(groups []AddressObject, active, account string) []FilterRow {
	rows := make([]FilterRow, 0, len(groups))
	for _, g := range groups {
		uid := g.UID()
		if uid == "" {
			continue
		}
		edit := g.URL()
		if g.Account != "" {
			edit += "?account=" + alborz.AddressParam(g.Account)
		}
		rows = append(rows, FilterRow{
			Label:  g.DisplayName(),
			Href:   filterHref("group", uid, account),
			Active: uid == active,
			Edit:   edit,
		})
	}
	return rows
}

func contactCategoryRows(categories []string, active, account string) []FilterRow {
	rows := make([]FilterRow, 0, len(categories))
	for _, word := range categories {
		rows = append(rows, FilterRow{
			Label:  word,
			Href:   filterHref("category", word, account),
			Active: word == active,
		})
	}
	return rows
}

func filterHref(name, value, account string) string {
	q := url.Values{name: {value}}
	if account != "" {
		q.Set("account", account)
	}
	return "/contacts?" + q.Encode()
}

type Settings struct {
	AddressBookFilter   bool
	VisibleAddressBooks []string
}

const settingsKey = "carddav.settings"

// show ticks a book the account just made or accepted in the chosen set
// in force: what to see was chosen before it existed, which was not a
// choice to hide it.
func show(store alborz.Store, path string) error {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	if !settings.AddressBookFilter {
		return nil
	}
	settings.VisibleAddressBooks = append(settings.VisibleAddressBooks, path)
	return store.Put(settingsKey, settings)
}

// choose writes the books the reader ticked into the settings.
func choose(store alborz.Store, paths []string) error {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	settings.AddressBookFilter, settings.VisibleAddressBooks = true, paths
	return store.Put(settingsKey, settings)
}

func init() {
	alborz.KeepKey(settingsKey)
}

// contactListParams are what decide which contacts a list holds and in
// what order: a contact's page is opened with them and returns to them.
var contactListParams = []string{"account", "book", "query", "view", "group", "category", "sort", "dir", "page", "ipp"}

type AddressObjectRenderData struct {
	alborz.BaseRenderData
	Rail dav.Rail
	// List is the list the page was opened from, filter and order kept,
	// for what leaves the page with nothing to come back to.
	List string
	// Modified is when the card last changed: REV if the card carries
	// one (vCard 6.7.4), and the server's own last-modified otherwise.
	//
	// There is no Created. A vCard has no property for it - REV is the
	// last revision, not the first - and the only universal answer is
	// WebDAV's DAV:creationdate, which the object REPORT does not ask
	// for. Showing REV as "created" was wrong and said so on every
	// contact page.
	Modified      time.Time
	AddressBook   *dav.Collection
	AddressObject AddressObject
	Birthday      string    // the input format, for the edit form
	BirthdayDate  time.Time // the same day, for the page to write out
	Neighbours    dav.Neighbours
	// In are the groups naming this contact and Groups the ones that do
	// not, for the form that puts it in one.
	In     []AddressObject
	Groups []AddressObject
}

type UpdateAddressObjectRenderData struct {
	alborz.BaseRenderData
	Rail          dav.Rail
	Groups        []dav.Group
	AddressBook   *dav.Collection
	AddressObject *carddav.AddressObject // nil if creating a new contact
	Card          vcard.Card
	Name          string
	Error         string
	Birthday      string
	// Photo is the card's picture as it stands, so the form can show
	// what it is about to replace.
	Photo string
}

// photoLongestSide is what a contact picture is reduced to. The card
// carries it inline and base64 costs another third, so every fetch of
// the contact pays for whatever was uploaded - and a face in a list is
// shown at a few dozen pixels. The server's own limit is not the one
// that matters; ours is smaller on purpose.
const photoLongestSide = 320

// photoMaxUpload is what we are willing to decode at all. Beyond it the
// answer is no rather than a slow yes.
const photoMaxUpload = 12 << 20 // 12 MiB

// applyPhoto replaces or leaves the card's picture; removing it is its
// own request, since it is an act rather than a pending edit. What is
// uploaded is never what is stored: it is decoded, reduced to a size a
// contact list can use, and re-encoded as JPEG, so a card stays small
// enough to fetch on every visit.
// What applyPhoto can refuse; the form says it in the reader's language.
var (
	errPhotoTooLarge   = errors.New("carddav: photo over the upload limit")
	errPhotoUnreadable = errors.New("carddav: photo is not an image")
)

// photoQuality is the JPEG setting a card's picture is stored at: past
// it the bytes grow faster than a thumbnail-sized picture improves.
const photoQuality = 82

func applyPhoto(ctx *alborz.Context, card vcard.Card) error {
	file, err := ctx.FormFile("photo")
	if err != nil || file == nil || file.Size == 0 {
		return nil // nothing offered; whatever the card has, it keeps
	}
	if file.Size > photoMaxUpload {
		return errPhotoTooLarge
	}
	f, err := file.Open()
	if err != nil {
		return errPhotoUnreadable
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return errPhotoUnreadable
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > photoLongestSide || h > photoLongestSide {
		if w >= h {
			h = h * photoLongestSide / w
			w = photoLongestSide
		} else {
			w = w * photoLongestSide / h
			h = photoLongestSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: photoQuality}); err != nil {
		return errPhotoUnreadable
	}
	// vCard 4.0 carries the picture as a data URI; 3.0 as an encoded
	// property. The version is set to 4.0 above for anything we create,
	// and a synced 3.0 card keeps the shape its server gave it.
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	if card.Value(vcard.FieldVersion) == "3.0" {
		card.Set(vcard.FieldPhoto, &vcard.Field{
			Value:  encoded,
			Params: vcard.Params{"ENCODING": {"b"}, "TYPE": {"JPEG"}},
		})
		return nil
	}
	card.Set(vcard.FieldPhoto, &vcard.Field{Value: "data:image/jpeg;base64," + encoded})
	return nil
}

// cardModified is when the card last changed: the card's own REV where
// it has one, and the server's last-modified where it has not.
func cardModified(ao *carddav.AddressObject) time.Time {
	if rev := cardRevision(ao.Card); !rev.IsZero() {
		return rev
	}
	return ao.ModTime
}

// cardRevision reads REV (vCard 6.7.4), the moment the card itself says
// it last changed.
func cardRevision(card vcard.Card) time.Time {
	v := card.PreferredValue(vcard.FieldRevision)
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"20060102T150405Z", "20060102T150405-0700",
		time.RFC3339, "2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// birthdayDate parses BDAY into a day, so the page can spell it out in
// the reader's language and calendar instead of printing the stored
// digits. A vCard may carry a birthday with no year (--0412); that one
// has no date to give.
func birthdayDate(card vcard.Card) time.Time {
	v := birthdayValue(card)
	day, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}
	}
	return day
}

// birthdayValue renders BDAY in the HTML date-input format, accepting both
// the vCard 4.0 basic format (19850412) and the dashed 3.0 form.
func birthdayValue(card vcard.Card) string {
	v := card.PreferredValue(vcard.FieldBirthday)
	if len(v) == 8 {
		return v[:4] + "-" + v[4:6] + "-" + v[6:]
	}
	return v
}

// getAddressObject fetches one card without go-webdav's response
// parsing. Its populateAddressObject runs the ETag through
// strconv.Unquote, which rejects the weak form ("W/\"...\"") that
// Nextcloud sends, and every edit of a contact there died with a bare
// "invalid syntax". Nothing here needs the ETag: the PUT that saves the
// card sends no If-Match.
func getAddressObject(ctx *alborz.Context, c *carddav.Client, path string) (*carddav.AddressObject, error) {
	body, err := c.Open(ctx.Request().Context(), path)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	card, err := vcard.NewDecoder(body).Decode()
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: path, Card: card}, nil
}

// withValues rewrites a property's fields from the form, in order, on
// top of the fields the card already has, so a TYPE=work or a PREF a
// phone synced onto an address survives an edit here that did not
// touch it. The form lists the fields in the card's order, which is
// what makes the match by position right.
func withValues(fields []*vcard.Field, values []string) []*vcard.Field {
	var out []*vcard.Field
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if len(out) < len(fields) {
			field := fields[len(out)]
			field.Value = value
			out = append(out, field)
		} else {
			out = append(out, &vcard.Field{Value: value})
		}
	}
	return out
}

func registerRoutes(p *plugin) {
	guard := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return p.dav.Guarded(errNoAddressBook, "contacts.unconfigured", "/address-books/create", h)
	}
	GET := func(path string, h func(*alborz.Context) error) { p.GoPlugin.GET(path, guard(h)) }
	POST := func(path string, h func(*alborz.Context) error) { p.GoPlugin.POST(path, guard(h)) }
	POST("/contacts", dav.HandleChoose("/contacts", "book", choose))
	GET("/contacts", p.contacts)
	POST("/contacts/export", dav.HandleExport(p.client, "/contacts",
		func(ctx *alborz.Context) string { return ctx.T("nav.contacts") + ".vcf" }, joinCards))
	POST("/contacts/refresh", p.dav.HandleRefresh("/contacts"))
	GET("/contacts/:path", p.contact)

	GET("/contacts/:path/raw", func(ctx *alborz.Context) error { return dav.Raw(ctx, p.client) })
	page := p.collectionPage()
	GET("/address-books/create", page.HandleCreate(p.dav, p.createForm))
	POST("/address-books/create", page.HandleCreate(p.dav, p.createForm))
	GET("/contacts/import", page.HandleImportPage(p.dav, "/contacts", "nav.contacts", "contacts.import", "contacts.importhint", "contact", false))
	POST("/contacts/import", page.HandleImportPage(p.dav, "/contacts", "nav.contacts", "contacts.import", "contacts.importhint", "contact", false))
	GET("/address-books/:path", page.Handle(p.dav))
	POST("/address-books/:path", page.Handle(p.dav))
	POST("/address-books/:path/delete", page.HandleDelete(p.dav))
	POST("/address-books/:path/import", page.HandleImport(p.dav))
	GET("/address-books/:path/export", page.HandleExport(p.dav))
	POST("/address-books/:path/share", page.HandleShare(p.dav))
	POST("/address-books/:path/unshare", page.HandleUnshare(p.dav))
	POST("/address-books/:path/accept", page.HandleAnswer(p.dav, true))
	POST("/address-books/:path/decline", page.HandleAnswer(p.dav, false))
	p.Inject("*", p.dav.InjectInvitations("book", page.Base, "/contacts", "/address-books"))
	GET("/contacts/create", p.updateContact)
	POST("/contacts/create", p.updateContact)
	GET("/contacts/:path/edit", p.updateContact)
	POST("/contacts/:path/edit", p.updateContact)
	POST("/contacts/color", p.color)
	POST("/contacts/:path/group", p.groupContact)
	POST("/contacts/:path/color", p.color)
	POST("/contacts/:path/note", p.note)
	POST("/contacts/:path/photo/delete", p.deletePhoto)
	remove := dav.Handler(dav.Action[*carddav.Client]{Client: p.client, Do: dav.Delete[*carddav.Client], List: "/contacts"})
	POST("/contacts/:path/delete", remove)
	POST("/contacts/delete", remove)
	POST("/contacts/from-message", p.importFromMessage)
}

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
	params := dav.ListParams(ctx, contactListParams...)

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
	collection, href := dav.Labels(ctx, addressBookInfos, "/contacts", "book", nil)
	return ctx.Render(http.StatusOK, "address-book.html", &AddressBookRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.contacts")),
		AddressBooks:   addressBookInfos,
		AddressObjects: list.Cards,
		HrefFor:        func(_, contactPath string) string { return hrefs[contactPath] },
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
func (p *plugin) contact(ctx *alborz.Context) error {
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
	if err != nil {
		return err
	}
	// The object's path starts with its address book's.
	addressBook := dav.Holding(addressBooks, "", path)
	if addressBook == nil {
		addressBook = &addressBooks[0]
	}

	multiGet := carddav.AddressBookMultiGet{
		DataRequest: carddav.AddressDataRequest{
			AllProp: true,
		},
	}
	aos, err := c.MultiGetAddressBook(ctx.Request().Context(), path, &multiGet)
	if err != nil {
		return fmt.Errorf("failed to query CardDAV address: %v", err)
	}
	if len(aos) == 0 {
		return alborz.NotFound("notfound.contact")
	}
	if len(aos) != 1 {
		return fmt.Errorf("expected exactly one address object with path %q, got %v", path, len(aos))
	}
	ao := &aos[0]

	rail, err := p.bookRail(ctx)
	if err != nil {
		return err
	}
	// The list the page was opened from is rebuilt to find what stands
	// either side; the DAV reads behind it are cached, so the cost is
	// the sorting, not another round trip.
	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	// A group names its members; a contact says nothing about the
	// groups it is in, so the answer comes from reading them.
	object := AddressObject{AddressObject: ao}
	var in, rest []AddressObject
	for _, g := range list.Groups {
		if slices.Contains(g.Members(), object.UID()) {
			in = append(in, g)
		} else {
			rest = append(rest, g)
		}
	}
	return ctx.Render(http.StatusOK, "address-object.html", &AddressObjectRenderData{
		In:             in,
		Groups:         rest,
		Neighbours:     dav.Around(list.Items, path),
		List:           dav.ListURL("/contacts", dav.ListParams(ctx, contactListParams...)),
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(AddressObject{AddressObject: ao}.DisplayName()),
		AddressBook:    addressBook,
		AddressObject:  AddressObject{AddressObject: ao},
		Birthday:       birthdayValue(ao.Card),
		BirthdayDate:   birthdayDate(ao.Card),
		Modified:       cardModified(ao),
	})
}
func (p *plugin) updateContact(ctx *alborz.Context) error {
	addressObjectPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	var c *carddav.Client
	var addressBooks []dav.Collection
	var groups []dav.Group
	var ao *carddav.AddressObject
	var card vcard.Card

	var currentAddressBook *dav.Collection
	if addressObjectPath != "" {
		c, addressBooks, err = p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		ao, err = getAddressObject(ctx, c, addressObjectPath)
		if err != nil {
			return fmt.Errorf("failed to query CardDAV address: %v", err)
		}
		card = ao.Card
		currentAddressBook = dav.Holding(addressBooks, "", ao.Path)
	} else {
		// Creation is a pooled operation. The active account may quite
		// legitimately have no address book while another account does.
		groups, err = p.writableBookGroups(ctx)
		if err != nil {
			return err
		}
		if len(groups) == 0 || len(groups[0].Collections) == 0 {
			return alborz.RenderInfo(ctx, http.StatusOK, ctx.T("contacts.nowritable"))
		}
		card = make(vcard.Card)
		currentAddressBook = &groups[0].Collections[0]
	}

	if ctx.Request().Method == "POST" {
		fn := ctx.FormValue("fn")
		emails := strings.Split(ctx.FormValue("emails"), ",")

		reject := func(message string) error {
			rail, err := p.bookRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "update-address-object.html", &UpdateAddressObjectRenderData{
				Rail:           rail,
				BaseRenderData: *alborz.NewBaseRenderData(ctx),
				Groups:         groups,
				AddressBook:    currentAddressBook,
				AddressObject:  ao,
				Card:           card,
				Name:           fn,
				Birthday:       ctx.FormValue("bday"),
				Photo:          card.PreferredValue(vcard.FieldPhoto),
				Error:          message,
			})
		}
		if strings.TrimSpace(fn) == "" {
			return reject(ctx.T("form.nameneeded"))
		}

		to := dav.Ref[*carddav.Client]{Client: c}
		creating := ao == nil
		if creating {
			to, err = dav.Destination(ctx, ctx.FormValue("addressbook"), p.clientWithAddressBooks, func(book dav.Collection) bool { return book.Writable })
			if errors.Is(err, dav.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		if _, ok := card[vcard.FieldVersion]; !ok {
			// Default to vCard 4.0
			card.SetValue(vcard.FieldVersion, "4.0")
		}

		if field := card.Preferred(vcard.FieldFormattedName); field != nil {
			field.Value = fn
		} else {
			card.Add(vcard.FieldFormattedName, &vcard.Field{Value: fn})
		}

		// TODO: Google wants a "N" field, fails with a 400 otherwise

		if fields := withValues(card[vcard.FieldEmail], emails); len(fields) > 0 {
			card[vcard.FieldEmail] = fields
		} else {
			delete(card, vcard.FieldEmail)
		}
		if fields := withValues(card[vcard.FieldTelephone], strings.Split(ctx.FormValue("tels"), ",")); len(fields) > 0 {
			card[vcard.FieldTelephone] = fields
		} else {
			delete(card, vcard.FieldTelephone)
		}

		// An empty form value removes the property.
		setValue := func(key, value string) {
			if value == "" {
				delete(card, key)
			} else if field := card.Preferred(key); field != nil {
				field.Value = value
			} else {
				card.Add(key, &vcard.Field{Value: value})
			}
		}
		setValue(vcard.FieldOrganization, strings.TrimSpace(ctx.FormValue("org")))
		setValue(vcard.FieldTitle, strings.TrimSpace(ctx.FormValue("title")))
		birthday := strings.TrimSpace(ctx.FormValue("bday"))
		if birthday != "" {
			day, err := ctx.ReadDate(birthday, time.UTC)
			if err != nil {
				return reject(ctx.T("form.birthday"))
			}
			birthday = day.Format("20060102")
		}
		setValue(vcard.FieldBirthday, birthday)
		setValue(vcard.FieldURL, strings.TrimSpace(ctx.FormValue("url")))
		setValue(vcard.FieldNote, strings.TrimSpace(ctx.FormValue("note")))
		// CATEGORIES is a comma list (RFC 6350 6.7.1), which is what the
		// field asks for and what every other client shows.
		var words []string
		for _, word := range strings.Split(ctx.FormValue("categories"), ",") {
			if word = strings.TrimSpace(word); word != "" {
				words = append(words, word)
			}
		}
		SetCategories(card, words)

		if err := applyPhoto(ctx, card); err != nil {
			switch {
			case errors.Is(err, errPhotoTooLarge):
				return reject(ctx.T("form.phototoolarge"))
			case errors.Is(err, errPhotoUnreadable):
				return reject(ctx.T("form.photounreadable"))
			}
			return err
		}

		// Free-form address lives in the street component; other
		// structured components from synced cards are preserved.
		street := strings.TrimSpace(ctx.FormValue("adr"))
		if adr := card.Address(); adr != nil {
			if street == "" {
				delete(card, vcard.FieldAddress)
			} else {
				adr.StreetAddress = street
				card.SetAddress(adr)
			}
		} else if street != "" {
			card.AddAddress(&vcard.Address{StreetAddress: street})
		}

		id := uuid.New()
		if _, ok := card[vcard.FieldUID]; !ok {
			card.SetValue(vcard.FieldUID, id.URN())
		}

		held, etag := "", ""
		if !creating {
			held, etag = ao.Path, ao.ETag
		}
		at, ifMatch, ifNoneMatch := dav.Target(to.Path, id.String()+".vcf", held, etag)
		ao, err = to.Client.PutAddressObject(ctx.Request().Context(), at, card,
			&carddav.PutAddressObjectOptions{IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}
		// The card as written names the contact; what a PUT answers
		// carries the path and no data at all.
		named := AddressObject{AddressObject: &carddav.AddressObject{Path: ao.Path, Card: card}}
		return dav.Saved(ctx, creating, ctx.T("notice.contactcreated"), named.DisplayName(), named.URL(), "/contacts", to.Account)
	}

	// Both map values would be evaluated eagerly; a missing object
	// must not reach DisplayName.
	name := ""
	if ao != nil {
		name = AddressObject{AddressObject: ao}.DisplayName()
	}
	rail, err := p.bookRail(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "update-address-object.html", &UpdateAddressObjectRenderData{
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Groups:         groups,
		AddressBook:    currentAddressBook,
		AddressObject:  ao,
		Card:           card,
		Name:           name,
		Birthday:       birthdayValue(card),
		Photo:          card.PreferredValue(vcard.FieldPhoto),
	})
}

// A line added where the contact is read, not behind the edit form.
func (p *plugin) note(ctx *alborz.Context) error {
	note := strings.TrimSpace(ctx.FormValue("note"))
	return dav.Run(ctx, dav.Action[*carddav.Client]{Client: p.client, List: "/contacts",
		Do: func(ctx *alborz.Context, ref dav.Ref[*carddav.Client]) error {
			if note == "" {
				return nil
			}
			return changeCard(ctx, ref, func(card vcard.Card) {
				text := alborz.AppendNote(card.Value(vcard.FieldNote), note, time.Now())
				if field := card.Preferred(vcard.FieldNote); field != nil {
					field.Value = text
				} else {
					card.Add(vcard.FieldNote, &vcard.Field{Value: text})
				}
			})
		}})
}

// Removing the picture is its own request, not a tick the edit form
// carries: a submit button inside that form would be the one Enter
// reaches from any field, and pressing Enter in a name is not a
// request to delete a photo. Idempotent - a contact with no picture
// is what it leaves either way.
func (p *plugin) deletePhoto(ctx *alborz.Context) error {
	return dav.Run(ctx, dav.Action[*carddav.Client]{Client: p.client, List: "/contacts",
		Do: func(ctx *alborz.Context, ref dav.Ref[*carddav.Client]) error {
			return changeCard(ctx, ref, func(card vcard.Card) { delete(card, vcard.FieldPhoto) })
		}})
}

// createForm is the form for a new address book. The first book is made
// from a rail that lists none.
func (p *plugin) createForm(ctx *alborz.Context) (dav.CreateForm, error) {
	rail, err := p.bookRail(ctx)
	if err != nil && !errors.Is(err, errNoAddressBook) {
		return dav.CreateForm{}, err
	}
	return dav.CreateForm{Rail: rail, Title: ctx.T("contacts.newbook"), Section: ctx.T("nav.contacts"), List: "/contacts",
		Made: func(string) (string, string) { return ctx.T("notice.bookcreated"), "/contacts" }}, nil
}

// color marks contacts, from their own page, their row or the list's
// toolbar.
func (p *plugin) color(ctx *alborz.Context) error {
	return dav.Star(ctx, p.client, "/contacts", func(ctx *alborz.Context, ref dav.Ref[*carddav.Client], name string) (was string, err error) {
		err = changeCard(ctx, ref, func(card vcard.Card) {
			was = CardColor(card)
			SetColor(card, name)
		})
		return was, err
	})
}

// groupContact adds a contact to a group or takes it out of one. What
// changes is the group's card - a group names its members (RFC 6350
// 6.6.5), a contact says nothing about its groups - except that a card
// with no UID is given one first, since a member is named by UID.
func (p *plugin) groupContact(ctx *alborz.Context) error {
	objPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}
	c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
	if err != nil {
		return err
	}
	ao, err := getAddressObject(ctx, c, objPath)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	contact := AddressObject{AddressObject: ao}
	uid := contact.UID()
	if uid == "" {
		uid = uuid.New().String()
		ao.Card.SetValue(vcard.FieldUID, memberPrefix+uid)
		if err := writeCard(ctx, c, ao.Path, ao.Card); err != nil {
			ctx.Notify(dav.Refused(ctx, err))
			return ctx.Redirect(http.StatusFound, ctx.NextOr(contact.URL()))
		}
	}

	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	back := ctx.NextOr(ctx.AccountPath(contact.URL()))
	if leaving := ctx.FormValue("leave"); leaving != "" {
		for _, g := range list.Groups {
			if g.UID() != leaving {
				continue
			}
			kept := slices.DeleteFunc(g.Members(), func(m string) bool { return m == uid })
			SetMembers(g.Card, kept)
			if err := p.putCard(ctx, c, g.Path, g.Card); err != nil {
				return err
			}
		}
		return ctx.Redirect(http.StatusFound, back)
	}

	if name := strings.TrimSpace(ctx.FormValue("newgroup")); name != "" {
		card := vcard.Card{}
		card.SetValue(vcard.FieldVersion, "4.0")
		card.SetValue(vcard.FieldFormattedName, name)
		card.SetValue(vcard.FieldUID, memberPrefix+uuid.New().String())
		card.SetKind(vcard.KindGroup)
		SetMembers(card, []string{uid})
		// The group is made where the contact is, which is the only
		// book the page knows anything about.
		book := dav.Holding(books, "", ao.Path)
		if book == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "the contact is in no address book")
		}
		if err := p.putCard(ctx, c, path.Join(book.Path, uuid.New().String()+".vcf"), card); err != nil {
			return err
		}
		return ctx.Redirect(http.StatusFound, back)
	}

	joining := ctx.FormValue("group")
	for _, g := range list.Groups {
		if g.UID() != joining || slices.Contains(g.Members(), uid) {
			continue
		}
		SetMembers(g.Card, append(g.Members(), uid))
		if err := p.putCard(ctx, c, g.Path, g.Card); err != nil {
			return err
		}
	}
	return ctx.Redirect(http.StatusFound, back)
}

// putCard writes a card with a fresh revision, and says on the page
// where a server refused it rather than answering with an error page.
func (p *plugin) putCard(ctx *alborz.Context, c *carddav.Client, at string, card vcard.Card) error {
	if err := writeCard(ctx, c, at, card); err != nil {
		ctx.Notify(dav.Refused(ctx, err))
	}
	return nil
}

func writeCard(ctx *alborz.Context, c *carddav.Client, at string, card vcard.Card) error {
	card.SetValue(vcard.FieldRevision, time.Now().UTC().Format("20060102T150405Z"))
	_, err := c.PutAddressObject(ctx.Request().Context(), at, card, nil)
	return err
}

// changeCard reads one card, lets change rewrite it, and writes it back
// with a fresh REV. The read is deliberately not go-webdav's: see
// getAddressObject for the ETag it cannot parse.
func changeCard(ctx *alborz.Context, ref dav.Ref[*carddav.Client], change func(vcard.Card)) error {
	ao, err := getAddressObject(ctx, ref.Client, ref.Path)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	change(ao.Card)
	return writeCard(ctx, ref.Client, ao.Path, ao.Card)
}

// collectionPage is an address book's own page.
func (p *plugin) collectionPage() dav.Page {
	return dav.Page{
		Base:   "/address-books/",
		List:   "/contacts",
		Color:  addressBookColor,
		Ext:    ".vcf",
		Forget: p.dav.Forget,
		Show:   show,
		Rail:   func(ctx *alborz.Context, _ string) (dav.Rail, error) { return p.bookRail(ctx) },
		Import: func(ctx *alborz.Context, path string, raw []byte) (int, error) {
			c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return 0, err
			}
			return importBook(ctx.Request().Context(), c, path, raw)
		},
		Export: func(ctx *alborz.Context, path string, _, _ time.Time) ([]byte, error) {
			c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return nil, err
			}
			return exportBook(ctx.Request().Context(), c, path)
		},
		Lookup: func(ctx *alborz.Context, path string) (dav.Collection, func() int, string, string, error) {
			_, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(books, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			return *info, p.dav.CountObjects(ctx, info.Path), "/contacts", ctx.T("nav.contacts"), nil
		},
	}
}

// bookRail is the section's rail for a page that is not its list,
// listing every account's address books.
func (p *plugin) bookRail(ctx *alborz.Context) (dav.Rail, error) {
	rail := dav.Rail{Path: "/contacts", Field: "book", ItemClass: "addressbook-item", Action: "/contacts", EditHref: "/address-books/",
		NewHref: "/address-books/create?next=" + url.QueryEscape(ctx.Request().URL.RequestURI()), NewLabel: ctx.T("contacts.newbook"),
		ImportHref: "/contacts/import", ImportLabel: ctx.T("contacts.import")}
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return rail, err
	}
	infos, _, err := visibleBooks(accounts, ctx.URLAccount(), nil)
	rail.Items = infos
	if err != nil {
		return rail, err
	}
	// Groups and categories are narrowings of the list, so the rail
	// carries them wherever the list's pages are (the list itself, an
	// object, its edit). The reads behind this are cached.
	list, err := p.contactList(ctx)
	if err != nil {
		return rail, err
	}
	rail.Groups = contactGroupRows(list.Groups, list.Group, ctx.URLAccount())
	rail.Categories = contactCategoryRows(list.Categories, list.Category, ctx.URLAccount())
	return rail, nil
}

// visibleBooks are dav.Visible's address books.
func visibleBooks(accounts []dav.Account[*carddav.Client], scope string, only map[string]bool) ([]dav.Collection, []dav.Site[*carddav.Client], error) {
	return dav.Visible(accounts, scope, only, func(acc dav.Account[*carddav.Client]) ([]dav.Collection, bool, []string, error) {
		settings := &Settings{}
		if err := acc.Session.Store().Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return nil, false, nil, fmt.Errorf("failed to load CardDAV settings: %w", err)
		}
		return acc.Collections, settings.AddressBookFilter, settings.VisibleAddressBooks, nil
	})
}

// joinCards writes the cards one after another, which is all a file of
// vCards is.
func joinCards(cards [][]byte) ([]byte, error) {
	var buf bytes.Buffer
	for _, card := range cards {
		buf.Write(card)
		if !bytes.HasSuffix(card, []byte("\n")) {
			buf.WriteString("\r\n")
		}
	}
	return buf.Bytes(), nil
}

// hasAttachment reports whether the message carries a part of one of
// the media types.
func hasAttachment(msg *alborzbase.IMAPMessage, types ...string) bool {
	for _, att := range msg.Attachments() {
		for _, t := range types {
			if strings.EqualFold(att.MIMEType, t) {
				return true
			}
		}
	}
	return false
}

// importFromMessage files the cards attached to a mail into the chosen
// address book.
func (p *plugin) importFromMessage(ctx *alborz.Context) error {
	mboxName, uid, err := alborzbase.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	raw, _, err := alborzbase.PartAt(ctx, mboxName, uid, ctx.FormValue("part"))
	if err != nil {
		return err
	}
	acct, bookPath, ok := strings.Cut(ctx.FormValue("addressbook"), "|")
	session := ctx.SessionFor(acct)
	if !ok || session == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "no address book to file it in")
	}
	c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), session)
	if err != nil {
		return err
	}
	if book := dav.At(books, bookPath); book == nil || !book.Writable {
		return echo.NewHTTPError(http.StatusBadRequest, "no address book to file it in")
	}
	n, err := importBook(ctx.Request().Context(), c, bookPath, raw)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import.vcf", n))
	}
	to := "/contacts"
	if acct != ctx.Session.Username() {
		to += "?account=" + alborz.AddressParam(acct)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
}

// importBook writes every card of a vCard stream into the book and
// reports how many. A UID the book already holds is written over.
func importBook(ctx context.Context, client *carddav.Client, bookPath string, raw []byte) (int, error) {
	dec := vcard.NewDecoder(bytes.NewReader(raw))
	n := 0
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("failed to read the card: %v", err)
		}
		if _, ok := card[vcard.FieldVersion]; !ok {
			card.SetValue(vcard.FieldVersion, "4.0")
		}
		uid := card.Value(vcard.FieldUID)
		if uid == "" {
			uid = uuid.New().URN()
			card.SetValue(vcard.FieldUID, uid)
		}
		target := path.Join(bookPath, dav.SafeObjectName(uid)+".vcf")
		if _, err := client.PutAddressObject(ctx, target, card, nil); err != nil {
			existing := cardPathByUID(ctx, client, bookPath, uid)
			if existing == "" {
				return n, fmt.Errorf("failed to write %s: %v", uid, err)
			}
			if _, err := client.PutAddressObject(ctx, existing, card, nil); err != nil {
				return n, fmt.Errorf("failed to write %s: %v", uid, err)
			}
		}
		n++
	}
}

// cardPathByUID is where the book keeps the card with that UID, "" when
// it has none.
func cardPathByUID(ctx context.Context, client *carddav.Client, bookPath, uid string) string {
	objects, err := client.QueryAddressBook(ctx, bookPath, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldUID}},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldUID, TextMatches: []carddav.TextMatch{{Text: uid, MatchType: carddav.MatchEquals}}}},
	})
	if err != nil || len(objects) == 0 {
		return ""
	}
	return objects[0].Path
}

// exportBook renders every card of the book as one vCard file.
func exportBook(ctx context.Context, client *carddav.Client, bookPath string) ([]byte, error) {
	objects, err := client.QueryAddressBook(ctx, bookPath, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read the address book: %v", err)
	}
	var buf bytes.Buffer
	enc := vcard.NewEncoder(&buf)
	for _, obj := range objects {
		if err := enc.Encode(obj.Card); err != nil {
			return nil, fmt.Errorf("failed to write a card: %v", err)
		}
	}
	return buf.Bytes(), nil
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

// warm fetches what the contacts page asks for first, as the account
// is signed in, so the first click finds it cached: the books and
// every card the list shows.
func (p *plugin) warm(ctx *alborz.Context, s *alborz.Session) {
	p.dav.Warm(ctx, s, func(bg context.Context) error { return p.warmAccount(bg, s) })
}

func (p *plugin) warmAccount(ctx context.Context, s *alborz.Session) error {
	c, books, err := p.clientWithAddressBooks(ctx, s)
	if err != nil {
		return err
	}
	query := contactsQuery("")
	for _, r := range dav.Each(ctx, books, func(ctx context.Context, book dav.Collection) (int, error) {
		_, err := c.QueryAddressBook(ctx, book.Path, &query)
		return 0, err
	}) {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
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
	if f, ok := ctx.FilterOn("book", ctx.T("contacts.book")); ok {
		// The URL names a book by path; the chip names it the way the
		// reader does.
		for _, ab := range list.Books {
			if ab.Path == dav.CanonicalCollectionPath(f.Value) {
				f.Value = ab.Name
			}
		}
		out = append(out, f)
	}
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
