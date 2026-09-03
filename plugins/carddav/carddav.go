package alborzcarddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

// The account's domain has no CardDAV server, or it holds no book;
// the HTTP layer answers 404 rather than crashing on direct URLs.
var errNoAddressBook = alborz.NotFoundf("carddav: no address book found")

type AddressBookInfo struct {
	dav.Collection
	Visible bool
	// Only marks the collection a URL is currently narrowed to, and only
	// when it is the sole one: pressing the same link again is how a
	// reader gets back out of a view they pressed their way into.
	Only bool

	// Account owning the book, set only in the unified view
	Account string
}

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
func listAddressBooks(ctx context.Context, client *http.Client, baseURL *url.URL, homeSet string) ([]AddressBookInfo, error) {
	listed, err := dav.ListCollections[davCollectionProps](ctx, client, baseURL, homeSet, `<D:propfind xmlns:D="DAV:" xmlns:I="http://inf-it.com/ns/ab/"><D:prop><D:resourcetype/><D:displayname/><I:addressbook-color/><D:current-user-privilege-set/></D:prop></D:propfind>`)
	if err != nil {
		return nil, err
	}
	infos := make([]AddressBookInfo, len(listed))
	for i, l := range listed {
		infos[i] = AddressBookInfo{Collection: l.Collection}
	}
	return infos, nil
}

// doMkcol makes an address book. RFC 5689 extended MKCOL is the way to
// make one with its properties in the same round trip; a server that
// refuses it leaves the collection unmade rather than half made.
func doMkcol(ctx context.Context, client *http.Client, target, name, color string) error {
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

// createAddressBook makes a book on the account's own server, walking
// to the first free address: two collections may share a display name
// but not a path, and a server answers a taken one with 405.
func (p *plugin) createAddressBook(ctx context.Context, session *alborz.Session, name, color string) error {
	c, err := p.client(session)
	if err != nil {
		return err
	}
	davBase, _ := p.dav.URL(session)

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("failed to query CardDAV principal: %v", err)
	}
	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return fmt.Errorf("failed to query CardDAV address book home set: %v", err)
	}

	client := p.dav.HTTPClient(session)
	err = dav.CreateCollection(ctx, davBase, homeSet, name, "contacts", func(ctx context.Context, target string) error {
		return doMkcol(ctx, client, target, name, color)
	})
	if err != nil {
		return err
	}
	p.books.Forget(session.Username())
	return nil
}

// Modified is when the card last changed, for the list to show. There
// is no matching Added: a vCard has no property for when it was made,
// and the only universal answer is WebDAV's DAV:creationdate, which the
// object REPORT does not ask for.
func (ao AddressObject) Modified() time.Time {
	return cardModified(ao.AddressObject)
}
