package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	netmail "net/mail"
	"slices"
	"strings"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/contacts"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	pimweb "git.mehdix.org/alborz/pim/web"
	"git.mehdix.org/alborz/web"
	"github.com/emersion/go-webdav/carddav"
)

func (p *plugin) pooledBooks(ctx *web.Context) ([]pim.Account[*carddav.Client], error) {
	return pimweb.Pooled(ctx, p.DAV, p.Open, contacts.ErrNoAddressBook)
}

type plugin struct{ *contacts.Books }

// Mount serves contacts.
func Mount(g *web.Group, st *store.Store) error {
	srv := g.Server()
	b, err := contacts.New(srv.Server, st)
	if err != nil {
		return err
	}
	p := &plugin{b}

	registerRoutes(p, g)
	web.Extend(srv, p.collectionPage().ShowOffer(p.DAV))
	// Calendar registers first, so the collections on alborz come last.
	srv.OnServers = append(srv.OnServers, pimweb.Cards(p.DAV, "settings.davcontacts", bookAbilities), pimweb.HereCard(p.DAV))
	srv.OnAccountReady = append(srv.OnAccountReady, p.warm, p.seed)

	// A card attached to a mail is filed from its own row.
	web.Extend(srv, func(ctx *web.Context, data *mail.Shown) error {
		if !hasAttachment(data.Message, "text/vcard", "text/x-vcard", "text/directory") {
			return nil
		}
		groups, err := p.writableBookGroups(ctx)
		if err != nil || len(groups) == 0 {
			return nil
		}
		data.ImportBooks = groups
		return nil
	})

	web.Extend(srv, func(ctx *web.Context, data *mail.Shown) error {
		groups, err := p.writableBookGroups(ctx)
		if err != nil || len(groups) == 0 {
			return nil
		}
		var addrs []*netmail.Address
		env := data.Message.Envelope
		for _, a := range slices.Concat(env.From, env.To, env.Cc) {
			addrs = append(addrs, &netmail.Address{Name: a.Name, Address: a.Addr()})
		}
		data.Contact = &mail.ContactOffer{Addresses: contacts.Distinct(addrs), Books: groups}
		return nil
	})

	srv.OnSent = append(srv.OnSent, func(ctx *web.Context, sender *alborz.Login, to []*netmail.Address) error {
		return p.Remember(ctx.Request().Context(), sender, ctx.T("contacts.previous"), to, false)
	})

	srv.Suggest = append(srv.Suggest, func(ctx *web.Context) []string {
		c, addressBooks, err := p.Open(ctx.Request().Context(), ctx.Login)
		if err != nil {
			// Suggestions are a convenience: a missing or silent address book must not block compose.
			if err != contacts.ErrNoAddressBook {
				ctx.Logger().Printf("carddav: no suggestions for compose: %v", err)
			}
			return nil
		}
		addrs, err := contacts.Emails(ctx.Request().Context(), c, addressBooks)
		if err != nil {
			ctx.Logger().Printf("carddav: no suggestions from one book: %v", err)
		}
		// Written as a recipient is, name before address, since it is inserted verbatim.
		entries := make([]string, len(addrs))
		for i, a := range addrs {
			entries[i] = a.Address
			if a.Name != "" {
				entries[i] = a.String()
			}
		}
		return entries
	})

	return nil
}

func (p *plugin) writableBookGroups(ctx *web.Context) ([]dav.Group, error) {
	accounts, err := p.pooledBooks(ctx)
	if err != nil {
		return nil, err
	}
	return pim.WritableGroups(accounts, func(ab dav.Collection) bool { return ab.Writable }), nil
}

func bookAbilities(found dav.Advertised) []web.Ability {
	return []web.Ability{
		{Label: "settings.davability3", Hint: "settings.davability3hint", Have: found.Has("3")},
		{Label: "settings.davabilityacl", Hint: "settings.davabilityaclhint", Have: found.Has("access-control")},
		{Label: "settings.davabilitycards", Hint: "settings.davabilitycardshint", Have: found.Has("addressbook")},
		{Label: "settings.davabilitymkcol", Hint: "settings.davabilitymkcolhint", Have: found.Has("extended-mkcol")},
	}
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
	part, err := mail.ParsePartPath(ctx.FormValue("part"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	raw, _, err := mail.PartAt(ctx.Request().Context(), ctx.Login, mboxName, uid, part)
	if err != nil {
		return err
	}
	acct, bookPath, ok := strings.Cut(ctx.FormValue("addressbook"), "|")
	login := ctx.LoginFor(acct)
	if !ok || login == nil {
		return web.NewError(http.StatusBadRequest, "no address book to file it in")
	}
	c, books, err := p.Open(ctx.Request().Context(), login)
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
	n, err := contacts.Import(ctx.Request().Context(), c, bookPath, raw)
	if pim.ImportAnswered(err) {
		ctx.Notify(pimweb.ImportRefused(ctx, err))
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

// addAddress adds one of a message's addresses to the address book chosen.
func (p *plugin) addAddress(ctx *web.Context) error {
	addr, err := netmail.ParseAddress(ctx.FormValue("address"))
	if err != nil {
		return web.NewError(http.StatusBadRequest, err)
	}
	to, err := pimweb.Destination(ctx, ctx.FormValue("addressbook"), p.Open, func(book dav.Collection) bool { return book.Writable })
	if errors.Is(err, pim.ErrNoDestination) {
		return web.NewError(http.StatusBadRequest, "no address book to add it to")
	} else if err != nil {
		return err
	}
	books, err := p.DAV.Collections(ctx.Request().Context(), ctx.LoginFor(to.Account))
	if err != nil {
		return err
	}
	book := dav.At(books, to.Path)
	back := ctx.NextOr(ctx.URL("contacts.list"))
	held, err := contacts.Emails(ctx.Request().Context(), to.Client, []dav.Collection{*book})
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
	ao, err := contacts.AddCard(ctx.Request().Context(), to.Client, to.Path, addr)
	if err != nil {
		ctx.Notify(web.Notice{Kind: web.NoticeFailed, Text: fmt.Sprintf(ctx.T("form.saverefused"), err)})
		return ctx.Redirect(http.StatusFound, back)
	}
	named := contacts.AddressObject{AddressObject: ao}
	sentence := ctx.T("notice.contactcreated")
	href := ctx.Path("contacts.contact", named.Path) + "?account=" + web.QueryValue(to.Account)
	ctx.Notify(web.Notice{Kind: web.NoticeDone,
		Text:   fmt.Sprintf(sentence, named.DisplayName()),
		Markup: template.HTML(fmt.Sprintf(sentence, web.NoticeLink(named.DisplayName(), href)))})
	return ctx.Redirect(http.StatusFound, back)
}

// seed fills Previous recipients from Sent, once per account.
func (p *plugin) seed(ctx *web.Context, s *alborz.Login) {
	name, log := ctx.T("contacts.previous"), ctx.Logger()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), dav.WarmBudget)
		defer cancel()
		if err := p.Seed(bg, s, name); err != nil {
			log.Printf("carddav: previous recipients of %s: %v", s.Username(), err)
		}
	}()
}
