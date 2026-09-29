package contacts

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/dates"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

type AddressObjectRenderData struct {
	web.BaseRenderData
	Rail pim.Rail
	// Authors are who added and last changed the object, where another account.
	Authors dav.Authors
	// List is the list the page was opened from.
	List string
	// Modified is REV, else the server's last-modified; vCard has no property for creation.
	Modified      time.Time
	AddressBook   *dav.Collection
	AddressObject AddressObject
	Birthday      string
	BirthdayDate  time.Time
	Neighbours    web.Neighbours
	// In are the groups naming this contact, Groups the others.
	In     []AddressObject
	Groups []AddressObject
}

type UpdateAddressObjectRenderData struct {
	web.BaseRenderData
	Rail          pim.Rail
	Groups        []dav.Group
	AddressBook   *dav.Collection
	AddressObject *carddav.AddressObject
	Card          vcard.Card
	Name          string
	Birthday      dates.Field
	// Photo is the card's current picture, shown beside its replacement.
	Photo string
}

// Beyond this the answer is no rather than a slow yes.
const photoMaxUpload = 12 << 20

// photoBodySize bounds a contact's form: its fields and the photo.
const photoBodySize = photoMaxUpload + web.MaxFormSize

var errPhotoTooLarge = errors.New("carddav: photo over the upload limit")

// photoTooLarge answers a contact form past its limit: the fields did not arrive, so it opens again on what the server holds.
func photoTooLarge(ctx *web.Context) error {
	ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: ctx.T("form.phototoolarge")})
	return ctx.Redirect(http.StatusFound, ctx.Request().URL.String())
}

func applyPhoto(ctx *web.Context, card vcard.Card) error {
	file, err := ctx.FormFile("photo")
	if err != nil || file == nil || file.Size == 0 {
		return nil
	}
	if file.Size > photoMaxUpload {
		return errPhotoTooLarge
	}
	f, err := file.Open()
	if err != nil {
		return errPhotoUnreadable
	}
	defer f.Close()
	return setPhoto(card, f)
}

func (p *plugin) contact(ctx *web.Context) error {
	path := ctx.Param("path")

	c, addressBooks, err := p.Open(ctx.Request().Context(), ctx.Login)
	if err != nil {
		return err
	}
	addressBook := dav.Holding(addressBooks, "", path)
	if addressBook == nil {
		return alborz.NotFound("notfound.contact")
	}

	multiGet := carddav.AddressBookMultiGet{
		DataRequest: carddav.AddressDataRequest{
			AllProp: true,
		},
	}
	aos, err := c.MultiGetAddressBook(ctx.Request().Context(), path, &multiGet)
	if err != nil {
		if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
			return alborz.NotFound("notfound.contact")
		}
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
	list, err := p.contactList(ctx)
	if err != nil {
		return err
	}
	// A contact says nothing about its groups; the groups are read to find them.
	object := AddressObject{AddressObject: ao}
	var in, rest []AddressObject
	for _, g := range list.Groups {
		if slices.Contains(g.Members(), object.UID()) {
			in = append(in, g)
		} else {
			rest = append(rest, g)
		}
	}
	// A birthday with no date stays zero and is shown as written.
	birthday, _ := ao.Card.Birthday()
	return ctx.Render(http.StatusOK, "contacts/contact", &AddressObjectRenderData{
		Authors:        p.DAV.Authors(object.Path, ctx.Login.Username()),
		In:             in,
		Groups:         rest,
		Neighbours:     pim.Around(list.Items, path),
		List:           cmp.Or(ctx.From(), pim.ListURL(ctx.Path("contacts.list"), pim.ListParams(ctx, contactListParams...))),
		Rail:           rail,
		BaseRenderData: *web.NewBaseRenderData(ctx).WithTitle(AddressObject{AddressObject: ao}.DisplayName()).WithItem(),
		AddressBook:    addressBook,
		AddressObject:  AddressObject{AddressObject: ao},
		Birthday:       birthdayValue(ao.Card),
		BirthdayDate:   birthday,
		Modified:       cardModified(ao),
	})
}
func (p *plugin) updateContact(ctx *web.Context) error {
	addressObjectPath := ctx.Param("path")

	var err error
	var c *carddav.Client
	var addressBooks []dav.Collection
	var groups []dav.Group
	var ao *carddav.AddressObject
	var card vcard.Card

	var currentAddressBook *dav.Collection
	if addressObjectPath != "" {
		c, addressBooks, err = p.Open(ctx.Request().Context(), ctx.Login)
		if err != nil {
			return err
		}
		ao, err = c.GetAddressObject(ctx.Request().Context(), addressObjectPath)
		if err != nil {
			if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
				return alborz.NotFound("notfound.contact")
			}
			return fmt.Errorf("failed to query CardDAV address: %v", err)
		}
		card = ao.Card
		currentAddressBook = dav.Holding(addressBooks, "", ao.Path)
	} else {
		// Pooled: the active account may have no address book while another does.
		groups, err = p.writableBookGroups(ctx)
		if err != nil {
			return err
		}
		card = make(vcard.Card)
		currentAddressBook = &dav.Collection{}
		if len(groups) > 0 {
			currentAddressBook = &groups[0].Collections[0]
		}
	}

	if ctx.Request().Method == "POST" {
		fn := ctx.FormValue("fn")
		emails := strings.Split(ctx.FormValue("emails"), ",")

		reject := func(message string) error {
			rail, err := p.bookRail(ctx)
			if err != nil {
				return err
			}
			return ctx.Render(http.StatusUnprocessableEntity, "contacts/contact-edit", &UpdateAddressObjectRenderData{
				Rail:           rail,
				BaseRenderData: *web.NewBaseRenderData(ctx).Refused(message),
				Groups:         groups,
				AddressBook:    currentAddressBook,
				AddressObject:  ao,
				Card:           card,
				Name:           fn,
				Birthday:       ctx.DateField("bday"),
				Photo:          card.PreferredValue(vcard.FieldPhoto),
			})
		}
		if strings.TrimSpace(fn) == "" {
			return reject(ctx.T("form.nameneeded"))
		}

		to := pim.Ref[*carddav.Client]{Client: c}
		creating := ao == nil
		if creating {
			to, err = pim.Destination(ctx, ctx.FormValue("addressbook"), p.Open, func(book dav.Collection) bool { return book.Writable })
			if errors.Is(err, pim.ErrNoDestination) {
				return reject(ctx.T("form.destinationneeded"))
			} else if err != nil {
				return err
			}
		}

		if _, ok := card[vcard.FieldVersion]; !ok {
			card.SetValue(vcard.FieldVersion, "4.0")
		}

		if field := card.Preferred(vcard.FieldFormattedName); field != nil {
			field.Value = fn
		} else {
			card.Add(vcard.FieldFormattedName, &vcard.Field{Value: fn})
		}

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
		var birthday string
		if field := ctx.DateField("bday"); field.Value != "" {
			day, err := field.Read(time.UTC)
			if err != nil {
				return reject(ctx.T("form.birthday"))
			}
			birthday = day.Format("20060102")
		}
		setValue(vcard.FieldBirthday, birthday)
		setValue(vcard.FieldURL, strings.TrimSpace(ctx.FormValue("url")))
		setValue(vcard.FieldNote, strings.TrimSpace(ctx.FormValue("note")))
		// CATEGORIES is a comma list (RFC 6350 6.7.1).
		var words []string
		for _, word := range strings.Split(ctx.FormValue("categories"), ",") {
			if word = strings.TrimSpace(word); word != "" {
				words = append(words, word)
			}
		}
		card.SetCategories(words)

		if err := applyPhoto(ctx, card); err != nil {
			switch {
			case errors.Is(err, errPhotoTooLarge):
				return reject(ctx.T("form.phototoolarge"))
			case errors.Is(err, errPhotoUnreadable):
				return reject(ctx.T("form.photounreadable"))
			}
			return err
		}

		// Free-form address goes in the street component; the others are kept.
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
			card.SetValue(vcard.FieldUID, uidURN+id.String())
		}

		held, etag := "", ""
		if !creating {
			held, etag = ao.Path, ao.ETag
		}
		at, ifMatch, ifNoneMatch := pim.Target(to.Path, id.String()+".vcf", held, etag)
		ao, err = to.Client.PutAddressObject(ctx.Request().Context(), at, card,
			&carddav.PutAddressObjectOptions{IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
		if err != nil {
			return reject(fmt.Sprintf(ctx.T("form.saverefused"), err))
		}
		// The PUT answer carries only the path, so the card as written names the contact.
		named := AddressObject{AddressObject: &carddav.AddressObject{Path: ao.Path, Card: card}}
		return pim.Saved(ctx, creating, ctx.T("notice.contactcreated"), named.DisplayName(), ctx.Path("contacts.contact", named.Path), ctx.Path("contacts.list"), to.Account)
	}

	// Map values evaluate eagerly; a missing object must not reach DisplayName.
	name := ""
	if ao != nil {
		name = AddressObject{AddressObject: ao}.DisplayName()
	}
	rail, err := p.bookRail(ctx)
	if err != nil {
		return err
	}
	return ctx.Render(http.StatusOK, "contacts/contact-edit", &UpdateAddressObjectRenderData{
		Rail:           rail,
		BaseRenderData: *web.NewBaseRenderData(ctx),
		Groups:         groups,
		AddressBook:    currentAddressBook,
		AddressObject:  ao,
		Card:           card,
		Name:           name,
		Birthday:       ctx.Dates().Day(birthdayValue(card)),
		Photo:          card.PreferredValue(vcard.FieldPhoto),
	})
}

func (p *plugin) note(ctx *web.Context) error {
	note := strings.TrimSpace(ctx.FormValue("note"))
	return pim.Run(ctx, pim.Action[*carddav.Client]{Client: p.Client, List: "contacts.list", Done: pim.Quiet[*carddav.Client],
		Do: func(ctx *web.Context, ref pim.Ref[*carddav.Client]) error {
			if note == "" {
				return nil
			}
			return changeCard(ctx.Request().Context(), ref, func(card vcard.Card) {
				text := pim.AppendNote(ctx, card.Value(vcard.FieldNote), note)
				if field := card.Preferred(vcard.FieldNote); field != nil {
					field.Value = text
				} else {
					card.Add(vcard.FieldNote, &vcard.Field{Value: text})
				}
			})
		}})
}

// Its own request: Enter in a name field must not delete the photo.
func (p *plugin) deletePhoto(ctx *web.Context) error {
	return pim.Run(ctx, pim.Action[*carddav.Client]{Client: p.Client, List: "contacts.list", Done: pim.Quiet[*carddav.Client],
		Do: func(ctx *web.Context, ref pim.Ref[*carddav.Client]) error {
			return changeCard(ctx.Request().Context(), ref, func(card vcard.Card) { delete(card, vcard.FieldPhoto) })
		}})
}

func (p *plugin) color(ctx *web.Context) error {
	return pim.Star(ctx, p.Client, "contacts.list", "contacts.contact", func(ctx *web.Context, ref pim.Ref[*carddav.Client], name string) (was string, err error) {
		err = changeCard(ctx.Request().Context(), ref, func(card vcard.Card) {
			was = CardColor(card)
			SetColor(card, name)
		})
		return was, err
	})
}
