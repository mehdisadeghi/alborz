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
	"mime"
	"net/http"
	"path"
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
	AddressBooks   []AddressBookInfo
	AddressObjects []AddressObject
	Sort, SortDir  string
	Query          string
	ColorForPath   func(account, path string) string
	// BookForPath names the address book a contact is in, so the list
	// can carry ownership on the collection rather than beside the name.
	BookForPath func(account, path string) string
}

type Settings struct {
	AddressBookFilter   bool
	VisibleAddressBooks []string
}

const settingsKey = "carddav.settings"

type AddressObjectRenderData struct {
	alborz.BaseRenderData
	// Modified is when the card last changed: REV if the card carries
	// one (vCard 6.7.4), and the server's own last-modified otherwise.
	//
	// There is no Created. A vCard has no property for it - REV is the
	// last revision, not the first - and the only universal answer is
	// WebDAV's DAV:creationdate, which the object REPORT does not ask
	// for. Showing REV as "created" was wrong and said so on every
	// contact page.
	Modified      time.Time
	AddressBook   *AddressBookInfo
	AddressObject AddressObject
	Birthday      string    // the input format, for the edit form
	BirthdayDate  time.Time // the same day, for the page to write out
}

type UpdateAddressObjectRenderData struct {
	alborz.BaseRenderData
	Groups        []dav.Group[AddressBookInfo]
	AddressBook   *AddressBookInfo
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

// onlyBooks is the set a URL asks to see, standing in for the stored
// visibility for that request alone: the URL carries the question and
// stored state carries the preference, so looking at one address book is
// a link rather than a setting somebody has to put back.
func onlyBooks(ctx *alborz.Context) map[string]bool {
	values := ctx.QueryParams()["book"]
	if len(values) == 0 {
		return nil
	}
	only := make(map[string]bool, len(values))
	for _, v := range values {
		only[dav.CanonicalCollectionPath(v)] = true
	}
	return only
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

func addressBookByPath(addressBooks []AddressBookInfo, path string) *AddressBookInfo {
	for i := range addressBooks {
		if addressBooks[i].Path == path {
			return &addressBooks[i]
		}
	}
	return nil
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
	// A missing address book is a state to explain, not an error: every
	// route in the section answers with the account named.
	guard := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return func(ctx *alborz.Context) error {
			err := h(ctx)
			if errors.Is(err, errNoAddressBook) {
				return alborz.RenderInfo(ctx, http.StatusOK,
					fmt.Sprintf(ctx.T("contacts.unconfigured"), ctx.Session.Username()))
			}
			return err
		}
	}
	GET := func(path string, h func(*alborz.Context) error) { p.GoPlugin.GET(path, guard(h)) }
	POST := func(path string, h func(*alborz.Context) error) { p.GoPlugin.POST(path, guard(h)) }
	POST("/contacts", p.chooseBooks)
	GET("/contacts", p.contacts)
	POST("/contacts/export", p.exportContacts)
	GET("/contacts/:path", p.contact)

	GET("/contacts/:path/raw", p.rawContact)
	GET("/address-books/create", handleCreateBook(p))
	POST("/address-books/create", handleCreateBook(p))
	page := p.collectionPage()
	GET("/address-books/:path", page.Handle(p.dav))
	POST("/address-books/:path", page.Handle(p.dav))
	POST("/address-books/:path/delete", page.HandleDelete(p.dav))
	POST("/address-books/:path/import", page.HandleImport(p.dav))
	GET("/address-books/:path/export", page.HandleExport(p.dav))
	GET("/contacts/create", p.updateContact)
	POST("/contacts/create", p.updateContact)
	GET("/contacts/:path/edit", p.updateContact)
	POST("/contacts/:path/edit", p.updateContact)
	POST("/contacts/:path/note", p.noteContact)
	POST("/contacts/:path/photo/delete", p.deletePhoto)
	POST("/contacts/:path/delete", p.deleteContact)
	POST("/contacts/delete", p.deleteContacts)
	POST("/contacts/import", p.importFromMessage)
}

func (p *plugin) chooseBooks(ctx *alborz.Context) error {
	settings := &Settings{}
	if err := ctx.Session.Store().Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return fmt.Errorf("failed to load CardDAV settings: %w", err)
	}
	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	settings.AddressBookFilter = true
	settings.VisibleAddressBooks = params["book"]
	if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
		return fmt.Errorf("failed to save CardDAV settings: %w", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
}
func (p *plugin) contacts(ctx *alborz.Context) error {
	queryText := ctx.QueryParam("query")

	only := onlyBooks(ctx)
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return err
	}

	addressBookInfos, sites, err := visibleBooks(accounts, only)
	if err != nil {
		return err
	}

	query := carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{
			Props: []string{
				vcard.FieldFormattedName,
				vcard.FieldName,
				vcard.FieldEmail,
				vcard.FieldTelephone,
				vcard.FieldNickname,
				vcard.FieldOrganization,
				vcard.FieldPhoto,
				vcard.FieldUID,
			},
		},
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

	var aos []AddressObject
	for _, result := range dav.Each(ctx.Request().Context(), sites, func(ctx context.Context, site bookSite) ([]carddav.AddressObject, error) {
		return site.client.QueryAddressBook(ctx, site.path, &query)
	}) {
		if result.Err != nil {
			return fmt.Errorf("failed to query address book %s: %v", result.Site.name, result.Err)
		}
		for i := range result.Value {
			aos = append(aos, AddressObject{AddressObject: &result.Value[i], Account: result.Site.account})
		}
	}

	sortKey := ctx.QueryParam("sort")
	switch sortKey {
	case "", "name", "email", "phone", "account", "book", "changed":
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort order")
	}
	sortDir := ctx.QueryParam("dir")
	if sortDir != "" && sortDir != "asc" && sortDir != "desc" {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort direction")
	}
	key := func(ao AddressObject) string {
		switch sortKey {
		case "email":
			return strings.ToLower(ao.Card.PreferredValue("EMAIL"))
		case "phone":
			return strings.ToLower(ao.Card.PreferredValue("TEL"))
		case "account":
			return strings.ToLower(ao.Account)
		case "book":
			for _, ab := range addressBookInfos {
				if ab.Account == ao.Account && strings.HasPrefix(ao.Path, ab.Path) {
					return strings.ToLower(ab.Name + "\x00" + ao.Account)
				}
			}
			return "\uffff"
		case "changed":
			// A card that says nothing about its age sorts after the
			// ones that do, in ascending order.
			t := cardModified(ao.AddressObject)
			if t.IsZero() {
				return "\uffff"
			}
			return t.Format(time.RFC3339)
		}
		return strings.ToLower(ao.DisplayName())
	}
	sort.SliceStable(aos, func(i, j int) bool {
		a, b := key(aos[i]), key(aos[j])
		if a == b {
			return strings.ToLower(aos[i].DisplayName()) < strings.ToLower(aos[j].DisplayName())
		}
		if sortDir == "desc" {
			return a > b
		}
		return a < b
	})

	return ctx.Render(http.StatusOK, "address-book.html", &AddressBookRenderData{
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.contacts")),
		AddressBooks:   addressBookInfos,
		AddressObjects: aos,
		Query:          queryText,
		Sort:           sortKey,
		SortDir:        sortDir,
		ColorForPath: func(account, contactPath string) string {
			for _, ab := range addressBookInfos {
				if ab.Account == account && strings.HasPrefix(contactPath, ab.Path) {
					return ab.Color
				}
			}
			return ""
		},
		BookForPath: func(account, contactPath string) string {
			for _, ab := range addressBookInfos {
				if ab.Account == account && strings.HasPrefix(contactPath, ab.Path) {
					return ab.Name
				}
			}
			return ""
		},
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
	addressBook := &addressBooks[0]
	for i := range addressBooks {
		if strings.HasPrefix(path, addressBooks[i].Path) {
			addressBook = &addressBooks[i]
			break
		}
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
		return alborz.NotFoundf("no such contact")
	}
	if len(aos) != 1 {
		return fmt.Errorf("expected exactly one address object with path %q, got %v", path, len(aos))
	}
	ao := &aos[0]

	return ctx.Render(http.StatusOK, "address-object.html", &AddressObjectRenderData{
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
	var addressBooks []AddressBookInfo
	var groups []dav.Group[AddressBookInfo]
	var ao *carddav.AddressObject
	var card vcard.Card

	var currentAddressBook *AddressBookInfo
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
		for i := range addressBooks {
			if strings.HasPrefix(ao.Path, addressBooks[i].Path) {
				currentAddressBook = &addressBooks[i]
				break
			}
		}
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
		addressBookPath := ctx.FormValue("addressbook")

		reject := func(message string) error {
			return ctx.Render(http.StatusUnprocessableEntity, "update-address-object.html", &UpdateAddressObjectRenderData{
				BaseRenderData: *alborz.NewBaseRenderData(ctx),
				Groups:         groups,
				AddressBook:    currentAddressBook,
				AddressObject:  ao,
				Card:           card,
				Name:           fn,
				Birthday:       birthdayValue(card),
				Photo:          card.PreferredValue(vcard.FieldPhoto),
				Error:          message,
			})
		}
		if strings.TrimSpace(fn) == "" {
			return reject(ctx.T("form.nameneeded"))
		}

		saveClient := c
		var createAcct string
		if ao == nil {
			// The form's choice names its owner as "account|path".
			acct, bookPath, ok := strings.Cut(addressBookPath, "|")
			session := ctx.SessionFor(acct)
			if !ok || session == nil {
				return reject(ctx.T("form.destinationneeded"))
			}
			c2, books2, err := p.clientWithAddressBooks(ctx.Request().Context(), session)
			if err != nil {
				return err
			}
			var w2 []AddressBookInfo
			for _, ab := range books2 {
				if ab.Writable {
					w2 = append(w2, ab)
				}
			}
			if addressBookByPath(w2, bookPath) == nil {
				return reject(ctx.T("form.destinationneeded"))
			}
			saveClient, addressBookPath, createAcct = c2, bookPath, acct
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
		setValue(vcard.FieldBirthday, strings.ReplaceAll(ctx.FormValue("bday"), "-", ""))
		setValue(vcard.FieldURL, strings.TrimSpace(ctx.FormValue("url")))
		setValue(vcard.FieldNote, strings.TrimSpace(ctx.FormValue("note")))

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

		var savePath string
		if ao != nil {
			savePath = ao.Path
		} else {
			savePath = path.Join(addressBookPath, id.String()+".vcf")
		}
		ao, err = saveClient.PutAddressObject(ctx.Request().Context(), savePath, card)
		if err != nil {
			return fmt.Errorf("failed to put address object: %v", err)
		}

		return func() error {
			if createAcct != "" {
				return ctx.Redirect(http.StatusFound, AddressObject{AddressObject: ao}.URL()+"?account="+alborz.AddressParam(createAcct))
			}
			return ctx.Redirect(http.StatusFound, ctx.AccountPath(AddressObject{AddressObject: ao}.URL()))
		}()
	}

	// Both map values would be evaluated eagerly; a missing object
	// must not reach DisplayName.
	name := ""
	if ao != nil {
		name = AddressObject{AddressObject: ao}.DisplayName()
	}
	return ctx.Render(http.StatusOK, "update-address-object.html", &UpdateAddressObjectRenderData{
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

// The card exactly as the server stores it; see the calendar's raw
// view for why it is not parsed.
func (p *plugin) rawContact(ctx *alborz.Context) error {
	path, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}
	c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
	if err != nil {
		return err
	}
	body, err := c.Open(ctx.Request().Context(), path)
	if err != nil {
		return err
	}
	defer body.Close()
	// Saved, it is the file other clients open; read, it is text.
	if ctx.QueryParam("save") == "1" {
		ctx.Response().Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": path[strings.LastIndex(path, "/")+1:]}))
		return ctx.Stream(http.StatusOK, "text/vcard; charset=utf-8", body)
	}
	return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", body)
}

// A line added where the contact is read, not behind the edit form.
func (p *plugin) noteContact(ctx *alborz.Context) error {
	note := strings.TrimSpace(ctx.FormValue("note"))
	if note == "" {
		return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/contacts")))
	}
	return editCard(ctx, p, func(card vcard.Card) {
		text := alborz.AppendNote(card.Value(vcard.FieldNote), note, time.Now())
		if field := card.Preferred(vcard.FieldNote); field != nil {
			field.Value = text
		} else {
			card.Add(vcard.FieldNote, &vcard.Field{Value: text})
		}
	})
}

// Removing the picture is its own request, not a tick the edit form
// carries: a submit button inside that form would be the one Enter
// reaches from any field, and pressing Enter in a name is not a
// request to delete a photo. Idempotent - a contact with no picture
// is what it leaves either way.
func (p *plugin) deletePhoto(ctx *alborz.Context) error {
	return editCard(ctx, p, func(card vcard.Card) {
		delete(card, vcard.FieldPhoto)
	})
}
func (p *plugin) deleteContact(ctx *alborz.Context) error {
	objPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, err := p.client(ctx.Session)
	if err != nil {
		return err
	}

	if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
		return fmt.Errorf("failed to delete address object: %v", err)
	}

	return ctx.Redirect(http.StatusFound, ctx.AccountPath("/contacts"))
}
func (p *plugin) deleteContacts(ctx *alborz.Context) error {
	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	refs, err := dav.Selected(ctx, params["paths"], p.client)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := ref.Client.RemoveAll(ctx.Request().Context(), ref.Path); err != nil {
			return fmt.Errorf("failed to delete address object: %v", err)
		}
	}

	return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
}

func handleCreateBook(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		data := &dav.NewCollectionRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("contacts.newbook")),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Session.Username(),
			Color:          dav.DefaultColor,
			Title:          ctx.T("contacts.newbook"),
			ListHref:       "/contacts",
			BackLabel:      ctx.T("nav.contacts"),
			Next:           ctx.FormValue("next"),
		}
		if ctx.Request().Method != http.MethodPost {
			return ctx.Render(http.StatusOK, "create-collection.html", data)
		}

		data.Name = strings.TrimSpace(ctx.FormValue("name"))
		data.Color = ctx.FormValue("color")
		if account := ctx.FormValue("account"); account != "" {
			data.Account = account
		}
		if data.Name == "" {
			data.Error = ctx.T("form.nameneeded")
			return ctx.Render(http.StatusUnprocessableEntity, "create-collection.html", data)
		}
		session := ctx.SessionFor(data.Account)
		if session == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
		}
		if err := p.createAddressBook(ctx.Request().Context(), session, data.Name, data.Color); err != nil {
			data.Error = err.Error()
			return ctx.Render(http.StatusUnprocessableEntity, "create-collection.html", data)
		}
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
	}
}

// editCard reads one card, lets the caller change it, and writes it
// back with a fresh REV. The read is deliberately not go-webdav's: see
// getAddressObject for the ETag it cannot parse.
func editCard(ctx *alborz.Context, p *plugin, change func(vcard.Card)) error {
	objPath, err := dav.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}
	c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
	if err != nil {
		return err
	}
	ao, err := getAddressObject(ctx, c, objPath)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	change(ao.Card)
	ao.Card.SetValue(vcard.FieldRevision, time.Now().UTC().Format("20060102T150405Z"))
	if _, err := c.PutAddressObject(ctx.Request().Context(), ao.Path, ao.Card); err != nil {
		return fmt.Errorf("failed to save the contact: %v", err)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/contacts")))
}

// collectionPage is an address book's own page.
func (p *plugin) collectionPage() dav.Page {
	return dav.Page{
		Base:   "/address-books/",
		Color:  addressBookColor,
		Ext:    ".vcf",
		Forget: p.books.Forget,
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
			info := addressBookByPath(books, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFoundf("no such collection")
			}
			return info.Collection, p.dav.CountObjects(ctx, info.Path), "/contacts", ctx.T("nav.contacts"), nil
		},
	}
}

// bookSite is one address book to query, with its account's client.
type bookSite struct {
	account string
	client  *carddav.Client
	path    string
	name    string
}

// visibleBooks marks each account's books with the account's own
// visibility setting, or with the URL's narrowing when it names one.
// Every book comes back for the aside with its checkbox state; only
// the visible ones come back as sites to query.
func visibleBooks(accounts []dav.Account[*carddav.Client, AddressBookInfo], only map[string]bool) ([]AddressBookInfo, []bookSite, error) {
	var infos []AddressBookInfo
	var sites []bookSite
	for _, acc := range accounts {
		settings := &Settings{}
		if err := acc.Session.Store().Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return nil, nil, fmt.Errorf("failed to load CardDAV settings: %w", err)
		}
		visibleSet := make(map[string]bool)
		for _, path := range settings.VisibleAddressBooks {
			visibleSet[dav.CanonicalCollectionPath(path)] = true
		}
		for _, ab := range acc.Collections {
			ab.Visible = !settings.AddressBookFilter || visibleSet[ab.Path]
			if only != nil {
				ab.Visible = only[ab.Path]
				ab.Only = len(only) == 1 && ab.Visible
			}
			infos = append(infos, ab)
			if ab.Visible {
				sites = append(sites, bookSite{account: ab.Account, client: acc.Client, path: ab.Path, name: ab.Name})
			}
		}
	}
	return infos, sites, nil
}

// exportContacts hands the selected cards back as one file, each as
// its server stores it. The rows name their own account, so the
// selection can span accounts.
func (p *plugin) exportContacts(ctx *alborz.Context) error {
	params, err := ctx.FormParams()
	if err != nil {
		return err
	}
	refs, err := dav.Selected(ctx, params["paths"], p.client)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
	}
	var buf bytes.Buffer
	for _, r := range dav.Each(ctx.Request().Context(), refs, func(ctx context.Context, ref dav.Ref[*carddav.Client]) ([]byte, error) {
		body, err := ref.Client.Open(ctx, ref.Path)
		if err != nil {
			return nil, err
		}
		defer body.Close()
		return io.ReadAll(body)
	}) {
		if r.Err != nil {
			return r.Err
		}
		buf.Write(r.Value)
		if !bytes.HasSuffix(r.Value, []byte("\n")) {
			buf.WriteString("\r\n")
		}
	}
	ctx.Response().Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": ctx.T("nav.contacts") + ".vcf"}))
	return ctx.Blob(http.StatusOK, "text/vcard; charset=utf-8", buf.Bytes())
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
	if book := addressBookByPath(books, bookPath); book == nil || !book.Writable {
		return echo.NewHTTPError(http.StatusBadRequest, "no address book to file it in")
	}
	n, err := importBook(ctx.Request().Context(), c, bookPath, raw)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Session.PutNotice(ctx.T("import.nothing"))
	} else {
		ctx.Session.PutNotice(ctx.Tf("import.vcf", n))
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
		if _, err := client.PutAddressObject(ctx, target, card); err != nil {
			existing := cardPathByUID(ctx, client, bookPath, uid)
			if existing == "" {
				return n, fmt.Errorf("failed to write %s: %v", uid, err)
			}
			if _, err := client.PutAddressObject(ctx, existing, card); err != nil {
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
