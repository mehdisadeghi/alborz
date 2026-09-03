package alborzcarddav

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/image/draw"

	"git.mehdix.org/alborz"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

const maxAddressBookQueryConcurrency = 4

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
	Groups        []BookGroup
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
func applyPhoto(ctx *alborz.Context, card vcard.Card) error {
	file, err := ctx.FormFile("photo")
	if err != nil || file == nil || file.Size == 0 {
		return nil // nothing offered; whatever the card has, it keeps
	}
	if file.Size > photoMaxUpload {
		return errors.New(ctx.T("form.phototoolarge"))
	}
	f, err := file.Open()
	if err != nil {
		return errors.New(ctx.T("form.photounreadable"))
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return errors.New(ctx.T("form.photounreadable"))
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
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return errors.New(ctx.T("form.photounreadable"))
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
		only[canonicalCollectionPath(v)] = true
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

func parseObjectPath(s string) (string, error) {
	p, err := url.PathUnescape(s)
	if err != nil {
		err = fmt.Errorf("failed to parse path: %v", err)
		return "", echo.NewHTTPError(http.StatusBadRequest, err)
	}
	return p, nil
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
	POST("/contacts", func(ctx *alborz.Context) error {
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
	})

	GET("/contacts", func(ctx *alborz.Context) error {
		queryText := ctx.QueryParam("query")

		only := onlyBooks(ctx)
		accounts, err := p.pooledBooks(ctx)
		if err != nil {
			return err
		}

		type bookSite struct {
			account string
			client  *carddav.Client
			path    string
			name    string
		}
		var addressBookInfos []AddressBookInfo
		var sites []bookSite
		for _, acc := range accounts {
			settings := &Settings{}
			if err := acc.session.Store().Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
				return fmt.Errorf("failed to load CardDAV settings: %w", err)
			}
			visibleSet := make(map[string]bool)
			for _, path := range settings.VisibleAddressBooks {
				visibleSet[canonicalCollectionPath(path)] = true
			}
			for _, ab := range acc.books {
				ab.Visible = !settings.AddressBookFilter || visibleSet[ab.Path]
				if only != nil {
					ab.Visible = only[ab.Path]
					ab.Only = len(only) == 1 && ab.Visible
				}
				addressBookInfos = append(addressBookInfos, ab)
				if ab.Visible {
					sites = append(sites, bookSite{account: ab.Account, client: acc.client, path: ab.Path, name: ab.Name})
				}
			}
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

		type abQueryResult struct {
			site     bookSite
			contacts []carddav.AddressObject
			err      error
		}

		reqCtx := ctx.Request().Context()
		results := make(chan abQueryResult, len(sites))
		sem := make(chan struct{}, maxAddressBookQueryConcurrency)
		for _, site := range sites {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				abContacts, err := site.client.QueryAddressBook(reqCtx, site.path, &query)
				results <- abQueryResult{site: site, contacts: abContacts, err: err}
			}()
		}

		var aos []AddressObject
		for range sites {
			result := <-results
			if result.err != nil {
				return fmt.Errorf("failed to query address book %s: %v", result.site.name, result.err)
			}
			for i := range result.contacts {
				aos = append(aos, AddressObject{AddressObject: &result.contacts[i], Account: result.site.account})
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
	})

	GET("/contacts/:path", func(ctx *alborz.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
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
	})

	updateContact := func(ctx *alborz.Context) error {
		addressObjectPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		var c *carddav.Client
		var addressBooks []AddressBookInfo
		var groups []BookGroup
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
					return echo.NewHTTPError(http.StatusBadRequest, "unknown address book")
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
				return reject(err.Error())
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
	GET("/contacts/:path/raw", func(ctx *alborz.Context) error {
		path, err := parseObjectPath(ctx.Param("path"))
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
		return ctx.Stream(http.StatusOK, "text/plain; charset=utf-8", body)
	})

	GET("/address-books/create", handleCreateBook(p))
	POST("/address-books/create", handleCreateBook(p))
	GET("/address-books/:path", handleBook(p))
	POST("/address-books/:path", handleBook(p))
	POST("/address-books/:path/delete", handleDeleteBook(p))

	GET("/contacts/create", updateContact)
	POST("/contacts/create", updateContact)

	GET("/contacts/:path/edit", updateContact)
	POST("/contacts/:path/edit", updateContact)

	// A line added where the contact is read, not behind the edit form.
	POST("/contacts/:path/note", func(ctx *alborz.Context) error {
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
	})

	// Removing the picture is its own request, not a tick the edit form
	// carries: a submit button inside that form would be the one Enter
	// reaches from any field, and pressing Enter in a name is not a
	// request to delete a photo. Idempotent - a contact with no picture
	// is what it leaves either way.
	POST("/contacts/:path/photo/delete", func(ctx *alborz.Context) error {
		return editCard(ctx, p, func(card vcard.Card) {
			delete(card, vcard.FieldPhoto)
		})
	})

	POST("/contacts/:path/delete", func(ctx *alborz.Context) error {
		objPath, err := parseObjectPath(ctx.Param("path"))
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
	})

	POST("/contacts/delete", func(ctx *alborz.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		paths := params["paths"]
		if len(paths) == 0 {
			return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
		}

		// A pooled list draws rows from several accounts, so a selection
		// can span them: each row names its own, and the deletions are
		// grouped so each account's own client makes them.
		byAccount := map[string][]string{}
		for _, ref := range paths {
			account, objPath, ok := strings.Cut(ref, "|")
			if !ok {
				return echo.NewHTTPError(http.StatusBadRequest, "unqualified contact")
			}
			byAccount[account] = append(byAccount[account], objPath)
		}
		for account, objPaths := range byAccount {
			session := ctx.SessionFor(account)
			if session == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "not signed in to that account")
			}
			c, err := p.client(session)
			if err != nil {
				return err
			}
			for _, objPath := range objPaths {
				if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
					return fmt.Errorf("failed to delete address object: %v", err)
				}
			}
		}

		return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
	})
}

// CollectionRenderData renders collection.html, the page calendars and
// address books share.
type CollectionRenderData struct {
	alborz.BaseRenderData
	Name      string
	Color     string
	Path      string
	Account   string
	Count     int // -1 when the server would not say
	Base      string
	ListHref  string
	BackLabel string
	Error     string
}

// NewCollectionRenderData renders create-collection.html. An address
// book has no component set to choose, so it offers no Holds.
type NewCollectionRenderData struct {
	alborz.BaseRenderData
	Accounts    []alborz.Account
	Name        string
	Account     string
	Color       string
	Title       string
	ListHref    string
	BackLabel   string
	OffersHolds bool
	Holds       string
	Next        string
	Error       string
}

func handleCreateBook(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		data := &NewCollectionRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("contacts.newbook")),
			Accounts:       ctx.Accounts(),
			Account:        ctx.Session.Username(),
			Color:          "#3366cc",
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

func handleBook(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = canonicalCollectionPath(collPath)
		c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		info := addressBookByPath(books, collPath)
		if info == nil {
			return alborz.NotFoundf("no such collection")
		}
		data := &CollectionRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(info.Name),
			Name:           info.Name,
			Color:          info.Color,
			Path:           info.Path,
			Account:        ctx.Session.Username(),
			Count:          bookCount(ctx, c, *info),
			Base:           "/address-books/",
			ListHref:       "/contacts",
			BackLabel:      ctx.T("nav.contacts"),
		}

		if ctx.Request().Method == http.MethodPost {
			name := strings.TrimSpace(ctx.FormValue("name"))
			data.Name, data.Color = name, ctx.FormValue("color")
			if name == "" {
				data.Error = ctx.T("form.nameneeded")
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			davBase, _ := p.davURL(ctx.Session)
			target := davBase.ResolveReference(&url.URL{Path: collPath}).String()
			if err := doProppatch(ctx.Request().Context(), p.httpClient(ctx.Session),
				target, name, ctx.FormValue("color")); err != nil {
				data.Error = err.Error()
				return ctx.Render(http.StatusUnprocessableEntity, "collection.html", data)
			}
			p.books.Forget(ctx.Session.Username())
			return ctx.Redirect(http.StatusFound, ctx.NextOr(ctx.AccountPath("/contacts")))
		}
		return ctx.Render(http.StatusOK, "collection.html", data)
	}
}

func handleDeleteBook(p *plugin) func(*alborz.Context) error {
	return func(ctx *alborz.Context) error {
		collPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}
		collPath = canonicalCollectionPath(collPath)
		_, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		if addressBookByPath(books, collPath) == nil {
			return alborz.NotFoundf("no such collection")
		}
		davBase, _ := p.davURL(ctx.Session)
		target := davBase.ResolveReference(&url.URL{Path: collPath}).String()
		if err := doDeleteCollection(ctx.Request().Context(), p.httpClient(ctx.Session), target); err != nil {
			return err
		}
		p.books.Forget(ctx.Session.Username())
		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/contacts"))
	}
}

// editCard reads one card, lets the caller change it, and writes it
// back with a fresh REV. The read is deliberately not go-webdav's: see
// getAddressObject for the ETag it cannot parse.
func editCard(ctx *alborz.Context, p *plugin, change func(vcard.Card)) error {
	objPath, err := parseObjectPath(ctx.Param("path"))
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

// bookCount says how much a delete would take. -1 means the server did
// not answer, which the page states rather than guessing at.
func bookCount(ctx *alborz.Context, c *carddav.Client, info AddressBookInfo) int {
	objs, err := c.QueryAddressBook(ctx.Request().Context(), info.Path, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldUID}},
	})
	if err != nil {
		ctx.Logger().Printf("failed to count %s: %v", info.Path, err)
		return -1
	}
	return len(objs)
}
