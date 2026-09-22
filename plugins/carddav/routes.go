package alborzcarddav

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

type Settings struct {
	AddressBookFilter   bool
	VisibleAddressBooks []string
}

const settingsKey = "carddav.settings"

// show ticks a book the account just made or accepted in the chosen set
// in force: what to see was chosen before it existed, which was not a
// choice to hide it.
func show(store alborz.Store, path string) error {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	if !settings.AddressBookFilter {
		return nil
	}
	settings.VisibleAddressBooks = append(settings.VisibleAddressBooks, path)
	return store.Put(settingsKey, settings)
}

// choose writes the books the reader ticked into the settings.
func choose(store alborz.Store, paths []string) error {
	settings := &Settings{}
	if err := store.Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	settings.AddressBookFilter, settings.VisibleAddressBooks = true, paths
	return store.Put(settingsKey, settings)
}

func init() {
	alborz.KeepKey(settingsKey)
}

// uidURN is how a UUID is written as a URN (RFC 9562 4), the form vCard
// UIDs take by convention (RFC 6350 6.7.6).
const uidURN = "urn:uuid:"

// getAddressObject fetches one card without go-webdav's response
// parsing. Its populateAddressObject runs the ETag through
// strconv.Unquote, which rejects the weak form ("W/\"...\"") that
// Nextcloud sends, and every edit of a contact there died with a bare
// "invalid syntax". Nothing here needs the ETag: the PUT that saves the
// card sends no If-Match.
func getAddressObject(ctx *alborz.Context, c *carddav.Client, path string) (*carddav.AddressObject, error) {
	body, err := c.Open(ctx.Request().Context(), path)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	card, err := vcard.NewDecoder(body).Decode()
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: path, Card: card}, nil
}

func registerRoutes(p *plugin) {
	guard := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return p.dav.Guarded(errNoAddressBook, "contacts.unconfigured", "/address-books/create", h)
	}
	GET := func(path string, h func(*alborz.Context) error) { p.GoPlugin.GET(path, guard(h)) }
	POST := func(path string, h func(*alborz.Context) error) { p.GoPlugin.POST(path, guard(h)) }
	POST("/contacts", dav.HandleChoose("/contacts", "book", choose))
	GET("/contacts", p.contacts)
	POST("/contacts/export", dav.HandleExport(p.client, "/contacts",
		func(ctx *alborz.Context) string { return ctx.T("nav.contacts") + ".vcf" }, joinCards))
	POST("/contacts/refresh", p.dav.HandleRefresh("/contacts"))
	GET("/contacts/:path", p.contact)

	GET("/contacts/:path/raw", func(ctx *alborz.Context) error { return dav.Raw(ctx, p.client) })
	page := p.collectionPage()
	GET("/address-books/create", page.HandleCreate(p.dav, p.createForm))
	POST("/address-books/create", page.HandleCreate(p.dav, p.createForm))
	GET("/contacts/export-all", page.HandleExportAll("/contacts", "address-books.zip"))
	GET("/contacts/import", page.HandleImportPage(p.dav, "/contacts", "nav.contacts", "contacts.import", "contacts.importhint", "contact", false))
	POST("/contacts/import", page.HandleImportPage(p.dav, "/contacts", "nav.contacts", "contacts.import", "contacts.importhint", "contact", false))
	GET("/address-books/:path", page.Handle(p.dav))
	POST("/address-books/:path", page.Handle(p.dav))
	POST("/address-books/:path/delete", page.HandleDelete(p.dav))
	POST("/address-books/:path/import", page.HandleImport(p.dav))
	GET("/address-books/:path/export", page.HandleExport(p.dav))
	POST("/address-books/:path/share", page.HandleShare(p.dav))
	POST("/address-books/:path/unshare", page.HandleUnshare(p.dav))
	POST("/address-books/:path/access", page.HandleAccess(p.dav))
	POST("/address-books/:path/accept", page.HandleAnswer(p.dav, true))
	POST("/address-books/:path/decline", page.HandleAnswer(p.dav, false))
	p.Inject("*", p.dav.InjectInvitations("book", page.Base, "/contacts", "/address-books"))
	p.Inject("message.html", page.InjectOffer(p.dav))
	GET("/contacts/create", p.updateContact)
	POST("/contacts/create", p.updateContact)
	GET("/contacts/:path/edit", p.updateContact)
	POST("/contacts/:path/edit", p.updateContact)
	POST("/contacts/color", p.color)
	POST("/contacts/:path/group", p.groupContact)
	POST("/contacts/:path/color", p.color)
	POST("/contacts/:path/note", p.note)
	POST("/contacts/:path/photo/delete", p.deletePhoto)
	remove := dav.Handler(dav.Action[*carddav.Client]{Client: p.client, Do: dav.Delete[*carddav.Client], List: "/contacts",
		Done: func(ctx *alborz.Context, done []dav.Ref[*carddav.Client], _ string) alborz.Notice {
			return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.contactsdeleted", len(done))}
		}})
	POST("/contacts/:path/delete", remove)
	POST("/contacts/delete", remove)
	POST("/contacts/from-message", p.importFromMessage)
}

func writeCard(ctx *alborz.Context, c *carddav.Client, at string, card vcard.Card) error {
	card.SetValue(vcard.FieldRevision, time.Now().UTC().Format("20060102T150405Z"))
	_, err := c.PutAddressObject(ctx.Request().Context(), at, card, nil)
	return err
}

// changeCard reads one card, lets change rewrite it, and writes it back
// with a fresh REV. The read is deliberately not go-webdav's: see
// getAddressObject for the ETag it cannot parse.
func changeCard(ctx *alborz.Context, ref dav.Ref[*carddav.Client], change func(vcard.Card)) error {
	ao, err := getAddressObject(ctx, ref.Client, ref.Path)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	change(ao.Card)
	return writeCard(ctx, ref.Client, ao.Path, ao.Card)
}

// joinCards writes the cards one after another, which is all a file of
// vCards is.
func joinCards(cards [][]byte) ([]byte, error) {
	var buf bytes.Buffer
	for _, card := range cards {
		buf.Write(card)
		if !bytes.HasSuffix(card, []byte("\n")) {
			buf.WriteString("\r\n")
		}
	}
	return buf.Bytes(), nil
}

// warm fetches what the contacts page asks for first, as the account
// is signed in, so the first click finds it cached: the books and
// every card the list shows.
func (p *plugin) warm(ctx *alborz.Context, s *alborz.Session) {
	p.dav.Warm(ctx, s, func(bg context.Context) error { return p.warmAccount(bg, s) })
}

func (p *plugin) warmAccount(ctx context.Context, s *alborz.Session) error {
	c, books, err := p.clientWithAddressBooks(ctx, s)
	if err != nil {
		return err
	}
	query := contactsQuery("")
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
