package contacts

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
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

func registerRoutes(p *plugin, g *web.Group) {
	guard := func(h func(*web.Context) error) func(*web.Context) error {
		return p.dav.Guarded(errNoAddressBook, "contacts.books.create", h)
	}
	GET := func(name, path string, h func(*web.Context) error) { g.GET(name, path, guard(h)) }
	POST := func(name, path string, h func(*web.Context) error) { g.POST(name, path, guard(h)) }
	upload := func(name, path string, limit int64, h, over func(*web.Context) error) {
		g.Upload(name, path, limit, guard(h), guard(over))
	}
	POST("list", "/contacts", pim.HandleChoose("contacts.list", "book", choose))
	GET("list", "/contacts", p.contacts)
	POST("export", "/contacts/export", pim.HandleExport(p.client, "contacts.list",
		func(ctx *web.Context) string { return ctx.T("nav.contacts") + ".vcf" }, joinCards))
	POST("refresh", "/contacts/refresh", p.dav.HandleRefresh("contacts.list"))
	GET("contact", "/contacts/{path}", p.contact)

	GET("contact.raw", "/contacts/{path}/raw", func(ctx *web.Context) error { return pim.Raw(ctx, p.client) })
	page := p.collectionPage()
	GET("books.create", "/address-books/create", page.HandleCreate(p.dav, p.createForm))
	POST("books.create", "/address-books/create", page.HandleCreate(p.dav, p.createForm))
	GET("exportall", "/contacts/export-all", page.HandleExportAll("contacts.list", "address-books.zip"))
	contactsImport, contactsOver := page.HandleImportPage(p.dav, "contacts.list", "nav.contacts", "contacts.import", "contacts.importhint", "contact", false)
	GET("import", "/contacts/import", contactsImport)
	upload("import", "/contacts/import", pim.ImportBodySize, contactsImport, contactsOver)
	GET("book", "/address-books/{path}", page.Handle(p.dav))
	POST("book", "/address-books/{path}", page.Handle(p.dav))
	POST("book.delete", "/address-books/{path}/delete", page.HandleDelete(p.dav))
	upload("book.import", "/address-books/{path}/import", pim.ImportBodySize, page.HandleImport(p.dav), page.HandleImportTooLarge)
	GET("book.export", "/address-books/{path}/export", page.HandleExport(p.dav))
	POST("book.share", "/address-books/{path}/share", page.HandleShare(p.dav))
	POST("book.unshare", "/address-books/{path}/unshare", page.HandleUnshare(p.dav))
	POST("book.access", "/address-books/{path}/access", page.HandleAccess(p.dav))
	POST("book.accept", "/address-books/{path}/accept", page.HandleAnswer(p.dav, true))
	POST("book.decline", "/address-books/{path}/decline", page.HandleAnswer(p.dav, false))
	GET("contact.create", "/contacts/create", p.updateContact)
	upload("contact.create", "/contacts/create", photoBodySize, p.updateContact, photoTooLarge)
	GET("contact.edit", "/contacts/{path}/edit", p.updateContact)
	upload("contact.edit", "/contacts/{path}/edit", photoBodySize, p.updateContact, photoTooLarge)
	POST("color", "/contacts/color", p.color)
	POST("contact.group", "/contacts/{path}/group", p.groupContact)
	POST("contact.color", "/contacts/{path}/color", p.color)
	POST("contact.note", "/contacts/{path}/note", p.note)
	POST("contact.photo.delete", "/contacts/{path}/photo/delete", p.deletePhoto)
	remove := pim.Handler(pim.Action[*carddav.Client]{Client: p.client, Do: pim.Delete[*carddav.Client], List: "contacts.list",
		Done: func(ctx *web.Context, done []pim.Ref[*carddav.Client], _ string) web.Notice {
			return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.contactsdeleted", len(done))}
		}})
	POST("contact.delete", "/contacts/{path}/delete", remove)
	POST("delete", "/contacts/delete", remove)
	POST("frommessage", "/contacts/from-message", p.importFromMessage)
	POST("add", "/contacts/add", p.addAddress)
}

func writeCard(ctx *web.Context, c *carddav.Client, at string, card vcard.Card) error {
	card.SetRevision(time.Now())
	_, err := c.PutAddressObject(ctx.Request().Context(), at, card, nil)
	return err
}

func changeCard(ctx *web.Context, ref pim.Ref[*carddav.Client], change func(vcard.Card)) error {
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

func (p *plugin) warm(ctx *web.Context, s *alborz.Login) {
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
