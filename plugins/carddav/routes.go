package alborzcarddav

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

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
	Birthday      string    // the input format, for the edit form
	BirthdayDate  time.Time // the same day, for the page to write out
}

type UpdateAddressObjectRenderData struct {
	alborz.BaseRenderData
	AccountBooks  []BookGroup
	AddressBook   *AddressBookInfo
	AddressObject *carddav.AddressObject // nil if creating a new contact
	Card          vcard.Card
	Name          string
	Error         string
	Birthday      string
}

// birthdayValue renders BDAY in the HTML date-input format, accepting both
// the vCard 4.0 basic format (19850412) and the dashed 3.0 form.
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
		return ctx.Redirect(http.StatusFound, ctx.NextOr("/contacts"))
	})

	GET("/contacts", func(ctx *alborz.Context) error {
		queryText := ctx.QueryParam("query")

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
				return fmt.Errorf("failed to load CardDAV settings: %v", err)
			}
			visibleSet := make(map[string]bool)
			for _, path := range settings.VisibleAddressBooks {
				visibleSet[canonicalCollectionPath(path)] = true
			}
			for _, ab := range acc.books {
				ab.Visible = !settings.AddressBookFilter || visibleSet[ab.Path]
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
		case "", "name", "email", "phone", "account":
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
			BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(AddressObject{AddressObject: ao}.DisplayName()),
			AddressBook:    addressBook,
			AddressObject:  AddressObject{AddressObject: ao},
			Birthday:       birthdayValue(ao.Card),
			BirthdayDate:   birthdayDate(ao.Card),
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
			// Creation is a pooled operation. The active account may quite
			// legitimately have no address book while another account does.
			groups, err = p.writableBookGroups(ctx)
			if err != nil {
				return err
			}
			if len(groups) == 0 || len(groups[0].Books) == 0 {
				return fmt.Errorf("no writable address books")
			}
			card = make(vcard.Card)
			currentAddressBook = &groups[0].Books[0]
		}

		if ctx.Request().Method == "POST" {
			fn := ctx.FormValue("fn")
			emails := strings.Split(ctx.FormValue("emails"), ",")
			addressBookPath := ctx.FormValue("addressbook")

			reject := func(message string) error {
				return ctx.Render(http.StatusUnprocessableEntity, "update-address-object.html", &UpdateAddressObjectRenderData{
					BaseRenderData: *alborz.NewBaseRenderData(ctx),
					AccountBooks:   groups,
					AddressBook:    currentAddressBook,
					AddressObject:  ao,
					Card:           card,
					Name:           fn,
					Birthday:       birthdayValue(card),
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
			ao, err = saveClient.PutAddressObject(ctx.Request().Context(), savePath, card)
			if err != nil {
				return fmt.Errorf("failed to put address object: %v", err)
			}

			return func() error {
				if createAcct != "" {
					return ctx.Redirect(http.StatusFound, AddressObject{AddressObject: ao}.URL()+"?account="+alborz.AccountParam(createAcct))
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
			AccountBooks:   groups,
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

		return ctx.Redirect(http.StatusFound, ctx.AccountPath("/contacts"))
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
