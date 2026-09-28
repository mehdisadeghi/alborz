package alborzcarddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	alborzbase "git.mehdix.org/alborz/plugins/base"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

var errNoAddressBook = alborz.NotFound("notfound.addressbook")

type davCollectionProps struct {
	ResourceType struct {
		AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook,omitempty"`
	} `xml:"resourcetype"`
	DisplayName      string         `xml:"displayname"`
	AddressBookColor string         `xml:"http://inf-it.com/ns/ab/ addressbook-color"`
	PrivilegeSet     dav.Privileges `xml:"current-user-privilege-set>privilege"`
}

func (p davCollectionProps) Collection() (name, color string, ok bool) {
	return p.DisplayName, p.AddressBookColor, p.ResourceType.AddressBook != nil
}

func (p davCollectionProps) Privileges() dav.Privileges { return p.PrivilegeSet }

var addressBookColor = dav.Prop{XMLNS: "http://inf-it.com/ns/ab/", Name: "addressbook-color"}

func listAddressBooks(ctx context.Context, client *http.Client, baseURL *url.URL, homes []dav.Home) ([]dav.Collection, []error) {
	listed, failed := dav.ListCollections[davCollectionProps](ctx, client, baseURL, homes, `<D:propfind xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/"><D:prop><D:resourcetype/><D:displayname/><I:addressbook-color/><D:current-user-privilege-set/></D:prop></D:propfind>`)
	infos := make([]dav.Collection, len(listed))
	for i, l := range listed {
		infos[i] = l.Collection
	}
	return infos, failed
}

// Extended MKCOL (RFC 5689) sets the properties in the same round trip.
func doMkcol(ctx context.Context, client *http.Client, target, name, color string, _ []string) error {
	var props bytes.Buffer
	props.WriteString(`<D:resourcetype><D:collection/><A:addressbook/></D:resourcetype>`)
	fmt.Fprintf(&props, "<D:displayname>%s</D:displayname>", dav.XMLEscape(name))
	if color != "" {
		fmt.Fprintf(&props, "<I:addressbook-color>%s</I:addressbook-color>", dav.XMLEscape(color))
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<D:mkcol xmlns:D="DAV:" xmlns:A="urn:ietf:params:xml:ns:carddav" xmlns:I="http://inf-it.com/ns/ab/"><D:set><D:prop>%s</D:prop></D:set></D:mkcol>`, props.String())

	return dav.MakeCollection(ctx, client, "MKCOL", target, buf.Bytes())
}

func newClient(u *url.URL, httpClient *http.Client) (*carddav.Client, error) {
	return carddav.NewClient(httpClient, u.String())
}

type AddressObject struct {
	*carddav.AddressObject

	Account string
}

func (ao AddressObject) URL() string {
	return "/contacts/" + url.PathEscape(ao.Path)
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
	return strings.EqualFold(ao.Card.Value(vcard.FieldKind), string(vcard.KindGroup)) ||
		strings.EqualFold(ao.Card.Value(appleKindField), string(vcard.KindGroup))
}

// UID is the card's UID without the URN wrapper.
func (ao AddressObject) UID() string {
	return strings.TrimPrefix(ao.Card.Value(vcard.FieldUID), memberPrefix)
}

// Members are the UIDs a group names, in the order the card lists them.
func (ao AddressObject) Members() []string {
	var out []string
	for _, field := range []string{vcard.FieldMember, appleMemberField} {
		for _, f := range ao.Card[field] {
			if uid := strings.TrimPrefix(f.Value, memberPrefix); uid != "" {
				out = append(out, uid)
			}
		}
	}
	return out
}

// Categories are the words the contact is filed under (RFC 6350 6.7.1).
func (ao AddressObject) Categories() []string {
	var out []string
	for _, f := range ao.Card[vcard.FieldCategories] {
		for _, word := range strings.Split(f.Value, ",") {
			if word = strings.TrimSpace(word); word != "" {
				out = append(out, word)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// SetMembers writes a group's membership, replacing what was there.
func SetMembers(card vcard.Card, uids []string) {
	delete(card, appleMemberField)
	delete(card, vcard.FieldMember)
	for _, uid := range uids {
		card.Add(vcard.FieldMember, &vcard.Field{Value: memberPrefix + uid})
	}
}

// SetCategories writes the words a contact is filed under.
func SetCategories(card vcard.Card, words []string) {
	if len(words) == 0 {
		delete(card, vcard.FieldCategories)
		return
	}
	card.SetValue(vcard.FieldCategories, strings.Join(words, ","))
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
	if !slices.Contains(alborzbase.FlagColors[:], name) {
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
