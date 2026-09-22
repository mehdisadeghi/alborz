package alborzcarddav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"uuid"

	alborzbase "git.mehdix.org/alborz/plugins/base"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
	"github.com/labstack/echo/v4"
)

// createForm is the form for a new address book. The first book is made
// from a rail that lists none.
func (p *plugin) createForm(ctx *alborz.Context) (dav.CreateForm, error) {
	rail, err := p.bookRail(ctx)
	if err != nil && !errors.Is(err, errNoAddressBook) {
		return dav.CreateForm{}, err
	}
	return dav.CreateForm{Rail: rail, Title: ctx.T("contacts.newbook"), Section: ctx.T("nav.contacts"), List: "/contacts",
		Made: func(string) (string, string) { return ctx.T("notice.bookcreated"), "/contacts" }}, nil
}

// collectionPage is an address book's own page.
func (p *plugin) collectionPage() dav.Page {
	return dav.Page{
		Base:   "/address-books/",
		List:   "/contacts",
		Color:  addressBookColor,
		Ext:    ".vcf",
		Forget: p.dav.Forget,
		Show:   show,
		Rail:   func(ctx *alborz.Context, _ string) (dav.Rail, error) { return p.bookRail(ctx) },
		Create: func(ctx *alborz.Context, _, name, place string) (string, error) {
			return p.dav.Create(ctx.Request().Context(), ctx.Session, name, dav.DefaultColor, place, nil)
		},
		Import: func(ctx *alborz.Context, path string, raw []byte) (int, error) {
			c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return 0, err
			}
			return importBook(ctx.Request().Context(), c, path, raw)
		},
		Export: func(ctx *alborz.Context, path string, _, _ time.Time) ([]byte, error) {
			c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return nil, err
			}
			return exportBook(ctx.Request().Context(), c, path)
		},
		Lookup: func(ctx *alborz.Context, path string) (dav.Collection, func() int, string, string, error) {
			_, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Session)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(books, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			return *info, p.dav.CountObjects(ctx, info.Path), "/contacts", ctx.T("nav.contacts"), nil
		},
	}
}

// bookRail is the section's rail for a page that is not its list,
// listing every account's address books.
func (p *plugin) bookRail(ctx *alborz.Context) (dav.Rail, error) {
	rail := dav.Rail{Path: "/contacts", Field: "book", ItemClass: "addressbook-item", Action: "/contacts", EditHref: "/address-books/",
		NewHref: "/address-books/create?next=" + alborz.QueryValue(ctx.Request().URL.RequestURI()), NewLabel: ctx.T("contacts.newbook"),
		ImportHref: "/contacts/import", ImportLabel: ctx.T("contacts.import")}
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return rail, err
	}
	infos, _, err := visibleBooks(accounts, ctx.URLAccount(), nil)
	rail.Items = infos
	if err != nil {
		return rail, err
	}
	// Groups and categories are narrowings of the list, so the rail
	// carries them wherever the list's pages are (the list itself, an
	// object, its edit). The reads behind this are cached.
	list, err := p.contactList(ctx)
	if err != nil {
		return rail, err
	}
	rail.Groups = contactGroupRows(list.Groups, list.Group, ctx.URLAccount())
	rail.Categories = contactCategoryRows(list.Categories, list.Category, ctx.URLAccount())
	return rail, nil
}

// visibleBooks are dav.Visible's address books.
func visibleBooks(accounts []dav.Account[*carddav.Client], scope string, only map[string]bool) ([]dav.Collection, []dav.Site[*carddav.Client], error) {
	return dav.Visible(accounts, scope, only, func(acc dav.Account[*carddav.Client]) ([]dav.Collection, bool, []string, error) {
		settings := &Settings{}
		if err := acc.Session.Store().Get(settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return nil, false, nil, fmt.Errorf("failed to load CardDAV settings: %w", err)
		}
		return acc.Collections, settings.AddressBookFilter, settings.VisibleAddressBooks, nil
	})
}

// hasAttachment reports whether the message carries a part of one of
// the media types.
func hasAttachment(msg *alborzbase.IMAPMessage, types ...string) bool {
	for _, att := range msg.Attachments() {
		for _, t := range types {
			if strings.EqualFold(att.MIMEType, t) {
				return true
			}
		}
	}
	return false
}

// importFromMessage files the cards attached to a mail into the chosen
// address book.
func (p *plugin) importFromMessage(ctx *alborz.Context) error {
	mboxName, uid, err := alborzbase.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	raw, _, err := alborzbase.PartAt(ctx, mboxName, uid, ctx.FormValue("part"))
	if err != nil {
		return err
	}
	acct, bookPath, ok := strings.Cut(ctx.FormValue("addressbook"), "|")
	session := ctx.SessionFor(acct)
	if !ok || session == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "no address book to file it in")
	}
	c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), session)
	if err != nil {
		return err
	}
	if book := dav.At(books, bookPath); book == nil || !book.Writable {
		return echo.NewHTTPError(http.StatusBadRequest, "no address book to file it in")
	}
	n, err := importBook(ctx.Request().Context(), c, bookPath, raw)
	if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(alborz.Notice{Kind: alborz.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import.vcf", n))
	}
	to := "/contacts"
	if acct != ctx.Session.Username() {
		to += "?account=" + alborz.QueryValue(acct)
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
}

// importBook writes every card of a vCard stream into the book and
// reports how many. A UID the book already holds is written over.
func importBook(ctx context.Context, client *carddav.Client, bookPath string, raw []byte) (int, error) {
	dec := vcard.NewDecoder(bytes.NewReader(raw))
	n := 0
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("failed to read the card: %v", err)
		}
		if _, ok := card[vcard.FieldVersion]; !ok {
			card.SetValue(vcard.FieldVersion, "4.0")
		}
		uid := card.Value(vcard.FieldUID)
		if uid == "" {
			uid = uidURN + uuid.New().String()
			card.SetValue(vcard.FieldUID, uid)
		}
		target := path.Join(bookPath, dav.SafeObjectName(uid)+".vcf")
		if _, err := client.PutAddressObject(ctx, target, card, nil); err != nil {
			existing := cardPathByUID(ctx, client, bookPath, uid)
			if existing == "" {
				return n, fmt.Errorf("failed to write %s: %v", uid, err)
			}
			if _, err := client.PutAddressObject(ctx, existing, card, nil); err != nil {
				return n, fmt.Errorf("failed to write %s: %v", uid, err)
			}
		}
		n++
	}
}

// cardPathByUID is where the book keeps the card with that UID, "" when
// it has none.
func cardPathByUID(ctx context.Context, client *carddav.Client, bookPath, uid string) string {
	objects, err := client.QueryAddressBook(ctx, bookPath, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldUID}},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldUID, TextMatches: []carddav.TextMatch{{Text: uid, MatchType: carddav.MatchEquals}}}},
	})
	if err != nil || len(objects) == 0 {
		return ""
	}
	return objects[0].Path
}

// exportBook renders every card of the book as one vCard file.
func exportBook(ctx context.Context, client *carddav.Client, bookPath string) ([]byte, error) {
	objects, err := client.QueryAddressBook(ctx, bookPath, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read the address book: %v", err)
	}
	var buf bytes.Buffer
	enc := vcard.NewEncoder(&buf)
	for _, obj := range objects {
		if err := enc.Encode(obj.Card); err != nil {
			return nil, fmt.Errorf("failed to write a card: %v", err)
		}
	}
	return buf.Bytes(), nil
}
