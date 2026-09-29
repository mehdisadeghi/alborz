package contacts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
	"golang.org/x/image/draw"
)

var ErrNoAddressBook = alborz.NotFound("notfound.addressbook")

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
	MemberPrefix     = "urn:uuid:"
)

// IsGroup reports whether the card is a group of contacts.
func (ao AddressObject) IsGroup() bool {
	return ao.Card.Kind() == vcard.KindGroup || strings.EqualFold(ao.Card.Value(appleKindField), string(vcard.KindGroup))
}

// UID is the card's UID without the URN wrapper.
func (ao AddressObject) UID() string {
	return strings.TrimPrefix(ao.Card.Value(vcard.FieldUID), MemberPrefix)
}

// Members are the UIDs a group names, in the order the card lists them.
func (ao AddressObject) Members() []string {
	var out []string
	for _, member := range append(ao.Card.Members(), ao.Card.Values(appleMemberField)...) {
		if uid := strings.TrimPrefix(member, MemberPrefix); uid != "" {
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
		members[i] = MemberPrefix + uid
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
	return Modified(ao.AddressObject)
}

const UIDPrefix = "urn:uuid:"

func WriteCard(ctx context.Context, c *carddav.Client, at string, card vcard.Card) error {
	card.SetRevision(time.Now())
	_, err := c.PutAddressObject(ctx, at, card, nil)
	return err
}

func ChangeCard(ctx context.Context, ref pim.Ref[*carddav.Client], change func(vcard.Card)) error {
	ao, err := ref.Client.GetAddressObject(ctx, ref.Path)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	change(ao.Card)
	return WriteCard(ctx, ref.Client, ao.Path, ao.Card)
}

func Join(cards [][]byte) ([]byte, error) {
	var buf bytes.Buffer
	for _, card := range cards {
		buf.Write(card)
		if !bytes.HasSuffix(card, []byte("\n")) {
			buf.WriteString("\r\n")
		}
	}
	return buf.Bytes(), nil
}

// WarmAccount reads the login's address books, so the pages open from the mirror.
func (p *Books) WarmAccount(ctx context.Context, s *alborz.Login) error {
	c, books, err := p.Open(ctx, s)
	if err != nil {
		return err
	}
	query := Query("")
	for _, r := range dav.Each(ctx, books, func(ctx context.Context, book dav.Collection) (int, error) {
		_, err := c.QueryAddressBook(ctx, book.Path, &query)
		return 0, err
	}) {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// Books are every account's address books.
type Books struct {
	DAV *dav.Provider
	// remembering keeps two writers from making the account's Previous recipients twice.
	remembering sync.Mutex
}

// Client is the login's CardDAV client.
func (p *Books) Client(ctx context.Context, login *alborz.Login) (*carddav.Client, error) {
	return newClient(dav.Base(), p.DAV.HTTPClient(ctx, login))
}

func findHome(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	c, err := carddav.NewClient(client, endpoint)
	if err != nil {
		return "", err
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to query CardDAV principal: %w", err)
	}
	homeSet, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("failed to query CardDAV address book home set: %w", err)
	}
	return homeSet, nil
}

// Open is the login's client with its address books.
func (p *Books) Open(ctx context.Context, login *alborz.Login) (*carddav.Client, []dav.Collection, error) {
	c, infos, err := dav.Opened(ctx, p.DAV, login, p.Client)
	if err != nil {
		return nil, nil, err
	}
	if len(infos) == 0 {
		return nil, nil, ErrNoAddressBook
	}
	return c, infos, nil
}

// New finds each served domain's CardDAV server.
func New(srv *alborz.Server, st *store.Store) (*Books, error) {
	provider, err := dav.NewProvider(srv, st, dav.Kind{
		Name: "carddav", Label: "CardDAV",
		Schemes: [2]string{"carddavs", "carddav+insecure"},
		// Address books change rarely.
		Poll:     10 * time.Minute,
		Discover: carddav.DiscoverContextURL,
		Own:      func(s dav.Services) string { return s.CardDAV },
		Holds:    store.AddressBook,
		FindHome: findHome,
		List:     listAddressBooks,
		Make:     makeBook,
		Change:   changeBook,
		Unnamed:  "contacts",
	})
	if err != nil {
		return nil, err
	}
	return &Books{DAV: provider}, nil
}

// Small on purpose: the card carries the picture inline, fetched on every visit.
const photoLongestSide = 320

var ErrPhotoUnreadable = errors.New("carddav: photo is not an image")

// Past this the bytes grow faster than a thumbnail improves.
const photoQuality = 82

// SetPhoto puts the picture into the card, made small.
func SetPhoto(card vcard.Card, r io.Reader) error {
	src, _, err := image.Decode(r)
	if err != nil {
		return ErrPhotoUnreadable
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
		return ErrPhotoUnreadable
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

func Modified(ao *carddav.AddressObject) time.Time {
	if rev, err := ao.Card.Revision(); err == nil && !rev.IsZero() {
		return rev
	}
	return ao.ModTime
}

// A birthday with no year (--0412) is shown as written.
func Birthday(card vcard.Card) string {
	if day, err := card.Birthday(); err == nil && !day.IsZero() {
		return day.Format("2006-01-02")
	}
	return card.Value(vcard.FieldBirthday)
}

// Matched by position, so a TYPE or PREF synced onto a field survives an edit that left it alone.
func WithValues(fields []*vcard.Field, values []string) []*vcard.Field {
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

func Query(queryText string) carddav.AddressBookQuery {
	// Every property: the list reads the colour, categories, revision and members.
	query := carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
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
	return query
}
