package contacts

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

type Settings struct {
	AddressBookFilter   bool
	VisibleAddressBooks []string
}

const settingsKey = "carddav.settings"

// A book made after the choice was not chosen to be hidden.
func show(ctx context.Context, store alborz.Store, path string) error {
	settings := &Settings{}
	if err := store.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	if !settings.AddressBookFilter {
		return nil
	}
	settings.VisibleAddressBooks = append(settings.VisibleAddressBooks, path)
	return store.Put(ctx, settingsKey, settings)
}

func choose(ctx context.Context, store alborz.Store, paths []string) error {
	settings := &Settings{}
	if err := store.Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
		return err
	}
	settings.AddressBookFilter, settings.VisibleAddressBooks = true, paths
	return store.Put(ctx, settingsKey, settings)
}

const uidURN = "urn:uuid:"

func registerRoutes(p *plugin) {
	guard := func(h func(*alborz.Context) error) func(*alborz.Context) error {
		return p.dav.Guarded(errNoAddressBook, "/address-books/create", h)
	}
	GET := func(path string, h func(*alborz.Context) error) { p.Router.GET(path, guard(h)) }
	POST := func(path string, h func(*alborz.Context) error) { p.Router.POST(path, guard(h)) }
	POST("/contacts", pim.HandleChoose("/contacts", "book", choose))
	GET("/contacts", p.contacts)
	POST("/contacts/export", pim.HandleExport(p.client, "/contacts",
		func(ctx *alborz.Context) string { return ctx.T("nav.contacts") + ".vcf" }, joinCards))
	POST("/contacts/refresh", p.dav.HandleRefresh("/contacts"))
	GET("/contacts/:path", p.contact)

	GET("/contacts/:path/raw", func(ctx *alborz.Context) error { return pim.Raw(ctx, p.client) })
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
	GET("/contacts/create", p.updateContact)
	POST("/contacts/create", p.updateContact)
	GET("/contacts/:path/edit", p.updateContact)
	POST("/contacts/:path/edit", p.updateContact)
	POST("/contacts/color", p.color)
	POST("/contacts/:path/group", p.groupContact)
	POST("/contacts/:path/color", p.color)
	POST("/contacts/:path/note", p.note)
	POST("/contacts/:path/photo/delete", p.deletePhoto)
	remove := pim.Handler(pim.Action[*carddav.Client]{Client: p.client, Do: pim.Delete[*carddav.Client], List: "/contacts",
		Done: func(ctx *alborz.Context, done []pim.Ref[*carddav.Client], _ string) alborz.Notice {
			return alborz.Notice{Kind: alborz.NoticeDone, Text: ctx.Tf("notice.contactsdeleted", len(done))}
		}})
	POST("/contacts/:path/delete", remove)
	POST("/contacts/delete", remove)
	POST("/contacts/from-message", p.importFromMessage)
	POST("/contacts/add", p.addAddress)
}

func writeCard(ctx *alborz.Context, c *carddav.Client, at string, card vcard.Card) error {
	card.SetRevision(time.Now())
	_, err := c.PutAddressObject(ctx.Request().Context(), at, card, nil)
	return err
}

func changeCard(ctx *alborz.Context, ref pim.Ref[*carddav.Client], change func(vcard.Card)) error {
	ao, err := ref.Client.GetAddressObject(ctx.Request().Context(), ref.Path)
	if err != nil {
		return fmt.Errorf("failed to read the contact: %v", err)
	}
	change(ao.Card)
	return writeCard(ctx, ref.Client, ao.Path, ao.Card)
}

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

func (p *plugin) warm(ctx *alborz.Context, s *alborz.Login) {
	p.dav.Warm(ctx, s, func(bg context.Context) error { return p.warmAccount(bg, s) })
}

func (p *plugin) warmAccount(ctx context.Context, s *alborz.Login) error {
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
