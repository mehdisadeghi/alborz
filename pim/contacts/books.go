package contacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	netmail "net/mail"
	"path"
	"slices"
	"strings"
	"time"
	"uuid"

	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/web"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

func (p *plugin) createForm(ctx *web.Context) (pim.CreateForm, error) {
	rail, err := p.bookRail(ctx)
	if err != nil && !errors.Is(err, errNoAddressBook) {
		return pim.CreateForm{}, err
	}
	return pim.CreateForm{Rail: rail, Title: ctx.T("contacts.newbook"), Section: ctx.T("nav.contacts"), List: ctx.Path("contacts.list"),
		Made: func(string) (string, string) { return ctx.T("notice.bookcreated"), ctx.Path("contacts.list") }}, nil
}

func (p *plugin) invitations(ctx *web.Context) (*dav.RailInvitations, error) {
	return p.dav.RailInvitations(ctx, p.collectionPage().Collection)
}

func (p *plugin) collectionPage() pim.Page {
	return pim.Page{
		Collection: "contacts.book",
		List:       "contacts.list",
		Ext:        ".vcf",
		Forget:     p.dav.Forget,
		Show:       show,
		Rail:       func(ctx *web.Context, _ string) (pim.Rail, error) { return p.bookRail(ctx) },
		Create: func(ctx *web.Context, _, name, place string) (string, error) {
			return p.dav.Create(ctx.Request().Context(), ctx.Login, name, dav.DefaultColor, place, nil)
		},
		Import: func(ctx *web.Context, path string, raw []byte) (int, error) {
			c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return 0, err
			}
			return importBook(ctx.Request().Context(), c, path, raw)
		},
		Export: func(ctx *web.Context, path string, _, _ time.Time) ([]byte, error) {
			c, _, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return nil, err
			}
			return exportBook(ctx.Request().Context(), c, path)
		},
		Lookup: func(ctx *web.Context, path string) (dav.Collection, func() int, string, string, error) {
			_, books, err := p.clientWithAddressBooks(ctx.Request().Context(), ctx.Login)
			if err != nil {
				return dav.Collection{}, nil, "", "", err
			}
			info := dav.At(books, path)
			if info == nil {
				return dav.Collection{}, nil, "", "", alborz.NotFound("notfound.collection")
			}
			return *info, p.dav.CountObjects(ctx, info.Path), ctx.Path("contacts.list"), ctx.T("nav.contacts"), nil
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
	infos, _, err := visibleBooks(ctx.Request().Context(), accounts, ctx.URLAccount(), nil)
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

func visibleBooks(ctx context.Context, accounts []pim.Account[*carddav.Client], scope string, only map[string]bool) ([]dav.Collection, []pim.Site[*carddav.Client], error) {
	return pim.Visible(accounts, scope, only, func(acc pim.Account[*carddav.Client]) ([]dav.Collection, bool, []string, error) {
		settings := &Settings{}
		if err := acc.Login.Store().Get(ctx, settingsKey, settings); err != nil && err != alborz.ErrNoStoreEntry {
			return nil, false, nil, fmt.Errorf("failed to load CardDAV settings: %w", err)
		}
		return acc.Collections, settings.AddressBookFilter, settings.VisibleAddressBooks, nil
	})
}

func hasAttachment(msg *mail.IMAPMessage, types ...string) bool {
	for _, att := range msg.Attachments() {
		for _, t := range types {
			if strings.EqualFold(att.MIMEType, t) {
				return true
			}
		}
	}
	return false
}

func (p *plugin) importFromMessage(ctx *web.Context) error {
	mboxName, uid, err := mail.ParseMessageRef(ctx.FormValue("mbox"), ctx.FormValue("uid"))
	if err != nil {
		return err
	}
	raw, _, err := mail.PartAt(ctx, mboxName, uid, ctx.FormValue("part"))
	if err != nil {
		return err
	}
	acct, bookPath, ok := strings.Cut(ctx.FormValue("addressbook"), "|")
	login := ctx.LoginFor(acct)
	if !ok || login == nil {
		return web.NewError(http.StatusBadRequest, "no address book to file it in")
	}
	c, books, err := p.clientWithAddressBooks(ctx.Request().Context(), login)
	if err != nil {
		return err
	}
	if book := dav.At(books, bookPath); book == nil || !book.Writable {
		return web.NewError(http.StatusBadRequest, "no address book to file it in")
	}
	to := ctx.Path("contacts.list")
	if acct != ctx.Login.Username() {
		to += "?account=" + web.QueryValue(acct)
	}
	n, err := importBook(ctx.Request().Context(), c, bookPath, raw)
	if pim.ImportAnswered(err) {
		ctx.Notify(pim.ImportRefused(ctx, err))
		return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
	} else if err != nil {
		return err
	}
	if n == 0 {
		ctx.Notify(web.Notice{Kind: web.NoticeWarning, Text: ctx.T("import.nothing")})
	} else {
		ctx.PutNotice(ctx.Tf("import.vcf", n))
	}
	return ctx.Redirect(http.StatusFound, ctx.NextOr(to))
}

// importBook reads the whole file before writing, so a refusal can say how much of it was saved.
func importBook(ctx context.Context, client *carddav.Client, bookPath string, raw []byte) (int, error) {
	dec := vcard.NewDecoder(bytes.NewReader(raw))
	var cards []vcard.Card
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, &pim.UnreadableError{Err: err}
		}
		cards = append(cards, card)
	}
	n := 0
	for _, card := range cards {
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
				return n, &pim.PartlySavedError{Saved: n, Total: len(cards), Err: fmt.Errorf("failed to write %s: %w", uid, err)}
			}
			if _, err := client.PutAddressObject(ctx, existing, card, nil); err != nil {
				return n, &pim.PartlySavedError{Saved: n, Total: len(cards), Err: fmt.Errorf("failed to write %s: %w", uid, err)}
			}
		}
		n++
	}
	return n, nil
}

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

// distinct are the addresses each once, a named form kept over a bare one.
func distinct(addrs []*netmail.Address) []*netmail.Address {
	var out []*netmail.Address
	at := make(map[string]int)
	for _, a := range addrs {
		// A group's start and end carry no address.
		if !strings.ContainsRune(a.Address, '@') {
			continue
		}
		key := strings.ToLower(a.Address)
		if i, ok := at[key]; ok {
			if out[i].Name == "" {
				out[i].Name = a.Name
			}
			continue
		}
		at[key] = len(out)
		out = append(out, &netmail.Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// emails are the addresses the books hold, each with its card's name; a book that did not answer is in the error.
func emails(ctx context.Context, c *carddav.Client, books []dav.Collection) ([]*netmail.Address, error) {
	query := carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldFormattedName, vcard.FieldEmail}},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldEmail}},
	}
	// One query per book, run together so the caller waits one round trip.
	results := dav.Each(ctx, books, func(ctx context.Context, book dav.Collection) ([]carddav.AddressObject, error) {
		return c.QueryAddressBook(ctx, book.Path, &query)
	})
	var out []*netmail.Address
	var errs []error
	for _, result := range results {
		errs = append(errs, result.Err)
		for _, ao := range result.Value {
			name := ao.Card.Value(vcard.FieldFormattedName)
			for _, email := range ao.Card.Values(vcard.FieldEmail) {
				if email != "" {
					out = append(out, &netmail.Address{Name: name, Address: email})
				}
			}
		}
	}
	return out, errors.Join(errs...)
}

// addCard writes a new card for one address into book.
func addCard(ctx context.Context, c *carddav.Client, book string, addr *netmail.Address) (*carddav.AddressObject, error) {
	id := uuid.New().String()
	name := addr.Name
	if name == "" {
		name = addr.Address
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, uidURN+id)
	card.SetValue(vcard.FieldFormattedName, name)
	card.SetValue(vcard.FieldEmail, addr.Address)
	at, _, ifNoneMatch := pim.Target(book, id+".vcf", "", "")
	ao, err := c.PutAddressObject(ctx, at, card, &carddav.PutAddressObjectOptions{IfNoneMatch: ifNoneMatch})
	if err != nil {
		return nil, err
	}
	ao.Card = card
	return ao, nil
}

// addAddress adds one of a message's addresses to the address book chosen.
func (p *plugin) addAddress(ctx *web.Context) error {
	addr, err := netmail.ParseAddress(ctx.FormValue("address"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	to, err := pim.Destination(ctx, ctx.FormValue("addressbook"), p.clientWithAddressBooks, func(book dav.Collection) bool { return book.Writable })
	if errors.Is(err, pim.ErrNoDestination) {
		return web.NewError(http.StatusBadRequest, "no address book to add it to")
	} else if err != nil {
		return err
	}
	books, err := p.dav.Collections(ctx.Request().Context(), ctx.LoginFor(to.Account))
	if err != nil {
		return err
	}
	book := dav.At(books, to.Path)
	back := ctx.NextOr(ctx.URL("contacts.list"))
	held, err := emails(ctx.Request().Context(), to.Client, []dav.Collection{*book})
	if err != nil {
		return err
	}
	if slices.ContainsFunc(held, func(a *netmail.Address) bool { return strings.EqualFold(a.Address, addr.Address) }) {
		sentence := ctx.T("notice.contactknown")
		ctx.Notify(web.Notice{Kind: web.NoticeDone,
			Text:   fmt.Sprintf(sentence, addr.Address, book.Name),
			Markup: template.HTML(fmt.Sprintf(sentence, web.NoticeName(addr.Address), web.NoticeName(book.Name)))})
		return ctx.Redirect(http.StatusFound, back)
	}
	ao, err := addCard(ctx.Request().Context(), to.Client, to.Path, addr)
	if err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), err)})
		return ctx.Redirect(http.StatusFound, back)
	}
	named := AddressObject{AddressObject: ao}
	sentence := ctx.T("notice.contactcreated")
	href := ctx.Path("contacts.contact", named.Path) + "?account=" + web.QueryValue(to.Account)
	ctx.Notify(web.Notice{Kind: web.NoticeDone,
		Text:   fmt.Sprintf(sentence, named.DisplayName()),
		Markup: template.HTML(fmt.Sprintf(sentence, web.NoticeLink(named.DisplayName(), href)))})
	return ctx.Redirect(http.StatusFound, back)
}

// previousKey keeps, per account, what became of its Previous recipients.
const previousKey = "carddav.previous"

type previous struct {
	// Book is the one made for them, empty until the first address; one the reader deleted is not made again.
	Book string
	// Seeded is Sent read for them once.
	Seeded bool
}

func loadPrevious(ctx context.Context, s *alborz.Login) (previous, error) {
	var was previous
	if err := s.Store().Get(ctx, previousKey, &was); err != nil && err != alborz.ErrNoStoreEntry {
		return was, err
	}
	return was, nil
}

// Recent is the point: an old recipient is not who the writer reaches for.
const seedDepth = 200

// seed fills Previous recipients from Sent, once per account.
func (p *plugin) seed(ctx *web.Context, s *alborz.Login) {
	name, log := ctx.T("contacts.previous"), ctx.Logger()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), dav.WarmBudget)
		defer cancel()
		was, err := loadPrevious(bg, s)
		if err == nil && was.Seeded {
			return
		}
		var addrs []*netmail.Address
		if err == nil {
			addrs, err = mail.SentRecipients(s, seedDepth)
		}
		if err == nil {
			err = p.remember(bg, s, name, addrs, true)
		}
		if err != nil {
			log.Printf("carddav: previous recipients of %s: %v", s.Username(), err)
		}
	}()
}

// remember adds to Previous recipients the addresses no address book of the account holds.
func (p *plugin) remember(ctx context.Context, s *alborz.Login, name string, addrs []*netmail.Address, seeding bool) error {
	p.remembering.Lock()
	defer p.remembering.Unlock()
	was, err := loadPrevious(ctx, s)
	if err != nil {
		return err
	}
	if seeding && was.Seeded {
		return nil
	}
	now := was
	now.Seeded = was.Seeded || seeding
	c, err := p.client(ctx, s)
	if err != nil {
		return err
	}
	books, err := p.dav.Collections(ctx, s)
	if err != nil {
		return err
	}
	if now.Book == "" || dav.At(books, now.Book) != nil {
		held, err := emails(ctx, c, books)
		if err != nil {
			return err
		}
		known := make(map[string]bool)
		for _, a := range held {
			known[strings.ToLower(a.Address)] = true
		}
		var fresh []*netmail.Address
		for _, a := range distinct(addrs) {
			if !known[strings.ToLower(a.Address)] {
				fresh = append(fresh, a)
			}
		}
		if len(fresh) > 0 && now.Book == "" {
			if now.Book, err = p.dav.Create(ctx, s, name, dav.DefaultColor, dav.SourceHere, nil); err != nil {
				return err
			}
			if err := s.Store().Put(ctx, previousKey, &now); err != nil {
				return err
			}
		}
		for _, a := range fresh {
			if _, err := addCard(ctx, c, now.Book, a); err != nil {
				return err
			}
		}
	}
	if now == was {
		return nil
	}
	return s.Store().Put(ctx, previousKey, &now)
}
