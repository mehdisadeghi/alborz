package contacts

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

var errNoAddressBook = alborz.NotFound("notfound.addressbook")

// The colour is InfCloud's, which the server keeps as a dead property.
var colorName = xml.Name{Space: "http://inf-it.com/ns/ab/", Local: "addressbook-color"}

type bookColor struct {
	XMLName xml.Name `xml:"http://inf-it.com/ns/ab/ addressbook-color"`
	Color   string   `xml:",chardata"`
}

func listAddressBooks(ctx context.Context, client *http.Client, baseURL *url.URL, homes []dav.Home) ([]dav.Collection, []error) {
	c, err := carddav.NewClient(client, baseURL.String())
	if err != nil {
		return nil, []error{err}
	}
	return dav.ListCollections(ctx, homes, func(ctx context.Context, home string) ([]dav.Collection, error) {
		books, err := c.FindAddressBooks(ctx, home, colorName)
		if err != nil {
			return nil, err
		}
		out := make([]dav.Collection, len(books))
		for i, b := range books {
			out[i] = dav.Collection{Path: b.Path, Name: b.Name, Writable: !b.ReadOnly}
			for _, d := range b.DeadProperties {
				var color bookColor
				if err := xml.Unmarshal(d.XML, &color); err != nil {
					return nil, err
				}
				out[i].Color = strings.TrimSpace(color.Color)
			}
		}
		return out, nil
	})
}

func colorProperty(color string) ([]webdav.DeadProperty, error) {
	if color == "" {
		return nil, nil
	}
	b, err := xml.Marshal(bookColor{Color: color})
	return []webdav.DeadProperty{{Name: colorName, XML: b}}, err
}

// Extended MKCOL (RFC 5689) sets the properties in the same round trip.
func makeBook(ctx context.Context, client *http.Client, base *url.URL, path, name, color string, _ []string) error {
	c, err := carddav.NewClient(client, base.String())
	if err != nil {
		return err
	}
	dead, err := colorProperty(color)
	if err != nil {
		return err
	}
	return c.CreateAddressBook(ctx, &carddav.AddressBook{Path: path, Name: name, DeadProperties: dead})
}

func changeBook(ctx context.Context, client *http.Client, base *url.URL, path, name, color string) error {
	c, err := carddav.NewClient(client, base.String())
	if err != nil {
		return err
	}
	dead, err := colorProperty(color)
	if err != nil {
		return err
	}
	return c.UpdateAddressBook(ctx, path, &carddav.AddressBookUpdate{Name: &name, DeadProperties: dead})
}

func newClient(u *url.URL, httpClient *http.Client) (*carddav.Client, error) {
	return carddav.NewClient(httpClient, u.String())
}

type AddressObject struct {
	*carddav.AddressObject

	Account string
}

func (ao AddressObject) DisplayName() string {
	if fn := ao.Card.PreferredValue("FN"); fn != "" {
		return fn
	}
	if n := ao.Card.Name(); n != nil {
		parts := []string{n.GivenName, n.AdditionalName, n.FamilyName}
		var nonEmpty []string
		for _, p := range parts {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		if len(nonEmpty) > 0 {
			return strings.Join(nonEmpty, " ")
		}
	}
	for _, field := range []string{vcard.FieldNickname, vcard.FieldOrganization, vcard.FieldEmail, vcard.FieldTelephone} {
		if v := ao.Card.PreferredValue(field); v != "" {
			return v
		}
	}
	return ao.Path
}

func (ao AddressObject) PhotoURL() string {
	return ao.Card.PreferredValue("PHOTO")
}

// A group is a KIND:group card naming its MEMBERs (RFC 6350); Apple's 3.0 names are read too.
const (
	appleKindField   = "X-ADDRESSBOOKSERVER-KIND"
	appleMemberField = "X-ADDRESSBOOKSERVER-MEMBER"
	memberPrefix     = "urn:uuid:"
)

// IsGroup reports whether the card is a group of contacts.
func (ao AddressObject) IsGroup() bool {
	return ao.Card.Kind() == vcard.KindGroup || strings.EqualFold(ao.Card.Value(appleKindField), string(vcard.KindGroup))
}

// UID is the card's UID without the URN wrapper.
func (ao AddressObject) UID() string {
	return strings.TrimPrefix(ao.Card.Value(vcard.FieldUID), memberPrefix)
}

// Members are the UIDs a group names, in the order the card lists them.
func (ao AddressObject) Members() []string {
	var out []string
	for _, member := range append(ao.Card.Members(), ao.Card.Values(appleMemberField)...) {
		if uid := strings.TrimPrefix(member, memberPrefix); uid != "" {
			out = append(out, uid)
		}
	}
	return out
}

// Categories are the words the contact is filed under (RFC 6350 6.7.1).
func (ao AddressObject) Categories() []string {
	words := ao.Card.Categories()
	slices.Sort(words)
	return slices.Compact(words)
}

// SetMembers writes a group's membership, replacing what was there.
func SetMembers(card vcard.Card, uids []string) {
	delete(card, appleMemberField)
	members := make([]string, len(uids))
	for i, uid := range uids {
		members[i] = memberPrefix + uid
	}
	card.SetMembers(members)
}

// The colour is on the card, so it travels with it (ADR 17).
const colorField = "X-ALBORZ-COLOR"

// Color is the contact's own colour, one of the seven, or empty.
func (ao AddressObject) Color() string {
	return CardColor(ao.Card)
}

// CardColor is the colour written on a card, one of the seven, or empty.
func CardColor(card vcard.Card) string {
	name := card.Value(colorField)
	if !slices.Contains(alborz.FlagColors[:], name) {
		return ""
	}
	return name
}

// SetColor writes one of the seven on a card, or clears it.
func SetColor(card vcard.Card, name string) {
	if name == "" {
		delete(card, colorField)
		return
	}
	card.SetValue(colorField, name)
}

// Modified is when the card last changed; vCard has no property for its creation.
func (ao AddressObject) Modified() time.Time {
	return cardModified(ao.AddressObject)
}
