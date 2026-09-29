package web

import (
	"context"
	"errors"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/contacts"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav/carddav"
)

func registerRoutes(p *plugin, g *web.Group) {
	guard := func(h func(*web.Context) error) func(*web.Context) error {
		return p.DAV.Guarded(contacts.ErrNoAddressBook, "contacts.books.create", h)
	}
	GET := func(name, path string, h func(*web.Context) error) { g.GET(name, path, guard(h)) }
	POST := func(name, path string, h func(*web.Context) error) { g.POST(name, path, guard(h)) }
	upload := func(name, path string, limit int64, h, over func(*web.Context) error) {
		g.Upload(name, path, limit, guard(h), guard(over))
	}
	POST("list", "/contacts", pim.HandleChoose("contacts.list", "book", contacts.Choose))
	GET("list", "/contacts", p.contacts)
	POST("export", "/contacts/export", pim.HandleExport(p.Client, "contacts.list",
		func(ctx *web.Context) string { return ctx.T("nav.contacts") + ".vcf" }, contacts.Join))
	POST("refresh", "/contacts/refresh", p.DAV.HandleRefresh("contacts.list"))
	GET("contact", "/contacts/{path}", p.contact)

	GET("contact.raw", "/contacts/{path}/raw", func(ctx *web.Context) error { return pim.Raw(ctx, p.Client) })
	page := p.collectionPage()
	GET("books.create", "/address-books/create", page.HandleCreate(p.DAV, p.createForm))
	POST("books.create", "/address-books/create", page.HandleCreate(p.DAV, p.createForm))
	GET("exportall", "/contacts/export-all", page.HandleExportAll("contacts.list", "address-books.zip"))
	contactsImport, contactsOver := page.HandleImportPage(p.DAV, "contacts.list", "nav.contacts", "contacts.import", "contacts.importhint", "contact", false)
	GET("import", "/contacts/import", contactsImport)
	upload("import", "/contacts/import", pim.ImportBodySize, contactsImport, contactsOver)
	GET("book", "/address-books/{path}", page.Handle(p.DAV))
	POST("book", "/address-books/{path}", page.Handle(p.DAV))
	POST("book.delete", "/address-books/{path}/delete", page.HandleDelete(p.DAV))
	upload("book.import", "/address-books/{path}/import", pim.ImportBodySize, page.HandleImport(p.DAV), page.HandleImportTooLarge)
	GET("book.export", "/address-books/{path}/export", page.HandleExport(p.DAV))
	POST("book.share", "/address-books/{path}/share", page.HandleShare(p.DAV))
	POST("book.unshare", "/address-books/{path}/unshare", page.HandleUnshare(p.DAV))
	POST("book.access", "/address-books/{path}/access", page.HandleAccess(p.DAV))
	POST("book.accept", "/address-books/{path}/accept", page.HandleAnswer(p.DAV, true))
	POST("book.decline", "/address-books/{path}/decline", page.HandleAnswer(p.DAV, false))
	GET("contact.create", "/contacts/create", p.updateContact)
	upload("contact.create", "/contacts/create", photoBodySize, p.updateContact, photoTooLarge)
	GET("contact.edit", "/contacts/{path}/edit", p.updateContact)
	upload("contact.edit", "/contacts/{path}/edit", photoBodySize, p.updateContact, photoTooLarge)
	POST("color", "/contacts/color", p.color)
	POST("contact.group", "/contacts/{path}/group", p.groupContact)
	POST("contact.color", "/contacts/{path}/color", p.color)
	POST("contact.note", "/contacts/{path}/note", p.note)
	POST("contact.photo.delete", "/contacts/{path}/photo/delete", p.deletePhoto)
	remove := pim.Handler(pim.Action[*carddav.Client]{Client: p.Client, Do: pim.Delete[*carddav.Client], List: "contacts.list",
		Done: func(ctx *web.Context, done []pim.Ref[*carddav.Client], _ string) web.Notice {
			return web.Notice{Kind: web.NoticeDone, Text: ctx.Tf("notice.contactsdeleted", len(done))}
		}})
	POST("contact.delete", "/contacts/{path}/delete", remove)
	POST("delete", "/contacts/delete", remove)
	POST("frommessage", "/contacts/from-message", p.importFromMessage)
	POST("add", "/contacts/add", p.addAddress)
}

func (p *plugin) warm(ctx *web.Context, s *alborz.Login) {
	p.DAV.Warm(s, func(bg context.Context) error { return p.WarmAccount(bg, s) })
}

func (p *plugin) createForm(ctx *web.Context) (pim.CreateForm, error) {
	rail, err := p.bookRail(ctx)
	if err != nil && !errors.Is(err, contacts.ErrNoAddressBook) {
		return pim.CreateForm{}, err
	}
	return pim.CreateForm{Rail: rail, Title: ctx.T("contacts.newbook"), Section: ctx.T("nav.contacts"), List: ctx.Path("contacts.list"),
		Made: func(string) (string, string) { return ctx.T("notice.bookcreated"), ctx.Path("contacts.list") }}, nil
}

func (p *plugin) invitations(ctx *web.Context) (*dav.RailInvitations, error) {
	return p.DAV.RailInvitations(ctx, p.collectionPage().Collection)
}

func (p *plugin) collectionPage() pim.Page {
	return pim.Page{
		Collection: "contacts.book",
		List:       "contacts.list",
		Ext:        ".vcf",
		Forget:     p.DAV.Forget,
		Show:       contacts.Show,
		Rail:       func(ctx *web.Context, _ string) (pim.Rail, error) { return p.bookRail(ctx) },
		Create: func(ctx *web.Context, _, name, place string) (string, error) {
			return p.DAV.Create(ctx.Request().Context(), ctx.Login, name, dav.DefaultColor, place, nil)
		},
		Import: func(ctx *web.Context, path string, raw []byte) (int, error) {
			c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return 0, err
			}
			return contacts.Import(ctx.Request().Context(), c, path, raw)
		},
		Export: func(ctx *web.Context, path string, _, _ time.Time) ([]byte, error) {
			c, _, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return nil, err
			}
			return contacts.Export(ctx.Request().Context(), c, path)
		},
		Lookup: func(ctx *web.Context, path string) (dav.Collection, func() int, string, string, error) {
			_, books, err := p.Open(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(books, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			return *info, p.DAV.CountObjects(ctx.Request().Context(), ctx.Login, info.Path), ctx.Path("contacts.list"), ctx.T("nav.contacts"), nil
		},
	}
}

func (p *plugin) bookRail(ctx *web.Context) (pim.Rail, error) {
	rail := pim.Rail{Path: ctx.Path("contacts.list"), Field: "book", ItemClass: "addressbook-item", Action: ctx.Path("contacts.list"), Edit: "contacts.book",
		NewHref: ctx.Path("contacts.books.create") + "?next=" + web.QueryValue(ctx.Request().URL.RequestURI()), NewLabel: ctx.T("contacts.newbook"),
		ImportHref: ctx.Path("contacts.import"), ImportLabel: ctx.T("contacts.import")}
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return rail, err
	}
	if rail.Invitations, err = p.invitations(ctx); err != nil {
		return rail, err
	}
	infos, _, err := contacts.Visible(ctx.Request().Context(), accounts, ctx.URLAccount(), nil)
	rail.Items = infos
	if err != nil {
		return rail, err
	}
	// Groups and categories narrow the list, so the rail carries them on every list page.
	list, err := p.contactList(ctx)
	if err != nil {
		return rail, err
	}
	rail.Groups = contactGroupRows(ctx, list.Groups, list.Group, ctx.URLAccount())
	rail.Categories = contactCategoryRows(ctx, list.Categories, list.Category, ctx.URLAccount())
	return rail, nil
}
