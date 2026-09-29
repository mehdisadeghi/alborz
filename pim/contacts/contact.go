package contacts

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
	"golang.org/x/image/draw"
)

type AddressObjectRenderData struct {
	alborz.BaseRenderData
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
	Neighbours    pim.Neighbours
	// In are the groups naming this contact, Groups the others.
	In     []AddressObject
	Groups []AddressObject
}

type UpdateAddressObjectRenderData struct {
	alborz.BaseRenderData
	Rail          pim.Rail
	Groups        []dav.Group
	AddressBook   *dav.Collection
	AddressObject *carddav.AddressObject
	Card          vcard.Card
	Name          string
	Birthday      alborz.DateField
	// Photo is the card's current picture, shown beside its replacement.
	Photo string
}

// Small on purpose: the card carries the picture inline, fetched on every visit.
const photoLongestSide = 320

// Beyond this the answer is no rather than a slow yes.
const photoMaxUpload = 12 << 20

var (
	errPhotoTooLarge   = errors.New("carddav: photo over the upload limit")
	errPhotoUnreadable = errors.New("carddav: photo is not an image")
)

// Past this the bytes grow faster than a thumbnail improves.
const photoQuality = 82

func applyPhoto(ctx *alborz.Context, card vcard.Card) error {
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
	// vCard 4.0 carries the picture as a data URI, 3.0 as an encoded property.
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

func cardModified(ao *carddav.AddressObject) time.Time {
	if rev, err := ao.Card.Revision(); err == nil && !rev.IsZero() {
		return rev
	}
	return ao.ModTime
}

// A birthday with no year (--0412) is shown as written.
func birthdayValue(card vcard.Card) string {
	if day, err := card.Birthday(); err == nil && !day.IsZero() {
		return day.Format("2006-01-02")
	}
	return card.Value(vcard.FieldBirthday)
}

// Matched by position, so a TYPE or PREF synced onto a field survives an edit that left it alone.
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

func (p *plugin) contact(ctx *alborz.Context) error {
	path, err := pim.ParseObjectPath(ctx.Param("path"))
	if err != nil {
		return err
	}

	c, addressBooks, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
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
		Authors:        p.dav.Authors(object.Path, ctx.Login.Username()),
		In:             in,
		Groups:         rest,
		Neighbours:     pim.Around(list.Items, path),
		List:           cmp.Or(ctx.From(), pim.ListURL("/contacts", pim.ListParams(ctx, contactListParams...))),
		Rail:           rail,
		BaseRenderData: *alborz.NewBaseRenderData(ctx).WithTitle(AddressObject{AddressObject: ao}.DisplayName()).WithItem(),
		AddressBook:    addressBook,
		AddressObject:  AddressObject{AddressObject: ao},
		Birthday:       birthdayValue(ao.Card),
		BirthdayDate:   birthday,
		Modified:       cardModified(ao),
	})
}
func (p *plugin) updateContact(ctx *alborz.Context) error {
	addressObjectPath, err := pim.ParseObjectPath(ctx.Param("path"))
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
		c, addressBooks, err = p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
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
				BaseRenderData: *alborz.NewBaseRenderData(ctx).Refused(message),
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
			to, err = pim.Destination(ctx, ctx.FormValue("addressbook"), p.clientWithAddressBooks, func(book dav.Collection) bool { return book.Writable })
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
			day, err := field.Date(time.UTC)
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
		return pim.Saved(ctx, creating, ctx.T("notice.contactcreated"), named.DisplayName(), named.URL(), "/contacts", to.Account)
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
		BaseRenderData: *alborz.NewBaseRenderData(ctx),
		Groups:         groups,
		AddressBook:    currentAddressBook,
		AddressObject:  ao,
		Card:           card,
		Name:           name,
		Birthday:       ctx.InputDay(birthdayValue(card)),
		Photo:          card.PreferredValue(vcard.FieldPhoto),
	})
}

func (p *plugin) note(ctx *alborz.Context) error {
	note := strings.TrimSpace(ctx.FormValue("note"))
	return pim.Run(ctx, pim.Action[*carddav.Client]{Client: p.client, List: "/contacts", Done: pim.Quiet[*carddav.Client],
		Do: func(ctx *alborz.Context, ref pim.Ref[*carddav.Client]) error {
			if note == "" {
				return nil
			}
			return changeCard(ctx, ref, func(card vcard.Card) {
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
func (p *plugin) deletePhoto(ctx *alborz.Context) error {
	return pim.Run(ctx, pim.Action[*carddav.Client]{Client: p.client, List: "/contacts", Done: pim.Quiet[*carddav.Client],
		Do: func(ctx *alborz.Context, ref pim.Ref[*carddav.Client]) error {
			return changeCard(ctx, ref, func(card vcard.Card) { delete(card, vcard.FieldPhoto) })
		}})
}

func (p *plugin) color(ctx *alborz.Context) error {
	return pim.Star(ctx, p.client, "/contacts", func(ctx *alborz.Context, ref pim.Ref[*carddav.Client], name string) (was string, err error) {
		err = changeCard(ctx, ref, func(card vcard.Card) {
			was = CardColor(card)
			SetColor(card, name)
		})
		return was, err
	})
}
