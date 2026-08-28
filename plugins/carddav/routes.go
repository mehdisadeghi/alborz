package alborzcarddav

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

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
	Query          string
	ColorForPath   func(string) string
}

type Settings struct {
	AddressBookFilter   bool
	VisibleAddressBooks []string
}

const settingsKey = "carddav.settings"

type AddressObjectRenderData struct {
	alborz.BaseRenderData
	AddressBook   *AddressBookInfo
	AddressObject AddressObject
	Birthday      string
}

type UpdateAddressObjectRenderData struct {
	alborz.BaseRenderData
	AddressBooks  []AddressBookInfo
	AddressBook   *AddressBookInfo
	AddressObject *carddav.AddressObject // nil if creating a new contact
	Card          vcard.Card
	Name          string
	Birthday      string
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
			return fmt.Errorf("failed to load CardDAV settings: %v", err)
		}
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		settings.AddressBookFilter = true
		settings.VisibleAddressBooks = params["book"]
		if err := ctx.Session.Store().Put(settingsKey, settings); err != nil {
			return fmt.Errorf("failed to save CardDAV settings: %v", err)
		}
		return ctx.Redirect(http.StatusFound, ctx.Request().URL.RequestURI())
	})

	GET("/contacts", func(ctx *alborz.Context) error {
		queryText := ctx.QueryParam("query")

		c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}

		settings := &Settings{}
		if err := ctx.Session.Store().Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return fmt.Errorf("failed to load CardDAV settings: %v", err)
		}

		addressBookFilter := settings.AddressBookFilter
		visibleSet := make(map[string]bool)
		for _, path := range settings.VisibleAddressBooks {
			visibleSet[canonicalCollectionPath(path)] = true
		}

		addressBookInfos := make([]AddressBookInfo, len(addressBooks))
		for i, ab := range addressBooks {
			visible := !addressBookFilter || visibleSet[ab.Path]
			addressBookInfos[i] = AddressBookInfo{
				Path:    ab.Path,
				Name:    ab.Name,
				Color:   ab.Color,
				Visible: visible,
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
			name     string
			contacts []carddav.AddressObject
			err      error
		}

		var visibleAddressBooks []AddressBookInfo
		for _, abInfo := range addressBookInfos {
			if abInfo.Visible {
				visibleAddressBooks = append(visibleAddressBooks, abInfo)
			}
		}

		reqCtx := ctx.Request().Context()
		results := make(chan abQueryResult, len(visibleAddressBooks))
		sem := make(chan struct{}, maxAddressBookQueryConcurrency)
		for _, abInfo := range visibleAddressBooks {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				abContacts, err := c.QueryAddressBook(reqCtx, abInfo.Path, &query)
				results <- abQueryResult{name: abInfo.Name, contacts: abContacts, err: err}
			}()
		}

		var aos []carddav.AddressObject
		for i := 0; i < len(visibleAddressBooks); i++ {
			result := <-results
			if result.err != nil {
				return fmt.Errorf("failed to query address book %s: %v", result.name, result.err)
			}
			aos = append(aos, result.contacts...)
		}

		sort.Slice(aos, func(i, j int) bool {
			nameI := AddressObject{&aos[i]}.DisplayName()
			nameJ := AddressObject{&aos[j]}.DisplayName()
			return strings.ToLower(nameI) < strings.ToLower(nameJ)
		})

		return ctx.Render(http.StatusOK, "address-book.html", &AddressBookRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(ctx.T("nav.contacts")),
			AddressBooks:   addressBookInfos,
			AddressObjects: newAddressObjectList(aos),
			Query:          queryText,
			ColorForPath: func(contactPath string) string {
				for _, ab := range addressBookInfos {
					if strings.HasPrefix(contactPath, ab.Path) {
						return ab.Color
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
		if len(aos) != 1 {
			return fmt.Errorf("expected exactly one address object with path %q, got %v", path, len(aos))
		}
		ao := &aos[0]

		return ctx.Render(http.StatusOK, "address-object.html", &AddressObjectRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(AddressObject{ao}.DisplayName()),
			AddressBook:    addressBook,
			AddressObject:  AddressObject{ao},
			Birthday:       birthdayValue(ao.Card),
		})
	})

	updateContact := func(ctx *alborz.Context) error {
		addressObjectPath, err := parseObjectPath(ctx.Param("path"))
		if err != nil {
			return err
		}

		c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
		if err != nil {
			return err
		}
		var ao *carddav.AddressObject
		var card vcard.Card
		writable := make([]AddressBookInfo, 0, len(addressBooks))
		for _, ab := range addressBooks {
			if ab.Writable {
				writable = append(writable, ab)
			}
		}

		var currentAddressBook *AddressBookInfo
		if addressObjectPath != "" {
			ao, err = c.GetAddressObject(ctx.Request().Context(), addressObjectPath)
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
			if len(writable) == 0 {
				return fmt.Errorf("no writable address books")
			}
			card = make(vcard.Card)
			currentAddressBook = &writable[0]
		}

		if ctx.Request().Method == "POST" {
			fn := ctx.FormValue("fn")
			emails := strings.Split(ctx.FormValue("emails"), ",")
			addressBookPath := ctx.FormValue("addressbook")
			if ao == nil && addressBookByPath(writable, addressBookPath) == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "unknown address book")
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

			// TODO: params are lost here
			var emailFields []*vcard.Field
			for _, email := range emails {
				if email = strings.TrimSpace(email); email != "" {
					emailFields = append(emailFields, &vcard.Field{Value: email})
				}
			}
			if len(emailFields) > 0 {
				card[vcard.FieldEmail] = emailFields
			} else {
				delete(card, vcard.FieldEmail)
			}

			var telFields []*vcard.Field
			for _, tel := range strings.Split(ctx.FormValue("tels"), ",") {
				if tel = strings.TrimSpace(tel); tel != "" {
					telFields = append(telFields, &vcard.Field{Value: tel})
				}
			}
			if len(telFields) > 0 {
				card[vcard.FieldTelephone] = telFields
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
			ao, err = c.PutAddressObject(ctx.Request().Context(), savePath, card)
			if err != nil {
				return fmt.Errorf("failed to put address object: %v", err)
			}

			return ctx.Redirect(http.StatusFound, AddressObject{ao}.URL())
		}

		// Both map values would be evaluated eagerly; a missing object
		// must not reach DisplayName.
		name := ""
		if ao != nil {
			name = AddressObject{ao}.DisplayName()
		}
		return ctx.Render(http.StatusOK, "update-address-object.html", &UpdateAddressObjectRenderData{
			BaseRenderData: *alborz.NewBaseRenderData(ctx),
			AddressBooks:   writable,
			AddressBook:    currentAddressBook,
			AddressObject:  ao,
			Card:           card,
			Name:           name,
			Birthday:       birthdayValue(card),
		})
	}

	GET("/contacts/create", updateContact)
	POST("/contacts/create", updateContact)

	GET("/contacts/:path/edit", updateContact)
	POST("/contacts/:path/edit", updateContact)

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

		return ctx.Redirect(http.StatusFound, "/contacts")
	})

	POST("/contacts/delete", func(ctx *alborz.Context) error {
		params, err := ctx.FormParams()
		if err != nil {
			return err
		}
		paths := params["paths"]
		if len(paths) == 0 {
			return ctx.Redirect(http.StatusFound, "/contacts")
		}

		c, err := p.client(ctx.Session)
		if err != nil {
			return err
		}

		for _, objPath := range paths {
			if err := c.RemoveAll(ctx.Request().Context(), objPath); err != nil {
				return fmt.Errorf("failed to delete address object: %v", err)
			}
		}

		return ctx.Redirect(http.StatusFound, "/contacts")
	})
}
