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

// The account's domain has no CardDAV server, or it holds no book;
// the HTTP layer answers 404 rather than crashing on direct URLs.
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

// addressBookColor is the property the DAV address book clients agree
// on for a book's colour.
var addressBookColor = dav.Prop{XMLNS: "http://inf-it.com/ns/ab/", Name: "addressbook-color"}

// listAddressBooks fetches the address book list with names and colors in a
// single PROPFIND.
func listAddressBooks(ctx context.Context, client *http.Client, baseURL *url.URL, homes []dav.Home) ([]dav.Collection, []error) {
	listed, failed := dav.ListCollections[davCollectionProps](ctx, client, baseURL, homes, `<D:propfind xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/"><D:prop><D:resourcetype/><D:displayname/><I:addressbook-color/><D:current-user-privilege-set/></D:prop></D:propfind>`)
	infos := make([]dav.Collection, len(listed))
	for i, l := range listed {
		infos[i] = l.Collection
	}
	return infos, failed
}

// doMkcol makes an address book. RFC 5689 extended MKCOL is the way to
// make one with its properties in the same round trip; a server that
// refuses it leaves the collection unmade rather than half made.
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

	// Account owning the contact, set only in the unified view
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

// A group is a card, not a list somebody keeps beside the cards: vCard
// says so (RFC 6350 6.1.4 KIND:group, 6.6.5 MEMBER), and a CardDAV
// server stores it like any other object. Apple writes the same two
// properties with its own names on a 3.0 card, which is what a group
// made on a phone looks like, so both are read and the standard pair is
// written - alborz creates 4.0 cards.
const (
	appleKindField   = "X-ADDRESSBOOKSERVER-KIND"
	appleMemberField = "X-ADDRESSBOOKSERVER-MEMBER"
	// memberPrefix is how a member names a card: its UID as a URN
	// (RFC 6350 6.6.5, RFC 4122).
	memberPrefix = "urn:uuid:"
)

// IsGroup reports whether the card is a group of contacts rather than
// one contact.
func (ao AddressObject) IsGroup() bool {
	return strings.EqualFold(ao.Card.Value(vcard.FieldKind), string(vcard.KindGroup)) ||
		strings.EqualFold(ao.Card.Value(appleKindField), string(vcard.KindGroup))
}

// UID is the card's own identity, without the URN wrapper a member
// reference carries.
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

// Categories are the words the reader filed the contact under
// (RFC 6350 6.7.1): the other way vCard says a contact belongs with
// others, and the one every client shows.
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

// colorField is where a contact's colour lives: a property of the card,
// so the mark travels with it the way a mail's colour lives on the
// server rather than in the browser that set it. ADR 17.
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

// SetColor writes one of the seven on a card, or clears it. Anything
// else is a caller's bug: the seven are what the interface offers.
func SetColor(card vcard.Card, name string) {
	if name == "" {
		delete(card, colorField)
		return
	}
	card.SetValue(colorField, name)
}

// Modified is when the card last changed, for the list to show. There
// is no matching Added: a vCard has no property for when it was made,
// and the only universal answer is WebDAV's DAV:creationdate, which the
// object REPORT does not ask for.
func (ao AddressObject) Modified() time.Time {
	return cardModified(ao.AddressObject)
}
