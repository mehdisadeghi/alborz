package alborzcarddav

import (
	"bytes"
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
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
	"golang.org/x/image/draw"
)

type AddressObjectRenderData struct {
	alborz.BaseRenderData
	Rail dav.Rail
	// Authors are who added the object and changed it last, where it is
	// kept here and that is another account.
	Authors dav.Authors
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
		Authors:        p.dav.Authors(object.Path, ctx.Session.Username()),
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
			if code, _ := webdav.HTTPErrorCode(err); code == http.StatusNotFound {
				return alborz.NotFound("notfound.contact")
			}
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
			card.SetValue(vcard.FieldUID, uidURN+id.String())
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
